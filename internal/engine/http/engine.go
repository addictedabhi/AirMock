// Package httpengine implements the shared HTTP-family engine: REST for
// phase 1.1, with SOAP/GraphQL matchers layered on in phases 1.5/1.6 onto
// the same net/http.Server and route table.
package httpengine

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
	sessreg "github.com/addictedabhi/airmock/internal/session"
)

var _ engine.Engine = (*Engine)(nil)

type Engine struct {
	mux   atomic.Pointer[chi.Mux]
	cors  atomic.Pointer[CORSConfig]
	mocks map[string]*mock.Definition
	mu    sync.RWMutex

	srv *http.Server

	tlsMu       sync.Mutex
	tlsSrv      *http.Server
	tlsSettings atomic.Pointer[TLSSettings]

	scheduler       mock.CallbackScheduler
	scenarioStepper ScenarioStepper
	hitLogger       HitLogger
	projectPorts    ProjectPortResolver
	emailTemplates  mock.EmailTemplateResolver
	dynamicValues   mock.DynamicValueSource

	// sessReg tracks live WS connections only — REST/SOAP/GraphQL are
	// request-response and have no notion of a "connected session," so
	// every List/Close/Send call here is scoped to a WS mock's ID.
	sessReg *sessreg.Registry[*wsSession]

	// redactedHeaderKeys holds *map[string]bool, set via SetRedactedHeaders;
	// nil until then, in which case redactedHeaderSet() falls back to
	// defaultRedactedHeaderKeys (see capture.go).
	redactedHeaderKeys atomic.Pointer[map[string]bool]

	// extraMu guards the extra-listener bookkeeping below (structural
	// changes only — request serving reads the per-port mux lock-free via
	// its own atomic pointer, same reasoning as the default e.mux).
	extraMu      sync.Mutex
	extraServers map[int]*http.Server
	extraMuxes   map[int]*atomic.Pointer[chi.Mux]
	// extraTLS holds a port's live TLSSettings pointer ONLY while that
	// port's project has TLS enabled — a port's presence as a key here (not
	// just a non-nil value) is what "this port is currently running as
	// TLS" means; a plain-HTTP port has no entry at all. Letting a
	// project's cert/mTLS settings be swapped without restarting its
	// listener mirrors the shared gateway's own UpdateTLSSettings.
	extraTLS map[int]*atomic.Pointer[TLSSettings]

	projectTLS ProjectTLSResolver
}

// ProjectPortResolver looks up the dedicated gateway port (0 = none — serve
// on the shared default gateway instead) configured for a mock's project,
// letting a Mock Project optionally isolate its REST/SOAP/GraphQL APIs on
// their own port, the same idea as each TCP mock already owning its own
// listener.
type ProjectPortResolver interface {
	GatewayPortForProject(projectID string) (int, error)
}

// SetProjectPortResolver wires up per-project dedicated ports. Optional: an
// engine with no resolver set (or a mock with no ProjectID) just serves
// everything on the shared default gateway, as before this existed.
func (e *Engine) SetProjectPortResolver(r ProjectPortResolver) {
	e.projectPorts = r
}

func (e *Engine) resolvePort(projectID string) int {
	if projectID == "" || e.projectPorts == nil {
		return 0
	}
	port, err := e.projectPorts.GatewayPortForProject(projectID)
	if err != nil {
		return 0
	}
	return port
}

// ProjectTLSResolver resolves the ready-to-use TLS settings (certificate
// already loaded from the store, not just an ID) for a project's own
// dedicated listener — nil, nil means "no TLS for this project," serving
// its dedicated port as plain HTTP, same as before this existed. Kept
// separate from ProjectPortResolver (rather than folding TLS into it) so an
// engine can have a port resolver with no TLS resolver at all, same as
// gateway TLS is itself optional.
type ProjectTLSResolver interface {
	TLSSettingsForProject(projectID string) (*TLSSettings, error)
}

// SetProjectTLSResolver wires up per-project TLS. Optional: with none set,
// every project's dedicated port (if any) serves plain HTTP regardless of
// that project's own TLS config, same as before this existed.
func (e *Engine) SetProjectTLSResolver(r ProjectTLSResolver) {
	e.projectTLS = r
}

func (e *Engine) resolveProjectTLS(projectID string) *TLSSettings {
	if projectID == "" || e.projectTLS == nil {
		return nil
	}
	settings, err := e.projectTLS.TLSSettingsForProject(projectID)
	if err != nil {
		log.Printf("airmock: resolving TLS for project %q: %v — serving its dedicated port as plain HTTP", projectID, err)
		return nil
	}
	return settings
}

// SetCallbackScheduler wires up async mock support. Optional: an engine
// with no scheduler set still serves sync mocks fine, and logs (rather than
// panics) if an async mock is hit before one is configured.
//
// mock.CallbackScheduler/mock.EmailTemplateResolver (below) are declared
// once in internal/mock rather than as a per-engine-package copy, since
// TCP interactions can now trigger the exact same kind of async callback.
func (e *Engine) SetCallbackScheduler(s mock.CallbackScheduler) {
	e.scheduler = s
}

// SetEmailTemplateResolver wires up named-template lookup for email-channel
// async callbacks. Optional: a mock that sets EmailTemplateID with no
// resolver configured just falls back to its own
// EmailSubjectTemplate/CallbackBodyTemplate, same as leaving
// EmailTemplateID empty.
func (e *Engine) SetEmailTemplateResolver(r mock.EmailTemplateResolver) {
	e.emailTemplates = r
}

// ScenarioStepper is the narrow slice of internal/mock.Store that scenario
// mocks need to persist/advance their per-session step counter.
type ScenarioStepper interface {
	AdvanceScenarioStep(mockID, sessionKey string, totalSteps int, loop bool) (int, error)
}

// SetScenarioStepper wires up stateful/scenario mock support.
func (e *Engine) SetScenarioStepper(s ScenarioStepper) {
	e.scenarioStepper = s
}

// HitLogger is the narrow slice of internal/hitlog.Store that proxy-mode
// mocks need to record a captured exchange.
type HitLogger interface {
	Record(e *hitlog.Entry) error
}

// SetDynamicValues wires up counter()/csv() template-function support.
// Optional: a nil source (the default) just makes counter()/csv() calls in
// a body template return a template execution error, same as calling any
// other undefined behavior — every other placeholder keeps working fine.
func (e *Engine) SetDynamicValues(dv mock.DynamicValueSource) {
	e.dynamicValues = dv
}

// SetHitLogger wires up proxy-capture recording. Optional: a proxy mock
// still forwards and returns the real response fine with no logger set,
// it just won't have anything to promote-to-mock from later.
func (e *Engine) SetHitLogger(l HitLogger) {
	e.hitLogger = l
}

func New() *Engine {
	e := &Engine{mocks: map[string]*mock.Definition{}, sessReg: sessreg.NewRegistry[*wsSession]()}
	e.mux.Store(chi.NewRouter())
	return e
}

func (e *Engine) Name() string { return "http" }

func (e *Engine) Start(ctx context.Context, cfg engine.ListenerConfig) error {
	e.srv = &http.Server{
		Addr: cfg.Addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			e.mux.Load().ServeHTTP(w, r)
		}),
	}

	errCh := make(chan error, 1)
	go func() {
		if err := e.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-time.After(150 * time.Millisecond):
		return nil // listener is up (best-effort: no error within the startup window)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) Stop(ctx context.Context) error {
	e.StopTLS(ctx)

	e.extraMu.Lock()
	for _, srv := range e.extraServers {
		srv.Shutdown(ctx)
	}
	e.extraServers = nil
	e.extraMuxes = nil
	e.extraTLS = nil
	e.extraMu.Unlock()

	if e.srv == nil {
		return nil
	}
	return e.srv.Shutdown(ctx)
}

func (e *Engine) RegisterMock(m *mock.Definition) error {
	e.mu.Lock()
	e.mocks[m.ID] = m
	e.mu.Unlock()
	// Force-closes any already-connected WS session for this mock — a no-op
	// for every other mock type (REST/SOAP/GraphQL, and a WS mock with no
	// live connections), same reasoning as TCP/MQTT's own RegisterMock: a
	// client connected before a config/interaction change otherwise kept
	// talking to the OLD behavior indefinitely.
	e.sessReg.CloseAll(m.ID, func(s *wsSession) { s.conn.CloseNow() })
	return e.rebuild()
}

func (e *Engine) UnregisterMock(id string) error {
	e.mu.Lock()
	delete(e.mocks, id)
	e.mu.Unlock()
	e.sessReg.CloseAll(id, func(s *wsSession) { s.conn.CloseNow() })
	return e.rebuild()
}

// ListSessions returns every currently-connected WS session for one mock —
// the admin API's "Connected sessions" panel.
func (e *Engine) ListSessions(mockID string) []sessreg.Info {
	return e.sessReg.List(mockID)
}

// CloseSession forcibly disconnects one WS session by id.
func (e *Engine) CloseSession(mockID, sessionID string) error {
	sess, ok := e.sessReg.Get(mockID, sessionID)
	if !ok {
		return errors.New("session not found")
	}
	sess.conn.CloseNow()
	return nil
}

// SendToSession writes payload as a text message directly to one targeted
// WS session, bypassing this mock's own interaction-matching entirely —
// the operator pushing an unsolicited message to see how a connected
// client handles it, not a reply to something the client sent.
func (e *Engine) SendToSession(mockID, sessionID, payload string, extra map[string]string) error {
	sess, ok := e.sessReg.Get(mockID, sessionID)
	if !ok {
		return errors.New("session not found")
	}
	return sess.writeText(context.Background(), []byte(payload)) // bumps the outbound counter itself
}

// buildMux renders one route table from a set of mock definitions — used
// both for the shared default gateway and for each project's dedicated
// port, so a project with its own port gets the exact same REST/SOAP/
// GraphQL matching behavior as the default gateway, just on an isolated
// listener.
func (e *Engine) buildMux(defs []*mock.Definition) *chi.Mux {
	r := chi.NewRouter()
	r.Use(e.corsMiddleware)

	soapGroups := map[string][]*mock.Definition{}
	graphqlGroups := map[string][]*mock.Definition{}

	for _, m := range defs {
		switch m.ProtocolType {
		case "rest":
			def := m // capture for closure
			e.registerRestRoute(r, def)
		case "soap":
			soapGroups[m.PathPattern] = append(soapGroups[m.PathPattern], m)
		case "graphql":
			graphqlGroups[m.PathPattern] = append(graphqlGroups[m.PathPattern], m)
		case "ws":
			def := m // capture for closure
			r.Get(def.PathPattern, func(w http.ResponseWriter, req *http.Request) {
				e.serveWS(w, req, def)
			})
		}
	}

	// A GET mock also answers HEAD (same status and headers, no body, which
	// net/http enforces) unless a HEAD mock is defined for that path.
	explicitHead := map[string]bool{}
	for _, m := range defs {
		if m.ProtocolType == "rest" && strings.EqualFold(m.Method, http.MethodHead) {
			explicitHead[m.PathPattern] = true
		}
	}
	for _, m := range defs {
		if m.ProtocolType == "rest" && strings.EqualFold(m.Method, http.MethodGet) && !explicitHead[m.PathPattern] {
			def := m
			r.Head(def.PathPattern, func(w http.ResponseWriter, req *http.Request) {
				e.serveRest(w, req, def)
			})
		}
	}

	// SOAP operations of one service, and GraphQL operations of one schema,
	// each share a single POST endpoint, disambiguated inside serveSoap/
	// serveGraphQL by SOAPAction/operation name — chi can only hold one
	// handler per (method, path), so mocks are grouped here rather than
	// registered individually.
	for path, group := range soapGroups {
		ops := group // capture for closure
		r.Post(path, func(w http.ResponseWriter, req *http.Request) {
			e.serveSoap(w, req, ops)
		})
	}
	for path, group := range graphqlGroups {
		ops := group // capture for closure
		r.Post(path, func(w http.ResponseWriter, req *http.Request) {
			e.serveGraphQL(w, req, ops)
		})
	}

	return r
}

// registerRestRoute wraps r.MethodFunc in a recover(): chi.Mux.MethodFunc
// PANICS (not just errors) on a Method it doesn't recognize. The admin API
// rejects an invalid Method before a REST mock is ever persisted (see
// validateMockShape's validRESTMethods check), but a row written before
// that validation existed could still be sitting in an install's database.
// Without this recover, one such def would panic mid-loop through buildMux
// — discarding the ENTIRE partially-built mux for this port, including
// every other, perfectly valid mock sharing it — every single time
// anything on this port is registered/updated/unregistered, since rebuild
// reruns buildMux from scratch on every such change.
func (e *Engine) registerRestRoute(r *chi.Mux, def *mock.Definition) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("airmock: skipping REST mock %q (%s), invalid route: %v", def.Name, def.ID, rec)
		}
	}()
	r.MethodFunc(def.Method, def.PathPattern, func(w http.ResponseWriter, req *http.Request) {
		e.serveRest(w, req, def)
	})
}

// Rebuild recomputes every project's dedicated listener (route table and
// TLS settings) from current mock/project state. RegisterMock/UnregisterMock
// already call this as a side effect of a mock changing, which is how a
// project's port/TLS config normally gets picked up — but a project's OWN
// settings (dedicated port, TLS bundle, client-cert mode) can change with
// no mock event at all, and nothing else re-syncs the running listener in
// that case. Callers that change project settings (not just mocks) must
// call this explicitly afterward.
func (e *Engine) Rebuild() error {
	return e.rebuild()
}

func (e *Engine) rebuild() error {
	e.mu.RLock()
	byPort := map[int][]*mock.Definition{}
	for _, m := range e.mocks {
		if !m.Enabled {
			continue
		}
		port := e.resolvePort(m.ProjectID)
		byPort[port] = append(byPort[port], m)
	}
	e.mu.RUnlock()

	e.mux.Store(e.buildMux(byPort[0]))
	delete(byPort, 0)
	return e.syncExtraListeners(byPort)
}

// syncExtraListeners starts a dedicated listener for every project port
// that now routes at least one mock, hot-swaps the route table (and, for a
// TLS-enabled project, the live cert/mTLS settings) for a port whose
// listener is already running, and shuts down any previously-started
// listener whose project no longer routes any mocks there (e.g. the
// project's port was cleared, or every mock in it was deleted/disabled).
func (e *Engine) syncExtraListeners(byPort map[int][]*mock.Definition) error {
	e.extraMu.Lock()
	defer e.extraMu.Unlock()

	if e.extraMuxes == nil {
		e.extraMuxes = map[int]*atomic.Pointer[chi.Mux]{}
		e.extraServers = map[int]*http.Server{}
		e.extraTLS = map[int]*atomic.Pointer[TLSSettings]{}
	}

	for port, defs := range byPort {
		mux := e.buildMux(defs)
		// All defs sharing a port were routed here via the SAME project's
		// GatewayPort (resolvePort only returns non-zero via
		// GatewayPortForProject(m.ProjectID)), so any one def's ProjectID
		// tells us which project's TLS config governs this port.
		desired := e.resolveProjectTLS(defs[0].ProjectID)

		tlsPtr, wasTLS := e.extraTLS[port]
		if _, running := e.extraMuxes[port]; running {
			if wasTLS == (desired != nil) {
				// TLS-ness unchanged — hot-swap in place, no restart needed.
				e.extraMuxes[port].Store(mux)
				if desired != nil {
					tlsPtr.Store(desired)
				}
				continue
			}
			// A listener can't flip between plain and TLS in place — tear it
			// down so the code below starts it fresh with the new mode.
			e.extraServers[port].Shutdown(context.Background())
			delete(e.extraServers, port)
			delete(e.extraMuxes, port)
			delete(e.extraTLS, port)
		}
		if err := e.startExtraListener(port, mux, desired); err != nil {
			return err
		}
	}

	for port, srv := range e.extraServers {
		if _, stillUsed := byPort[port]; !stillUsed {
			srv.Shutdown(context.Background())
			delete(e.extraServers, port)
			delete(e.extraMuxes, port)
			delete(e.extraTLS, port)
		}
	}
	return nil
}

// startExtraListener must be called with extraMu held. tlsSettings nil
// means plain HTTP; non-nil starts this dedicated port as HTTPS instead,
// with its own GetConfigForClient so a later cert/mTLS change (see
// syncExtraListeners above) can hot-swap without restarting the listener.
func (e *Engine) startExtraListener(port int, mux *chi.Mux, tlsSettings *TLSSettings) error {
	muxPtr := &atomic.Pointer[chi.Mux]{}
	muxPtr.Store(mux)

	srv := &http.Server{
		Addr: fmt.Sprintf(":%d", port),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			muxPtr.Load().ServeHTTP(w, r)
		}),
	}

	var tlsPtr *atomic.Pointer[TLSSettings]
	serve := srv.ListenAndServe
	if tlsSettings != nil {
		tlsPtr = &atomic.Pointer[TLSSettings]{}
		tlsPtr.Store(tlsSettings)
		srv.TLSConfig = &tls.Config{
			GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
				return tlsConfigFromSettings(tlsPtr.Load())
			},
		}
		serve = func() error { return srv.ListenAndServeTLS("", "") }
	}

	errCh := make(chan error, 1)
	go func() {
		if err := serve(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("start dedicated gateway listener on port %d: %w", port, err)
	case <-time.After(150 * time.Millisecond):
		// listener is up (best-effort: no error within the startup window)
	}

	e.extraMuxes[port] = muxPtr
	e.extraServers[port] = srv
	if tlsPtr != nil {
		e.extraTLS[port] = tlsPtr
	}
	return nil
}

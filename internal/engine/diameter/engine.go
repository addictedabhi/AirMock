// Package diameterengine implements a minimal Diameter (RFC 6733) peer —
// the CER/CEA capabilities-exchange handshake and DWR/DWA watchdog, plus
// Credit-Control-Request/Answer (RFC 4006) — enough for a real Diameter
// client library to bind, hold a session open, and complete charging/
// credit-control exchanges against it. There's deliberately no other
// application (no Gx/Rx/S6a-specific commands, no grouped AVPs beyond what
// CCR/CCA already require) — a client library completing the handshake and
// a CCR/CCA round trip covers the "test my app's own Diameter integration"
// use case this mock exists for, at a fraction of the full protocol's
// application surface.
//
// The wire protocol (message/AVP framing, dictionary-based AVP encoding,
// and — critically — the entire CER/CEA+DWR/DWA state machine) is handled
// by github.com/fiorix/go-diameter's diam/sm package rather than
// hand-rolled here, the same reasoning as the Kafka and SMPP engines
// reusing their own respective dependencies: it's a maintained,
// already-correct implementation of both the wire format and the
// handshake/keepalive protocol every real Diameter peer expects, so
// building a mock on top of it means registering one handler for CCR, not
// re-implementing RFC 6733's peer state machine from scratch.
package diameterengine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"github.com/fiorix/go-diameter/v4/diam/sm"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
	sessreg "github.com/addictedabhi/airmock/internal/session"
)

var _ engine.Engine = (*Engine)(nil)

const (
	defaultOriginHost  = "airmock"
	defaultOriginRealm = "airmock.test"
)

// HitLogger is the narrow slice of internal/hitlog.Store this engine needs
// to record each inbound CCR.
type HitLogger interface {
	Record(e *hitlog.Entry) error
}

type Engine struct {
	mu        sync.Mutex
	listeners map[string]net.Listener // keyed by mock ID
	sessReg   *sessreg.Registry[diam.Conn]
	hitLogger HitLogger
}

func New() *Engine {
	return &Engine{listeners: map[string]net.Listener{}, sessReg: sessreg.NewRegistry[diam.Conn]()}
}

func (e *Engine) Name() string { return "diameter" }

// SetHitLogger wires up hit-log recording. Optional: an engine with no
// logger set still serves Diameter mocks fine, it just has nothing to show
// in the Hit Log / dashboard for them.
func (e *Engine) SetHitLogger(l HitLogger) {
	e.hitLogger = l
}

// Start is a no-op — like TCP/SMTP/MQTT/FTP/Kafka/SMPP, each mock binds its
// own port from RegisterMock instead of one shared listener here.
func (e *Engine) Start(ctx context.Context, cfg engine.ListenerConfig) error {
	return nil
}

func (e *Engine) Stop(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, ln := range e.listeners {
		ln.Close()
		delete(e.listeners, id)
	}
	return nil
}

func (e *Engine) RegisterMock(m *mock.Definition) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if ln, ok := e.listeners[m.ID]; ok {
		ln.Close()
		delete(e.listeners, m.ID)
	}
	// Force-closes every already-connected peer for this mock — called with
	// e.mu already held. diam.Server.Close (which we never even call
	// directly here, since ln.Close() above already stops accepting new
	// connections) explicitly documents that it does NOT affect already-
	// accepted connections; a Diameter peer connection is meant to stay open
	// indefinitely (that's what the DWR/DWA watchdog is for), so without
	// this, updating or unregistering a mock would leave any already-bound
	// peer's connection running forever.
	e.sessReg.CloseAll(m.ID, func(c diam.Conn) { c.Close() })
	if !m.Enabled || m.Diameter == nil {
		return nil
	}

	settings := &sm.Settings{
		OriginHost:  datatype.DiameterIdentity(originHostOf(m.Diameter)),
		OriginRealm: datatype.DiameterIdentity(originRealmOf(m.Diameter)),
		VendorID:    13,
		ProductName: "AirMock",
	}
	mux := sm.New(settings)
	mux.Handle("CCR", e.handleCCR(m))

	srv := &diam.Server{
		Handler: mux,
		OnNewConnection: func(c diam.Conn) {
			regID := e.sessReg.Add(m.ID, "diameter", c, c.RemoteAddr().String(), nil)
			// Stashed on the connection's own context so handleCCR (which
			// only receives the diam.Conn/diam.Message the go-diameter
			// library hands it, not anything this engine controls) can
			// recover which registry entry this connection is, to capture
			// the peer's real Origin-Host/Realm once its first CCR arrives
			// (see ccr.go) and to bump activity/message counters.
			c.SetContext(context.WithValue(c.Context(), regIDContextKey{}, regID))
			if cn, ok := c.(diam.CloseNotifier); ok {
				go func() {
					<-cn.CloseNotify()
					e.sessReg.Remove(m.ID, regID)
				}()
			}
		},
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", m.Diameter.Port))
	if err != nil {
		return fmt.Errorf("diameter mock %q: listen on port %d: %w", m.Name, m.Diameter.Port, err)
	}
	e.listeners[m.ID] = ln
	go srv.Serve(ln)
	return nil
}

func (e *Engine) UnregisterMock(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ln, ok := e.listeners[id]; ok {
		ln.Close()
		delete(e.listeners, id)
	}
	e.sessReg.CloseAll(id, func(c diam.Conn) { c.Close() })
	return nil
}

// regIDContextKey is the diam.Conn context key OnNewConnection stashes each
// connection's registry session id under (see RegisterMock) — an unexported
// zero-size struct so it can never collide with a key some other package
// might store on the same shared context.Context.
type regIDContextKey struct{}

// regIDOf recovers a connection's registry session id, or "" if this
// connection was somehow never registered (defensive only; every real
// connection goes through OnNewConnection first).
func regIDOf(c diam.Conn) string {
	id, _ := c.Context().Value(regIDContextKey{}).(string)
	return id
}

// ListSessions returns every currently-connected Diameter peer for one
// mock — the admin API's "Connected sessions" panel. Diameter has no
// SendToSession: this mock only implements CCR/CCA (see the package doc),
// so there's no other command an operator could meaningfully trigger —
// adding one would mean building out real command support first, left as
// explicit future work.
func (e *Engine) ListSessions(mockID string) []sessreg.Info {
	return e.sessReg.List(mockID)
}

// CloseSession forcibly disconnects one peer by id.
func (e *Engine) CloseSession(mockID, sessionID string) error {
	conn, ok := e.sessReg.Get(mockID, sessionID)
	if !ok {
		return errors.New("session not found")
	}
	conn.Close()
	return nil
}

func originHostOf(cfg *mock.DiameterConfig) string {
	if cfg.OriginHost != "" {
		return cfg.OriginHost
	}
	return defaultOriginHost
}

func originRealmOf(cfg *mock.DiameterConfig) string {
	if cfg.OriginRealm != "" {
		return cfg.OriginRealm
	}
	return defaultOriginRealm
}

func (e *Engine) recordHit(def *mock.Definition, sessionID string, ccRequestType, resultCode int, answered bool) {
	if e.hitLogger == nil {
		return
	}
	respBody := "(no answer — fault)"
	if answered {
		respBody = fmt.Sprintf("CCA Result-Code=%d", resultCode)
	}
	err := e.hitLogger.Record(&hitlog.Entry{
		MockID:       def.ID,
		ProtocolType: "diameter",
		Direction:    hitlog.DirectionInbound,
		Path:         sessionID,
		RequestBody:  fmt.Sprintf("CCR CC-Request-Type=%d", ccRequestType),
		ResponseBody: respBody,
	})
	if err != nil {
		log.Printf("airmock: failed to record diameter hit for mock %q: %v", def.ID, err)
	}
}

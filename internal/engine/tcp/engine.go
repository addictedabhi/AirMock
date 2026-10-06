// Package tcpengine implements a raw line-oriented TCP/Telnet mock engine —
// the first Phase 2 protocol, added on top of the plugin Engine interface
// that HTTP/REST/SOAP/GraphQL already implement. Unlike the HTTP family,
// which shares one gateway port disambiguated by path/method/operation, TCP
// has no equivalent of a path to multiplex several mocks onto one port, so
// each TCP mock owns its own dedicated listener, started/stopped as mocks
// are registered/unregistered rather than at one shared Start().
package tcpengine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/addictedabhi/airmock/internal/certs"
	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
	sessreg "github.com/addictedabhi/airmock/internal/session"
)

var _ engine.Engine = (*Engine)(nil)

// HitLogger is the narrow slice of internal/hitlog.Store that TCP mocks need
// to record each line-in/response-out exchange.
type HitLogger interface {
	Record(e *hitlog.Entry) error
}

// CertProvider is the narrow slice of internal/certs.Store a TLS-enabled
// TCP mock needs — look up a stored certificate by ID, or a bundle (a named
// CA+server+client cert group) by ID.
type CertProvider interface {
	Get(id string) (*certs.Certificate, error)
	GetBundle(id string) (*certs.Bundle, error)
}

type Engine struct {
	mu             sync.Mutex
	listeners      map[string]net.Listener // keyed by mock ID
	sessReg        *sessreg.Registry[net.Conn]
	hitLogger      HitLogger
	certProvider   CertProvider
	scheduler      mock.CallbackScheduler
	emailTemplates mock.EmailTemplateResolver
	dynamicValues  mock.DynamicValueSource
}

func New() *Engine {
	return &Engine{listeners: map[string]net.Listener{}, sessReg: sessreg.NewRegistry[net.Conn]()}
}

func (e *Engine) Name() string { return "tcp" }

// SetHitLogger wires up hit-log recording. Optional: an engine with no
// logger set still serves TCP mocks fine, it just has nothing to show in
// the Hit Log / dashboard for them.
func (e *Engine) SetHitLogger(l HitLogger) {
	e.hitLogger = l
}

// SetDynamicValues wires up counter()/csv() template-function support. See
// httpengine.Engine.SetDynamicValues's doc comment for the nil-safe behavior.
func (e *Engine) SetDynamicValues(dv mock.DynamicValueSource) {
	e.dynamicValues = dv
}

// SetCertProvider wires up TLS-enabled TCP mocks. Optional: a mock with
// TCP.TLS set fails to register with a clear error if no provider is
// configured, rather than silently serving plaintext.
func (e *Engine) SetCertProvider(p CertProvider) {
	e.certProvider = p
}

// SetCallbackScheduler wires up async-callback support for TCP
// interactions (see mock.TCPInteraction.Async). Optional: an interaction
// with Async set but no scheduler configured just logs and skips
// scheduling, the same as an async REST mock hit before one is configured.
func (e *Engine) SetCallbackScheduler(s mock.CallbackScheduler) {
	e.scheduler = s
}

// SetEmailTemplateResolver wires up named email-template lookup for
// interactions whose Async.CallbackChannel is "email" and reference one via
// EmailTemplateID.
func (e *Engine) SetEmailTemplateResolver(r mock.EmailTemplateResolver) {
	e.emailTemplates = r
}

// Start is a no-op — see the package doc: each mock binds its own port from
// RegisterMock instead of one shared listener here.
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

// RegisterMock (re)binds the listener for m's port. Called for both create
// and update, so any previously bound listener for this mock ID is always
// closed first — covers a port change, a disable (Enabled=false leaves it
// unbound), or a plain re-registration on startup.
func (e *Engine) RegisterMock(m *mock.Definition) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if ln, ok := e.listeners[m.ID]; ok {
		ln.Close()
		delete(e.listeners, m.ID)
	}
	// Force-closes every already-connected session for this mock, not just
	// the listener — without this, a client connected before an update
	// (e.g. a port/TLS/interaction change) kept talking to the OLD
	// configuration indefinitely, since handleConn's read loop has no way
	// to learn its mock changed out from under it. Matches how MQTT's own
	// RegisterMock already behaves for the same reason.
	e.sessReg.CloseAll(m.ID, func(c net.Conn) { c.Close() })
	if !m.Enabled || m.TCP == nil {
		return nil
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", m.TCP.Port))
	if err != nil {
		return fmt.Errorf("tcp mock %q: listen on port %d: %w", m.Name, m.TCP.Port, err)
	}
	if m.TCP.TLS != nil {
		ln, err = e.wrapTLS(ln, m.TCP.TLS)
		if err != nil {
			return fmt.Errorf("tcp mock %q: %w", m.Name, err)
		}
	}
	e.listeners[m.ID] = ln
	go e.acceptLoop(ln, m)
	return nil
}

// wrapTLS wraps ln so every accepted connection is a TLS handshake
// presenting the given stored certificate — each TCP mock has its own
// port, so (unlike the HTTP gateway) TLS is genuinely per-mock here.
// ClientCertMode/ClientCAID mirror the HTTP gateway's own mTLS wiring
// (internal/engine/http's tlsConfigFromSettings): ClientCAs built from the
// referenced CA's cert, ClientAuth set per mode — just built once here
// rather than behind an atomic pointer/GetConfigForClient, since a TCP
// mock's listener is already fully torn down and rebound on every
// RegisterMock, unlike HTTP's one long-lived shared listener.
func (e *Engine) wrapTLS(ln net.Listener, cfg *mock.TCPTLSConfig) (net.Listener, error) {
	if e.certProvider == nil {
		return nil, fmt.Errorf("tls requested but no certificate store is configured")
	}
	certID, clientCAID, err := resolveTLSRefs(e.certProvider, cfg)
	if err != nil {
		return nil, err
	}
	cert, err := e.certProvider.Get(certID)
	if err != nil {
		return nil, fmt.Errorf("load certificate %q: %w", certID, err)
	}
	tlsCert, err := tls.X509KeyPair([]byte(cert.CertPEM), []byte(cert.KeyPEM))
	if err != nil {
		return nil, fmt.Errorf("parse certificate %q: %w", certID, err)
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{tlsCert}}
	switch cfg.ClientCertMode {
	case string(certs.ClientCertOptional), string(certs.ClientCertRequired):
		pool, err := e.clientCAPool(clientCAID)
		if err != nil {
			return nil, err
		}
		tlsConfig.ClientCAs = pool
		if cfg.ClientCertMode == string(certs.ClientCertRequired) {
			tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		} else {
			tlsConfig.ClientAuth = tls.VerifyClientCertIfGiven
		}
	}
	return tls.NewListener(ln, tlsConfig), nil
}

// resolveTLSRefs resolves cfg's effective certificate/client-CA IDs — if
// BundleID is set, the bundle's own ServerCertID/CAID take precedence over
// any directly-set CertificateID/ClientCAID, since picking a bundle is
// meant to replace picking the two individually.
func resolveTLSRefs(provider CertProvider, cfg *mock.TCPTLSConfig) (certID, clientCAID string, err error) {
	certID, clientCAID = cfg.CertificateID, cfg.ClientCAID
	if cfg.BundleID == "" {
		return certID, clientCAID, nil
	}
	b, err := provider.GetBundle(cfg.BundleID)
	if err != nil {
		return "", "", fmt.Errorf("load certificate bundle %q: %w", cfg.BundleID, err)
	}
	if b.ServerCertID != "" {
		certID = b.ServerCertID
	}
	if b.CAID != "" {
		clientCAID = b.CAID
	}
	return certID, clientCAID, nil
}

func (e *Engine) clientCAPool(clientCAID string) (*x509.CertPool, error) {
	ca, err := e.certProvider.Get(clientCAID)
	if err != nil {
		return nil, fmt.Errorf("load client CA %q: %w", clientCAID, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(ca.CertPEM)) {
		return nil, fmt.Errorf("client CA %q: no valid PEM certificate found", clientCAID)
	}
	return pool, nil
}

func (e *Engine) UnregisterMock(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ln, ok := e.listeners[id]; ok {
		ln.Close()
		delete(e.listeners, id)
	}
	e.sessReg.CloseAll(id, func(c net.Conn) { c.Close() })
	return nil
}

// ListSessions returns every currently-connected TCP session for one mock —
// the admin API's "Connected sessions" panel.
func (e *Engine) ListSessions(mockID string) []sessreg.Info {
	return e.sessReg.List(mockID)
}

// CloseSession forcibly disconnects one session by id.
func (e *Engine) CloseSession(mockID, sessionID string) error {
	conn, ok := e.sessReg.Get(mockID, sessionID)
	if !ok {
		return errors.New("session not found")
	}
	return conn.Close()
}

// SendToSession writes payload directly to one targeted session's raw
// connection — a line-oriented mock's response is already whatever text
// the operator wants to appear, so this makes no attempt to add a line
// ending itself the way an automatic Interaction response does (see
// ensureLineEnding); the operator's payload is sent exactly as given.
func (e *Engine) SendToSession(mockID, sessionID, payload string, extra map[string]string) error {
	conn, ok := e.sessReg.Get(mockID, sessionID)
	if !ok {
		return errors.New("session not found")
	}
	if _, err := conn.Write([]byte(payload)); err != nil {
		return err
	}
	e.sessReg.Touch(mockID, sessionID, 0, 1)
	return nil
}

func (e *Engine) acceptLoop(ln net.Listener, def *mock.Definition) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed: unregistered, updated, or shutting down
		}
		go e.handleConn(conn, def)
	}
}

func (e *Engine) recordHit(def *mock.Definition, reqLine, respLine string, latencyMs int64) {
	if e.hitLogger == nil {
		return
	}
	err := e.hitLogger.Record(&hitlog.Entry{
		MockID:       def.ID,
		ProtocolType: "tcp",
		Direction:    hitlog.DirectionInbound,
		RequestBody:  reqLine,
		ResponseBody: respLine,
		LatencyMs:    latencyMs,
	})
	if err != nil {
		log.Printf("airmock: failed to record tcp hit for mock %q: %v", def.ID, err)
	}
}

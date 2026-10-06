// Package smtpengine implements a mock inbound SMTP listener — the mirror
// image of an async mock's "email" callback channel (internal/smtp), which
// SENDS mail. This engine RECEIVES it: a real SMTP dialogue
// (EHLO/MAIL FROM/RCPT TO/DATA/QUIT) that accepts or rejects a message
// against configured rules, for testing an application's own outbound-email
// code against a fake receiving server instead of a real mailbox.
//
// Structurally this mirrors internal/engine/tcp closely: each mock owns its
// own dedicated listener port (there's no equivalent of an HTTP path to
// multiplex several mocks onto one shared port), started/stopped as mocks
// are registered/unregistered rather than at one shared Start().
package smtpengine

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

// HitLogger is the narrow slice of internal/hitlog.Store this engine needs
// to record each received message.
type HitLogger interface {
	Record(e *hitlog.Entry) error
}

// CertProvider is the narrow slice of internal/certs.Store a TLS-enabled
// SMTP mock needs — look up a stored certificate by ID, or a bundle (a
// named CA+server+client cert group) by ID.
type CertProvider interface {
	Get(id string) (*certs.Certificate, error)
	GetBundle(id string) (*certs.Bundle, error)
}

type Engine struct {
	mu           sync.Mutex
	listeners    map[string]net.Listener // keyed by mock ID
	hitLogger    HitLogger
	certProvider CertProvider
	sessReg      *sessreg.Registry[net.Conn]
}

func New() *Engine {
	return &Engine{listeners: map[string]net.Listener{}, sessReg: sessreg.NewRegistry[net.Conn]()}
}

func (e *Engine) Name() string { return "smtp" }

// SetHitLogger wires up hit-log recording. Optional: an engine with no
// logger set still serves SMTP mocks fine, it just has nothing to show in
// the Hit Log / dashboard for them.
func (e *Engine) SetHitLogger(l HitLogger) {
	e.hitLogger = l
}

// SetCertProvider wires up TLS-enabled SMTP mocks. Optional: a mock with
// SMTP.TLS set fails to register with a clear error if no provider is
// configured, rather than silently serving plaintext.
func (e *Engine) SetCertProvider(p CertProvider) {
	e.certProvider = p
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
	// Force-closes every already-connected session for this mock, matching
	// TCP/MQTT's own RegisterMock — otherwise a client connected before an
	// update (port/TLS/rule change) kept talking to the OLD configuration
	// indefinitely.
	e.sessReg.CloseAll(m.ID, func(c net.Conn) { c.Close() })
	if !m.Enabled || m.SMTP == nil {
		return nil
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", m.SMTP.Port))
	if err != nil {
		return fmt.Errorf("smtp mock %q: listen on port %d: %w", m.Name, m.SMTP.Port, err)
	}
	if m.SMTP.TLS != nil {
		ln, err = e.wrapTLS(ln, m.SMTP.TLS)
		if err != nil {
			return fmt.Errorf("smtp mock %q: %w", m.Name, err)
		}
	}
	e.listeners[m.ID] = ln
	go e.acceptLoop(ln, m)
	return nil
}

// wrapTLS wraps ln so every accepted connection is a TLS handshake
// presenting the given stored certificate — implicit TLS on the listener
// itself (like a real port-465 relay) rather than an inline STARTTLS
// command, matching the same simplification internal/smtp's outbound Send
// makes when UseTLS is off: fewer states to implement correctly.
// ClientCertMode/ClientCAID mirror the HTTP gateway's own mTLS wiring (see
// internal/engine/tcp's identical wrapTLS for the shared rationale).
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

// ListSessions returns every currently-connected SMTP session for one
// mock — the admin API's "Connected sessions" panel. SMTP has no
// SendToSession: a real SMTP dialogue is entirely client-driven
// (EHLO/MAIL FROM/RCPT TO/DATA), with no server-push verb an operator
// could meaningfully trigger mid-conversation.
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

func (e *Engine) acceptLoop(ln net.Listener, def *mock.Definition) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed: unregistered, updated, or shutting down
		}
		go e.handleConn(conn, def)
	}
}

// recordHit logs the SMTP envelope addresses (MAIL FROM/RCPT TO — which can
// legitimately differ from the message's own From/To headers, e.g. a
// mailing list) above the raw message exactly as received, rather than
// synthesizing a second From/To/Subject header block on top of the one the
// message body almost always already carries itself.
func (e *Engine) recordHit(def *mock.Definition, from, to, body string, statusCode int, latencyMs int64) {
	if e.hitLogger == nil {
		return
	}
	reqBody := fmt.Sprintf("Envelope-From: %s\r\nEnvelope-To: %s\r\n\r\n%s", from, to, body)
	err := e.hitLogger.Record(&hitlog.Entry{
		MockID:         def.ID,
		ProtocolType:   "smtp",
		Direction:      hitlog.DirectionInbound,
		RequestBody:    reqBody,
		ResponseStatus: statusCode,
		LatencyMs:      latencyMs,
	})
	if err != nil {
		log.Printf("airmock: failed to record smtp hit for mock %q: %v", def.ID, err)
	}
}

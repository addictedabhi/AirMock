// Package ftpengine implements a minimal FTP mock server (RFC 959) — just
// enough (USER/PASS, SYST/TYPE/PWD/CWD, PASV-mode LIST/RETR/STOR, QUIT) to
// let a real FTP client library connect, list, download, and upload
// against it, without depending on a third-party FTP server or client
// library — the same hand-rolled-protocol approach already used for the
// TCP/SMTP/MQTT engines.
//
// Only passive mode (PASV) is supported, deliberately: active mode (PORT)
// requires the server to connect back out to the client, which is both
// more complex to implement correctly and unfriendly to firewalls/NAT —
// every mainstream FTP client (including curl) already defaults to
// passive mode, so there's no real client this excludes.
package ftpengine

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
)

var _ engine.Engine = (*Engine)(nil)

// HitLogger is the narrow slice of internal/hitlog.Store this engine needs
// to record each RETR/STOR.
type HitLogger interface {
	Record(e *hitlog.Entry) error
}

type Engine struct {
	mu        sync.Mutex
	listeners map[string]net.Listener // keyed by mock ID
	hitLogger HitLogger
}

func New() *Engine {
	return &Engine{listeners: map[string]net.Listener{}}
}

func (e *Engine) Name() string { return "ftp" }

// SetHitLogger wires up hit-log recording. Optional: an engine with no
// logger set still serves FTP mocks fine, it just has nothing to show in
// the Hit Log / dashboard for them.
func (e *Engine) SetHitLogger(l HitLogger) {
	e.hitLogger = l
}

// Start is a no-op — like TCP/SMTP/MQTT, each mock binds its own control
// port from RegisterMock instead of one shared listener here.
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
	if !m.Enabled || m.FTP == nil {
		return nil
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", m.FTP.Port))
	if err != nil {
		return fmt.Errorf("ftp mock %q: listen on port %d: %w", m.Name, m.FTP.Port, err)
	}
	e.listeners[m.ID] = ln
	go e.acceptLoop(ln, m)
	return nil
}

func (e *Engine) UnregisterMock(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ln, ok := e.listeners[id]; ok {
		ln.Close()
		delete(e.listeners, id)
	}
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

func (e *Engine) recordHit(def *mock.Definition, command, body string, size int) {
	if e.hitLogger == nil {
		return
	}
	err := e.hitLogger.Record(&hitlog.Entry{
		MockID:       def.ID,
		ProtocolType: "ftp",
		Direction:    hitlog.DirectionInbound,
		Path:         command,
		RequestBody:  body,
		ResponseBody: fmt.Sprintf("%d bytes", size),
	})
	if err != nil {
		log.Printf("airmock: failed to record ftp hit for mock %q: %v", def.ID, err)
	}
}

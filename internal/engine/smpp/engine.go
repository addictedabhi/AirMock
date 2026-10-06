// Package smppengine implements a minimal SMPP v3.4 SMSC mock —
// bind_transceiver, submit_sm/submit_sm_resp, deliver_sm/deliver_sm_resp,
// enquire_link/enquire_link_resp, and unbind only. There's deliberately no
// submit_multi, query_sm, replace_sm, cancel_sm, or data_sm — a real client
// library binding, submitting an SMS, and (optionally) receiving a
// rule-triggered deliver_sm back covers the "test my app's own SMS-sending
// code" use case this mock exists for, at a fraction of the full v3.4 PDU
// set.
//
// PDU encoding/decoding is handled by github.com/fiorix/go-smpp's own pdu
// package rather than hand-rolled here — the same reasoning as the Kafka
// engine reusing segmentio/kafka-go's protocol package: it's a maintained,
// already-correct implementation of the wire format (C-octet strings,
// length-prefixed short_message, the fixed 16-byte header), so building a
// mock on top of it means picking the right PDU constructor, not
// re-implementing binary framing from scratch.
package smppengine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
	sessreg "github.com/addictedabhi/airmock/internal/session"
)

var _ engine.Engine = (*Engine)(nil)

// HitLogger is the narrow slice of internal/hitlog.Store this engine needs
// to record each inbound submit_sm.
type HitLogger interface {
	Record(e *hitlog.Entry) error
}

type Engine struct {
	mu            sync.Mutex
	listeners     map[string]net.Listener // keyed by mock ID
	sessReg       *sessreg.Registry[*session]
	hitLogger     HitLogger
	dynamicValues mock.DynamicValueSource
}

func New() *Engine {
	return &Engine{listeners: map[string]net.Listener{}, sessReg: sessreg.NewRegistry[*session]()}
}

func (e *Engine) Name() string { return "smpp" }

// SetHitLogger wires up hit-log recording. Optional: an engine with no
// logger set still serves SMPP mocks fine, it just has nothing to show in
// the Hit Log / dashboard for them.
func (e *Engine) SetHitLogger(l HitLogger) {
	e.hitLogger = l
}

// SetDynamicValues wires up counter()/csv() template-function support. See
// httpengine.Engine.SetDynamicValues's doc comment for the nil-safe behavior.
func (e *Engine) SetDynamicValues(dv mock.DynamicValueSource) {
	e.dynamicValues = dv
}

// Start is a no-op — like TCP/SMTP/MQTT/FTP/Kafka, each mock binds its own
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
	e.sessReg.CloseAll(m.ID, func(s *session) { s.conn.Close() })
	if !m.Enabled || m.SMPP == nil {
		return nil
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", m.SMPP.Port))
	if err != nil {
		return fmt.Errorf("smpp mock %q: listen on port %d: %w", m.Name, m.SMPP.Port, err)
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
	e.sessReg.CloseAll(id, func(s *session) { s.conn.Close() })
	return nil
}

// ListSessions returns every currently-connected SMPP session for one
// mock — the admin API's "Connected sessions" panel.
func (e *Engine) ListSessions(mockID string) []sessreg.Info {
	return e.sessReg.List(mockID)
}

// CloseSession forcibly disconnects one session by id.
func (e *Engine) CloseSession(mockID, sessionID string) error {
	sess, ok := e.sessReg.Get(mockID, sessionID)
	if !ok {
		return errors.New("session not found")
	}
	return sess.conn.Close()
}

// SendToSession pushes a server-initiated deliver_sm directly to one
// targeted session — the operator simulating an inbound SMS arriving for a
// connected client, the same wire primitive handleSubmitSM's own
// rule-triggered reply already uses, just addressed at one chosen session
// instead of the client that happens to be replied to inline. extra's
// "sourceAddr"/"destAddr" override the PDU's source/destination, both
// defaulting to empty when not given.
func (e *Engine) SendToSession(mockID, sessionID, payload string, extra map[string]string) error {
	sess, ok := e.sessReg.Get(mockID, sessionID)
	if !ok {
		return errors.New("session not found")
	}
	return sess.writeDeliverSM(nextDeliverSequence(), extra["sourceAddr"], extra["destAddr"], payload) // bumps the outbound counter itself
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

func (e *Engine) recordHit(def *mock.Definition, destAddr, message, replyMessage string, matched bool) {
	if e.hitLogger == nil {
		return
	}
	respBody := ""
	if matched && replyMessage != "" {
		respBody = fmt.Sprintf("deliver_sm: %s", replyMessage)
	}
	err := e.hitLogger.Record(&hitlog.Entry{
		MockID:       def.ID,
		ProtocolType: "smpp",
		Direction:    hitlog.DirectionInbound,
		Path:         destAddr,
		RequestBody:  message,
		ResponseBody: respBody,
	})
	if err != nil {
		log.Printf("airmock: failed to record smpp hit for mock %q: %v", def.ID, err)
	}
}

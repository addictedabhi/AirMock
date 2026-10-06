package jmsengine

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
// to record each inbound message transfer.
type HitLogger interface {
	Record(e *hitlog.Entry) error
}

type Engine struct {
	mu            sync.Mutex
	listeners     map[string]net.Listener // keyed by mock ID
	sessReg       *sessreg.Registry[*connState]
	hitLogger     HitLogger
	dynamicValues mock.DynamicValueSource
}

func New() *Engine {
	return &Engine{listeners: map[string]net.Listener{}, sessReg: sessreg.NewRegistry[*connState]()}
}

func (e *Engine) Name() string { return "jms" }

// SetHitLogger wires up hit-log recording. Optional: an engine with no
// logger set still serves JMS mocks fine, it just has nothing to show in
// the Hit Log / dashboard for them.
func (e *Engine) SetHitLogger(l HitLogger) {
	e.hitLogger = l
}

// SetDynamicValues wires up counter()/csv() template-function support. See
// httpengine.Engine.SetDynamicValues's doc comment for the nil-safe behavior.
func (e *Engine) SetDynamicValues(dv mock.DynamicValueSource) {
	e.dynamicValues = dv
}

// Start is a no-op — like TCP/SMTP/MQTT/FTP/Kafka/SMPP/Diameter, each mock
// binds its own port from RegisterMock instead of one shared listener here.
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
	// Force-closes every already-attached connection for this mock. An AMQP
	// connection is meant to stay open indefinitely (a JMS consumer
	// typically holds one for its entire lifetime), so without this,
	// updating or unregistering a mock would leave any already-attached
	// client's connection running forever.
	e.sessReg.CloseAll(m.ID, func(cs *connState) { cs.conn.Close() })
	if !m.Enabled || m.JMS == nil {
		return nil
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", m.JMS.Port))
	if err != nil {
		return fmt.Errorf("jms mock %q: listen on port %d: %w", m.Name, m.JMS.Port, err)
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
	e.sessReg.CloseAll(id, func(cs *connState) { cs.conn.Close() })
	return nil
}

// ListSessions returns every currently-connected JMS session for one
// mock — the admin API's "Connected sessions" panel.
func (e *Engine) ListSessions(mockID string) []sessreg.Info {
	return e.sessReg.List(mockID)
}

// CloseSession forcibly disconnects one session by id.
func (e *Engine) CloseSession(mockID, sessionID string) error {
	cs, ok := e.sessReg.Get(mockID, sessionID)
	if !ok {
		return errors.New("session not found")
	}
	cs.conn.Close()
	return nil
}

// SendToSession delivers payload to one targeted session's own consumer
// link(s) attached to extra["address"] — reusing deliverToConsumers' own
// writeTransfer primitive, just aimed at one chosen connection instead of
// every connection whose consumer link matches (deliverToConsumers' normal,
// rule-triggered-reply behavior).
func (e *Engine) SendToSession(mockID, sessionID, payload string, extra map[string]string) error {
	cs, ok := e.sessReg.Get(mockID, sessionID)
	if !ok {
		return errors.New("session not found")
	}
	address := extra["address"]
	if address == "" {
		return errors.New("an address is required to deliver a message to a JMS session")
	}
	handles := cs.consumerHandlesFor(address)
	if len(handles) == 0 {
		return errors.New("no consumer link on this session is attached to that address with available credit")
	}
	for _, h := range handles {
		if err := cs.writeTransfer(h, payload); err != nil {
			return err
		}
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

// connsFor returns a snapshot of the currently-open connections for a mock
// — deliverToConsumers iterates and writes to each without holding any
// lock across those network writes.
func (e *Engine) connsFor(mockID string) []*connState {
	conns := e.sessReg.All(mockID)
	out := make([]*connState, 0, len(conns))
	for _, cs := range conns {
		out = append(out, cs)
	}
	return out
}

func (e *Engine) recordHit(def *mock.Definition, address, body, replyAddress, replyPayload string) {
	if e.hitLogger == nil {
		return
	}
	respBody := ""
	if replyAddress != "" {
		respBody = fmt.Sprintf("-> %s: %s", replyAddress, replyPayload)
	}
	err := e.hitLogger.Record(&hitlog.Entry{
		MockID:       def.ID,
		ProtocolType: "jms",
		Direction:    hitlog.DirectionInbound,
		Path:         address,
		RequestBody:  body,
		ResponseBody: respBody,
	})
	if err != nil {
		log.Printf("airmock: failed to record jms hit for mock %q: %v", def.ID, err)
	}
}

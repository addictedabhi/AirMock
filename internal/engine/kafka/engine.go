// Package kafkaengine implements a minimal single-node Kafka broker mock —
// enough of the real wire protocol (ApiVersions, Metadata, Produce, Fetch)
// for an actual Kafka client library to connect, produce, and consume
// against it. There's deliberately no consumer-group coordination
// (FindCoordinator/JoinGroup/SyncGroup/Heartbeat), transactions, or
// compression — a manual-offset consumer (no GroupID) covers the "test my
// app's own produce/consume code" use case this mock exists for, at a
// fraction of the real broker's protocol surface.
//
// The wire format itself (request/response framing, the RecordBatch v2
// encoding, per-API version negotiation) is handled by
// github.com/segmentio/kafka-go's own protocol subpackage rather than
// hand-rolled here — it's the same reflection-tag-driven codec that
// package's real client uses to talk to real brokers, so decoding a
// request/encoding a response is just picking the right typed Go struct,
// not re-implementing varints and CRC32C checksums from scratch.
package kafkaengine

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
// to record each inbound Produce.
type HitLogger interface {
	Record(e *hitlog.Entry) error
}

type Engine struct {
	mu            sync.Mutex
	listeners     map[string]net.Listener // keyed by mock ID
	stores        map[string]*topicStore  // keyed by mock ID
	sessReg       *sessreg.Registry[net.Conn]
	hitLogger     HitLogger
	dynamicValues mock.DynamicValueSource
}

func New() *Engine {
	return &Engine{
		listeners: map[string]net.Listener{},
		stores:    map[string]*topicStore{},
		sessReg:   sessreg.NewRegistry[net.Conn](),
	}
}

func (e *Engine) Name() string { return "kafka" }

// SetHitLogger wires up hit-log recording. Optional: an engine with no
// logger set still serves Kafka mocks fine, it just has nothing to show in
// the Hit Log / dashboard for them.
func (e *Engine) SetHitLogger(l HitLogger) {
	e.hitLogger = l
}

// SetDynamicValues wires up counter()/csv() template-function support. See
// httpengine.Engine.SetDynamicValues's doc comment for the nil-safe behavior.
func (e *Engine) SetDynamicValues(dv mock.DynamicValueSource) {
	e.dynamicValues = dv
}

// Start is a no-op — like TCP/SMTP/MQTT/FTP, each mock binds its own port
// from RegisterMock instead of one shared listener here.
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
	// Force-closes every already-connected session for this mock — without
	// this, updating or unregistering a mock only stopped its LISTENER; a
	// client already connected (a consumer that's been polling Fetch in a
	// loop, say) kept its handleConn goroutine and socket running
	// indefinitely, since Kafka connections are typically long-lived like
	// MQTT's, unlike TCP/FTP's short request-response sessions.
	e.sessReg.CloseAll(m.ID, func(c net.Conn) { c.Close() })
	delete(e.stores, m.ID)
	if !m.Enabled || m.Kafka == nil {
		return nil
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", m.Kafka.Port))
	if err != nil {
		return fmt.Errorf("kafka mock %q: listen on port %d: %w", m.Name, m.Kafka.Port, err)
	}
	e.listeners[m.ID] = ln
	e.stores[m.ID] = newTopicStore()
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
	e.sessReg.CloseAll(id, func(c net.Conn) { c.Close() })
	delete(e.stores, id)
	return nil
}

// ListSessions returns every currently-connected Kafka session for one
// mock — the admin API's "Connected sessions" panel. Kafka has no
// SendToSession: consumers pull via Fetch on their own polling cadence,
// and this mock deliberately implements no consumer-group/topic-
// subscription tracking a "send" could target anyway.
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

func (e *Engine) storeFor(mockID string) *topicStore {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stores[mockID]
}

func (e *Engine) recordHit(def *mock.Definition, topic string, payload []byte, replyTopic, replyPayload string) {
	if e.hitLogger == nil {
		return
	}
	respBody := ""
	if replyTopic != "" {
		respBody = fmt.Sprintf("PRODUCE %s: %s", replyTopic, replyPayload)
	}
	err := e.hitLogger.Record(&hitlog.Entry{
		MockID:       def.ID,
		ProtocolType: "kafka",
		Direction:    hitlog.DirectionInbound,
		Path:         topic,
		RequestBody:  string(payload),
		ResponseBody: respBody,
	})
	if err != nil {
		log.Printf("airmock: failed to record kafka hit for mock %q: %v", def.ID, err)
	}
}

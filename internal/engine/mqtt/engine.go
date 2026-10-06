package mqttengine

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
// to record each inbound PUBLISH.
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

func (e *Engine) Name() string { return "mqtt" }

// SetHitLogger wires up hit-log recording. Optional: an engine with no
// logger set still serves MQTT mocks fine, it just has nothing to show in
// the Hit Log / dashboard for them.
func (e *Engine) SetHitLogger(l HitLogger) {
	e.hitLogger = l
}

// SetDynamicValues wires up counter()/csv() template-function support. See
// httpengine.Engine.SetDynamicValues's doc comment for the nil-safe behavior.
func (e *Engine) SetDynamicValues(dv mock.DynamicValueSource) {
	e.dynamicValues = dv
}

// Start is a no-op — like TCP/SMTP, each mock binds its own port from
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

func (e *Engine) RegisterMock(m *mock.Definition) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if ln, ok := e.listeners[m.ID]; ok {
		ln.Close()
		delete(e.listeners, m.ID)
	}
	// Force-closes every currently-connected session's underlying
	// connection for this mock. Without this, updating or unregistering a
	// mock only stopped its LISTENER; any client already connected kept
	// its handleConn goroutine and socket running indefinitely, since MQTT
	// connections are typically long-lived (unlike TCP/FTP's short
	// request-response sessions) and handleConn's read loop has no way to
	// learn its mock was changed out from under it. Closing the connection
	// here makes its blocked read return an error, which handleConn
	// already treats as "client disconnected."
	e.sessReg.CloseAll(m.ID, func(s *session) { s.conn.Close() })
	if !m.Enabled || m.MQTT == nil {
		return nil
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", m.MQTT.Port))
	if err != nil {
		return fmt.Errorf("mqtt mock %q: listen on port %d: %w", m.Name, m.MQTT.Port, err)
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

// ListSessions returns every currently-connected MQTT session for one
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
	sess.conn.Close()
	return nil
}

// SendToSession publishes payload on topic directly to one targeted
// session, bypassing the subscription-filter check broadcastToSubscribers
// applies — this is an operator explicitly pushing a message to a chosen
// client, not a real client's own PUBLISH being fanned out to whoever
// happens to be subscribed.
func (e *Engine) SendToSession(mockID, sessionID, payload string, extra map[string]string) error {
	sess, ok := e.sessReg.Get(mockID, sessionID)
	if !ok {
		return errors.New("session not found")
	}
	topic := extra["topic"]
	if topic == "" {
		return errors.New("a topic is required to publish to an MQTT session")
	}
	sess.writeRaw(encodePublish(topic, []byte(payload))) // bumps the outbound counter itself
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

// broadcastToSubscribers publishes (topic, payload) to every session on
// mockID whose subscriptions include a filter matching topic — including a
// session that both published the trigger message and subscribed to the
// reply topic itself.
func (e *Engine) broadcastToSubscribers(mockID, topic string, payload []byte) {
	conns := e.sessReg.All(mockID)

	frame := encodePublish(topic, payload)
	for _, s := range conns {
		if s.subscribedTo(topic) {
			s.writeRaw(frame) // bumps the outbound counter itself
		}
	}
}

func (e *Engine) recordHit(def *mock.Definition, topic string, payload []byte, replyTopic, replyPayload string) {
	if e.hitLogger == nil {
		return
	}
	respBody := ""
	if replyTopic != "" {
		respBody = fmt.Sprintf("PUBLISH %s: %s", replyTopic, replyPayload)
	}
	err := e.hitLogger.Record(&hitlog.Entry{
		MockID:       def.ID,
		ProtocolType: "mqtt",
		Direction:    hitlog.DirectionInbound,
		Path:         topic,
		RequestBody:  string(payload),
		ResponseBody: respBody,
	})
	if err != nil {
		log.Printf("airmock: failed to record mqtt hit for mock %q: %v", def.ID, err)
	}
}

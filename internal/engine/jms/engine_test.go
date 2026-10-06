package jmsengine

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/Azure/go-amqp"

	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/session"
)

type fakeHitLogger struct {
	mu      sync.Mutex
	entries []*hitlog.Entry
}

func (f *fakeHitLogger) Record(e *hitlog.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeHitLogger) all() []*hitlog.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*hitlog.Entry, len(f.entries))
	copy(out, f.entries)
	return out
}

func registerOnFreePort(t *testing.T, e *Engine, m *mock.Definition) string {
	t.Helper()
	m.JMS.Port = 0
	if err := e.RegisterMock(m); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	ln, ok := e.listeners[m.ID]
	if !ok {
		t.Fatal("expected a listener to be registered")
	}
	return ln.Addr().(*net.TCPAddr).String()
}

func newDef(name string, cfg *mock.JMSConfig) *mock.Definition {
	return &mock.Definition{ID: "jms-" + name, Name: name, ProtocolType: "jms", Enabled: true, JMS: cfg}
}

// dialClient connects a real github.com/Azure/go-amqp client against addr
// — the exact "real client library completes a basic exchange" bar the
// mock is meant to clear, exercising the actual AMQP 1.0 protocol-header/
// open/begin handshake this engine implements rather than a hand-rolled
// fake client. No SASL/credentials are configured, matching this mock's
// deliberately auth-free scope.
func dialClient(t *testing.T, addr string) *amqp.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := amqp.Dial(ctx, "amqp://"+addr, nil)
	if err != nil {
		t.Fatalf("Dial (protocol header + open/begin handshake): %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func newSession(t *testing.T, conn *amqp.Conn) *amqp.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := conn.NewSession(ctx, nil)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

func TestJMSMockHandshakeAndSendReceive(t *testing.T) {
	e := New()
	def := newDef("send-receive", &mock.JMSConfig{
		Rules: []mock.JMSRule{
			{AddressPattern: "orders", ReplyAddress: "receipts", ReplyPayload: "ok:{{.Request.Body}}"},
		},
	})
	addr := registerOnFreePort(t, e, def)

	conn := dialClient(t, addr)
	sess := newSession(t, conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	receiver, err := sess.NewReceiver(ctx, "receipts", nil)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	sender, err := sess.NewSender(ctx, "orders", nil)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	if err := sender.Send(ctx, amqp.NewMessage([]byte("hello")), nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	recvCtx, recvCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer recvCancel()
	msg, err := receiver.Receive(recvCtx, nil)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if got := string(msg.GetData()); got != "ok:hello" {
		t.Fatalf("expected reply %q, got %q", "ok:hello", got)
	}
}

func TestJMSMockPayloadMatchGatesReply(t *testing.T) {
	e := New()
	def := newDef("payload-match", &mock.JMSConfig{
		Rules: []mock.JMSRule{
			{AddressPattern: "commands", PayloadMatch: "reboot", MatchType: "exact", ReplyAddress: "acks", ReplyPayload: "rebooting"},
		},
	})
	addr := registerOnFreePort(t, e, def)

	conn := dialClient(t, addr)
	sess := newSession(t, conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	receiver, err := sess.NewReceiver(ctx, "acks", nil)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	sender, err := sess.NewSender(ctx, "commands", nil)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	if err := sender.Send(ctx, amqp.NewMessage([]byte("status")), nil); err != nil {
		t.Fatalf("Send (non-matching): %v", err)
	}
	if err := sender.Send(ctx, amqp.NewMessage([]byte("reboot")), nil); err != nil {
		t.Fatalf("Send (matching): %v", err)
	}

	recvCtx, recvCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer recvCancel()
	msg, err := receiver.Receive(recvCtx, nil)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if got := string(msg.GetData()); got != "rebooting" {
		t.Fatalf("expected reply %q, got %q", "rebooting", got)
	}
}

func TestJMSMockRecordsHits(t *testing.T) {
	e := New()
	logger := &fakeHitLogger{}
	e.SetHitLogger(logger)
	def := newDef("hit-logging", &mock.JMSConfig{
		Rules: []mock.JMSRule{{AddressPattern: "orders", ReplyAddress: "receipts", ReplyPayload: "ok"}},
	})
	addr := registerOnFreePort(t, e, def)

	conn := dialClient(t, addr)
	sess := newSession(t, conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sender, err := sess.NewSender(ctx, "orders", nil)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	if err := sender.Send(ctx, amqp.NewMessage([]byte("hello")), nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(logger.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	entries := logger.all()
	if len(entries) != 1 {
		t.Fatalf("expected 1 hit-log entry, got %d", len(entries))
	}
	if entries[0].Path != "orders" || entries[0].RequestBody != "hello" || entries[0].ResponseBody != "-> receipts: ok" {
		t.Fatalf("unexpected entry: %+v", entries[0])
	}
}

func TestJMSMockFaultSuppressesReply(t *testing.T) {
	e := New()
	def := newDef("fault-suppress", &mock.JMSConfig{
		Rules: []mock.JMSRule{{AddressPattern: "orders", ReplyAddress: "receipts", ReplyPayload: "ok"}},
	})
	def.Fault = &mock.FaultConfig{ErrorRatePercent: 100, ErrorStatusCodes: []int{500}}
	addr := registerOnFreePort(t, e, def)

	conn := dialClient(t, addr)
	sess := newSession(t, conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	receiver, err := sess.NewReceiver(ctx, "receipts", nil)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	sender, err := sess.NewSender(ctx, "orders", nil)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	if err := sender.Send(ctx, amqp.NewMessage([]byte("hello")), nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	recvCtx, recvCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer recvCancel()
	if msg, err := receiver.Receive(recvCtx, nil); err == nil {
		t.Fatalf("expected no reply under a 100%% fault, got one: %+v", msg)
	}
}

func TestReRegisteringAJMSMockClosesAlreadyConnectedConns(t *testing.T) {
	e := New()
	def := newDef("re-register", &mock.JMSConfig{})
	addr := registerOnFreePort(t, e, def)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write(protocolHeader[:]); err != nil {
		t.Fatalf("write protocol header: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := readProtocolHeader(conn); err != nil {
		t.Fatalf("read protocol header response: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	registerOnFreePort(t, e, def) // re-register the same mock ID

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected the old connection to be closed after re-registering the mock")
	}
}

func TestUnregisteringAJMSMockClosesAlreadyConnectedConns(t *testing.T) {
	e := New()
	def := newDef("unregister", &mock.JMSConfig{})
	addr := registerOnFreePort(t, e, def)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write(protocolHeader[:]); err != nil {
		t.Fatalf("write protocol header: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := readProtocolHeader(conn); err != nil {
		t.Fatalf("read protocol header response: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	if err := e.UnregisterMock(def.ID); err != nil {
		t.Fatalf("UnregisterMock: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected the connection to be closed after unregistering the mock")
	}
}

func TestUnregisterJMSMockClosesListener(t *testing.T) {
	e := New()
	def := newDef("unregister-listener", &mock.JMSConfig{})
	addr := registerOnFreePort(t, e, def)

	if err := e.UnregisterMock(def.ID); err != nil {
		t.Fatalf("UnregisterMock: %v", err)
	}
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Fatal("expected dial to fail after unregistering the mock")
	}
}

func TestRegisterJMSMockDisabledDoesNotListen(t *testing.T) {
	e := New()
	def := newDef("disabled", &mock.JMSConfig{Port: 0})
	def.Enabled = false
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	if _, ok := e.listeners[def.ID]; ok {
		t.Fatal("expected no listener for a disabled mock")
	}
}

// TestJMSSessionsListSendAndClose covers the admin "Connected sessions" API
// surface: an attached consumer shows up in ListSessions with its link
// captured into Meta, SendToSession delivers a message that consumer
// actually receives (distinct from a rule-triggered reply), and
// CloseSession forcibly drops the connection.
func TestJMSSessionsListSendAndClose(t *testing.T) {
	e := New()
	def := newDef("sessions", &mock.JMSConfig{})
	addr := registerOnFreePort(t, e, def)

	conn := dialClient(t, addr)
	sess := newSession(t, conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	receiver, err := sess.NewReceiver(ctx, "alerts", nil)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	var sessions []session.Info
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sessions = e.ListSessions(def.ID)
		if len(sessions) == 1 && sessions[0].Meta["links"] != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 connected session, got %d", len(sessions))
	}
	if sessions[0].Protocol != "jms" {
		t.Fatalf("expected protocol %q, got %q", "jms", sessions[0].Protocol)
	}
	if !strings.Contains(sessions[0].Meta["links"], "alerts") {
		t.Fatalf("expected the attached link's address captured in Meta, got %+v", sessions[0].Meta)
	}

	// The receiver's initial credit grant arrives via its own flow frame
	// shortly after attach — retry until that's landed, rather than racing
	// a single SendToSession call against it.
	deadline = time.Now().Add(2 * time.Second)
	var sendErr error
	for time.Now().Before(deadline) {
		sendErr = e.SendToSession(def.ID, sessions[0].ID, "server-pushed alert", map[string]string{"address": "alerts"})
		if sendErr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sendErr != nil {
		t.Fatalf("SendToSession: %v", sendErr)
	}
	recvCtx, recvCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer recvCancel()
	msg, err := receiver.Receive(recvCtx, nil)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if got := string(msg.GetData()); got != "server-pushed alert" {
		t.Fatalf("expected pushed message %q, got %q", "server-pushed alert", got)
	}

	if err := e.SendToSession(def.ID, sessions[0].ID, "x", map[string]string{"address": "no-such-address"}); err == nil {
		t.Fatal("expected an error sending to an address with no attached consumer link")
	}

	if err := e.CloseSession(def.ID, sessions[0].ID); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	if err := e.CloseSession(def.ID, "no-such-session"); err == nil {
		t.Fatal("expected an error closing a session id that doesn't exist")
	}
}

package smppengine

import (
	"net"
	"sync"
	"testing"
	"time"

	gosmpp "github.com/fiorix/go-smpp/smpp"
	"github.com/fiorix/go-smpp/smpp/pdu"
	"github.com/fiorix/go-smpp/smpp/pdu/pdufield"
	"github.com/fiorix/go-smpp/smpp/pdu/pdutext"

	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
	sessreg "github.com/addictedabhi/airmock/internal/session"
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
	m.SMPP.Port = 0
	if err := e.RegisterMock(m); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	ln, ok := e.listeners[m.ID]
	if !ok {
		t.Fatal("expected a listener to be registered")
	}
	return ln.Addr().String()
}

func newDef(name string, cfg *mock.SMPPConfig) *mock.Definition {
	return &mock.Definition{ID: "smpp-" + name, Name: name, ProtocolType: "smpp", Enabled: true, SMPP: cfg}
}

// bindTransceiver binds a real github.com/fiorix/go-smpp client against
// addr — the exact "real client library completes a basic exchange" bar
// the mock is meant to clear, exercising the actual bind_transceiver +
// submit_sm/deliver_sm wire protocol rather than a hand-rolled fake client.
func bindTransceiver(t *testing.T, addr, user, passwd string, handler gosmpp.HandlerFunc) *gosmpp.Transceiver {
	t.Helper()
	tx := &gosmpp.Transceiver{
		Addr:        addr,
		User:        user,
		Passwd:      passwd,
		EnquireLink: 0, // disable the periodic enquire_link goroutine; tests don't need it and it'd outlive the test
		RespTimeout: 3 * time.Second,
		Handler:     handler,
	}
	t.Cleanup(func() { tx.Close() })
	status := <-tx.Bind()
	if status.Status() != gosmpp.Connected {
		t.Fatalf("expected Connected, got %s (err: %v)", status.Status(), status.Error())
	}
	return tx
}

func TestSMPPMockBindAndSubmit(t *testing.T) {
	e := New()
	def := newDef("bind-submit", &mock.SMPPConfig{})
	addr := registerOnFreePort(t, e, def)

	tx := bindTransceiver(t, addr, "any", "any", nil)
	sm, err := tx.Submit(&gosmpp.ShortMessage{Src: "1000", Dst: "2000", Text: pdutext.Raw("hello")})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if sm.RespID() == "" {
		t.Fatal("expected a non-empty message_id in submit_sm_resp")
	}
}

func TestSMPPMockRejectsWrongCredentials(t *testing.T) {
	e := New()
	def := newDef("auth", &mock.SMPPConfig{SystemID: "correct-id", Password: "correct-pw"})
	addr := registerOnFreePort(t, e, def)

	tx := &gosmpp.Transceiver{Addr: addr, User: "wrong-id", Passwd: "wrong-pw", RespTimeout: 3 * time.Second}
	defer tx.Close()
	status := <-tx.Bind()
	if status.Status() == gosmpp.Connected {
		t.Fatal("expected bind to fail with wrong credentials")
	}
}

func TestSMPPMockAcceptsCorrectCredentials(t *testing.T) {
	e := New()
	def := newDef("auth-ok", &mock.SMPPConfig{SystemID: "correct-id", Password: "correct-pw"})
	addr := registerOnFreePort(t, e, def)
	bindTransceiver(t, addr, "correct-id", "correct-pw", nil)
}

func TestSMPPMockRuleSendsDeliverSMReply(t *testing.T) {
	e := New()
	def := newDef("rule-reply", &mock.SMPPConfig{
		Rules: []mock.SMPPRule{
			{DestAddrPattern: "2000", MessageMatch: "BALANCE", MatchType: "contains", ReplyMessage: "Your balance is $42"},
		},
	})
	addr := registerOnFreePort(t, e, def)

	received := make(chan pdu.Body, 1)
	tx := bindTransceiver(t, addr, "any", "any", func(p pdu.Body) {
		if p.Header().ID == pdu.DeliverSMID {
			received <- p
		}
	})

	if _, err := tx.Submit(&gosmpp.ShortMessage{Src: "1000", Dst: "2000", Text: pdutext.Raw("BALANCE?")}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	select {
	case p := <-received:
		got := p.Fields()[pdufield.ShortMessage].String()
		if got != "Your balance is $42" {
			t.Fatalf("expected deliver_sm short_message %q, got %q", "Your balance is $42", got)
		}
		if src := p.Fields()[pdufield.SourceAddr].String(); src != "2000" {
			t.Fatalf("expected deliver_sm source_addr to default to the original destination_addr %q, got %q", "2000", src)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for deliver_sm")
	}
}

func TestSMPPMockMessageMatchGatesReply(t *testing.T) {
	e := New()
	def := newDef("message-match", &mock.SMPPConfig{
		Rules: []mock.SMPPRule{
			{MessageMatch: "STOP", MatchType: "exact", ReplyMessage: "unsubscribed"},
		},
	})
	addr := registerOnFreePort(t, e, def)

	received := make(chan pdu.Body, 2)
	tx := bindTransceiver(t, addr, "any", "any", func(p pdu.Body) {
		if p.Header().ID == pdu.DeliverSMID {
			received <- p
		}
	})

	if _, err := tx.Submit(&gosmpp.ShortMessage{Src: "1000", Dst: "2000", Text: pdutext.Raw("hello")}); err != nil {
		t.Fatalf("Submit (non-matching): %v", err)
	}
	if _, err := tx.Submit(&gosmpp.ShortMessage{Src: "1000", Dst: "2000", Text: pdutext.Raw("STOP")}); err != nil {
		t.Fatalf("Submit (matching): %v", err)
	}

	select {
	case p := <-received:
		if got := p.Fields()[pdufield.ShortMessage].String(); got != "unsubscribed" {
			t.Fatalf("expected reply %q, got %q", "unsubscribed", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the one expected deliver_sm")
	}
	select {
	case p := <-received:
		t.Fatalf("expected only ONE deliver_sm (from the matching STOP submit), got a second: %+v", p.Fields())
	case <-time.After(200 * time.Millisecond):
		// correct: the non-matching "hello" submit must not have triggered a reply
	}
}

func TestSMPPMockRecordsHits(t *testing.T) {
	e := New()
	logger := &fakeHitLogger{}
	e.SetHitLogger(logger)
	def := newDef("hit-logging", &mock.SMPPConfig{
		Rules: []mock.SMPPRule{{MessageMatch: "hi", ReplyMessage: "hello back"}},
	})
	addr := registerOnFreePort(t, e, def)

	tx := bindTransceiver(t, addr, "any", "any", nil)
	if _, err := tx.Submit(&gosmpp.ShortMessage{Src: "1000", Dst: "2000", Text: pdutext.Raw("hi there")}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(logger.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	entries := logger.all()
	if len(entries) != 1 {
		t.Fatalf("expected 1 hit-log entry, got %d", len(entries))
	}
	if entries[0].Path != "2000" || entries[0].RequestBody != "hi there" || entries[0].ResponseBody != "deliver_sm: hello back" {
		t.Fatalf("unexpected entry: %+v", entries[0])
	}
}

func TestSMPPMockFaultSuppressesReply(t *testing.T) {
	e := New()
	def := newDef("fault-suppress", &mock.SMPPConfig{
		Rules: []mock.SMPPRule{{MessageMatch: "hi", ReplyMessage: "hello back"}},
	})
	def.Fault = &mock.FaultConfig{ErrorRatePercent: 100, ErrorStatusCodes: []int{500}}
	addr := registerOnFreePort(t, e, def)

	received := make(chan pdu.Body, 1)
	tx := bindTransceiver(t, addr, "any", "any", func(p pdu.Body) {
		if p.Header().ID == pdu.DeliverSMID {
			received <- p
		}
	})
	if _, err := tx.Submit(&gosmpp.ShortMessage{Src: "1000", Dst: "2000", Text: pdutext.Raw("hi there")}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	select {
	case p := <-received:
		t.Fatalf("expected no deliver_sm under a 100%% fault, got one: %+v", p.Fields())
	case <-time.After(300 * time.Millisecond):
		// correct: fault suppressed the reply
	}
}

func TestReRegisteringAnSMPPMockClosesAlreadyConnectedConns(t *testing.T) {
	e := New()
	def := newDef("re-register", &mock.SMPPConfig{})
	addr := registerOnFreePort(t, e, def)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	registerOnFreePort(t, e, def)

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected the old connection to be closed after re-registering the mock")
	}
}

func TestUnregisteringAnSMPPMockClosesAlreadyConnectedConns(t *testing.T) {
	e := New()
	def := newDef("unregister", &mock.SMPPConfig{})
	addr := registerOnFreePort(t, e, def)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	if err := e.UnregisterMock(def.ID); err != nil {
		t.Fatalf("UnregisterMock: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected the connection to be closed after unregistering the mock")
	}
}

func TestUnregisterSMPPMockClosesListener(t *testing.T) {
	e := New()
	def := newDef("unregister-listener", &mock.SMPPConfig{})
	addr := registerOnFreePort(t, e, def)

	if err := e.UnregisterMock(def.ID); err != nil {
		t.Fatalf("UnregisterMock: %v", err)
	}
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Fatal("expected dial to fail after unregistering the mock")
	}
}

func TestRegisterSMPPMockDisabledDoesNotListen(t *testing.T) {
	e := New()
	def := newDef("disabled", &mock.SMPPConfig{Port: 0})
	def.Enabled = false
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	if _, ok := e.listeners[def.ID]; ok {
		t.Fatal("expected no listener for a disabled mock")
	}
}

// TestSMPPSessionsListSendAndClose covers the admin "Connected sessions" API
// surface: a bound client shows up in ListSessions with its bind system_id
// captured into Meta, SendToSession pushes an unsolicited deliver_sm the
// client actually receives (distinct from a rule-triggered reply), and
// CloseSession forcibly drops the connection.
func TestSMPPSessionsListSendAndClose(t *testing.T) {
	e := New()
	def := newDef("sessions", &mock.SMPPConfig{})
	addr := registerOnFreePort(t, e, def)

	received := make(chan pdu.Body, 1)
	bindTransceiver(t, addr, "test-client", "any", func(p pdu.Body) {
		if p.Header().ID == pdu.DeliverSMID {
			received <- p
		}
	})

	var sessions []sessreg.Info
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sessions = e.ListSessions(def.ID)
		if len(sessions) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 connected session, got %d", len(sessions))
	}
	if sessions[0].Protocol != "smpp" {
		t.Fatalf("expected protocol %q, got %q", "smpp", sessions[0].Protocol)
	}
	if sessions[0].Meta["systemId"] != "test-client" {
		t.Fatalf("expected bind system_id captured in Meta, got %+v", sessions[0].Meta)
	}

	if err := e.SendToSession(def.ID, sessions[0].ID, "pushed inbound sms", map[string]string{"sourceAddr": "9999", "destAddr": "1000"}); err != nil {
		t.Fatalf("SendToSession: %v", err)
	}
	select {
	case p := <-received:
		if got := p.Fields()[pdufield.ShortMessage].String(); got != "pushed inbound sms" {
			t.Fatalf("expected pushed message %q, got %q", "pushed inbound sms", got)
		}
		if src := p.Fields()[pdufield.SourceAddr].String(); src != "9999" {
			t.Fatalf("expected source_addr %q, got %q", "9999", src)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the operator-pushed deliver_sm")
	}

	if err := e.CloseSession(def.ID, sessions[0].ID); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	if err := e.CloseSession(def.ID, "no-such-session"); err == nil {
		t.Fatal("expected an error closing a session id that doesn't exist")
	}
}

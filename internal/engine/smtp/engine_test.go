package smtpengine

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/session"
	smtpclient "github.com/addictedabhi/airmock/internal/smtp"
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

// registerOnFreePort registers m (with Port left at 0, letting the OS pick a
// free port) and returns the actual bound address — port 0 keeps the test
// independent of any specific port being free on the machine running it.
func registerOnFreePort(t *testing.T, e *Engine, m *mock.Definition) string {
	t.Helper()
	m.SMTP.Port = 0
	if err := e.RegisterMock(m); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	ln, ok := e.listeners[m.ID]
	if !ok {
		t.Fatal("expected a listener to be registered")
	}
	return ln.Addr().String()
}

// dialAndSend speaks just enough raw SMTP to send one message, returning
// every response line the mock sent back (so a test can assert on the
// DATA-completion response specifically).
func dialAndSend(t *testing.T, addr, from, to, subject, body string) []string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)

	var responses []string
	read := func() string {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		responses = append(responses, strings.TrimRight(line, "\r\n"))
		return line
	}

	read() // 0: banner
	fmt.Fprintf(conn, "EHLO test\r\n")
	read() // 1: EHLO ack
	fmt.Fprintf(conn, "MAIL FROM:<%s>\r\n", from)
	read() // 2: MAIL FROM ack
	fmt.Fprintf(conn, "RCPT TO:<%s>\r\n", to)
	read() // 3: RCPT TO ack
	fmt.Fprintf(conn, "DATA\r\n")
	read() // 4: "354 Start mail input..."
	fmt.Fprintf(conn, "Subject: %s\r\n\r\n%s\r\n.\r\n", subject, body)
	read() // 5: the accept/reject response for the message
	fmt.Fprintf(conn, "QUIT\r\n")
	read() // 6: "221 Bye"

	return responses
}

func TestSMTPMockAcceptsByDefault(t *testing.T) {
	logger := &fakeHitLogger{}
	e := New()
	e.SetHitLogger(logger)
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "m1", Name: "catch-all", Enabled: true, ProtocolType: "smtp",
		SMTP: &mock.SMTPConfig{DefaultAccept: true},
	}
	addr := registerOnFreePort(t, e, m)

	responses := dialAndSend(t, addr, "alice@example.com", "bob@example.com", "Hello", "Just testing.")
	last := responses[len(responses)-1]
	if !strings.HasPrefix(last, "221") {
		t.Fatalf("expected QUIT to be acked with 221, got %q (all: %v)", last, responses)
	}
	dataResp := responses[5]
	if !strings.HasPrefix(dataResp, "250") {
		t.Fatalf("expected the message to be accepted with 250, got %q", dataResp)
	}

	entries := logger.all()
	if len(entries) != 1 {
		t.Fatalf("expected exactly one hit logged, got %d", len(entries))
	}
	e0 := entries[0]
	if e0.ProtocolType != "smtp" || e0.ResponseStatus != 250 {
		t.Fatalf("unexpected hit entry: %+v", e0)
	}
	if !strings.Contains(e0.RequestBody, "Envelope-From: alice@example.com") || !strings.Contains(e0.RequestBody, "Subject: Hello") {
		t.Fatalf("expected the captured envelope address and subject in the hit body, got %q", e0.RequestBody)
	}
}

func TestSMTPMockRejectsByRule(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "m2", Name: "reject-spam", Enabled: true, ProtocolType: "smtp",
		SMTP: &mock.SMTPConfig{
			DefaultAccept: true,
			Rules: []mock.SMTPRule{
				{MatchField: "subject", Match: "spam", MatchType: "contains", Accept: false, ResponseCode: 550, ResponseMessage: "spam rejected"},
			},
		},
	}
	addr := registerOnFreePort(t, e, m)

	responses := dialAndSend(t, addr, "spammer@example.com", "victim@example.com", "buy spam now", "...")
	dataResp := responses[5]
	if dataResp != "550 spam rejected" {
		t.Fatalf("expected the custom rejection response, got %q", dataResp)
	}
}

func TestSMTPMockDefaultRejectWhenNoRuleMatches(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "m3", Name: "allowlist-only", Enabled: true, ProtocolType: "smtp",
		SMTP: &mock.SMTPConfig{
			DefaultAccept: false,
			Rules: []mock.SMTPRule{
				{MatchField: "to", Match: "allowed@example.com", MatchType: "exact", Accept: true},
			},
		},
	}
	addr := registerOnFreePort(t, e, m)

	responses := dialAndSend(t, addr, "someone@example.com", "not-allowed@example.com", "hi", "body")
	dataResp := responses[5]
	if !strings.HasPrefix(dataResp, "550") {
		t.Fatalf("expected the default reject for an unmatched recipient, got %q", dataResp)
	}

	responses2 := dialAndSend(t, addr, "someone@example.com", "allowed@example.com", "hi", "body")
	dataResp2 := responses2[5]
	if !strings.HasPrefix(dataResp2, "250") {
		t.Fatalf("expected the allowlisted recipient to be accepted, got %q", dataResp2)
	}
}

// TestSMTPMockInteropWithInternalSMTPClient proves this mock listener
// actually speaks compatible SMTP: internal/smtp.Send (the same client
// AirMock's own async "email" callback channel uses to send real mail) can
// successfully deliver a message to it, not just a hand-rolled test dialog.
func TestSMTPMockInteropWithInternalSMTPClient(t *testing.T) {
	logger := &fakeHitLogger{}
	e := New()
	e.SetHitLogger(logger)
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "m4", Name: "interop", Enabled: true, ProtocolType: "smtp",
		SMTP: &mock.SMTPConfig{DefaultAccept: true},
	}
	addr := registerOnFreePort(t, e, m)
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("parse port: %v", err)
	}

	// The listener binds the wildcard address (e.g. "[::]:PORT"); connect to
	// loopback explicitly rather than reusing that host string, since a bare
	// "::" isn't a dialable "host:port" without brackets.
	cfg := smtpclient.Settings{Host: "127.0.0.1", Port: port, FromAddress: "sender@example.com", UseTLS: false}
	if err := smtpclient.Send(cfg, "recipient@example.com", "Interop test", "<p>hello</p>"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	entries := logger.all()
	if len(entries) != 1 {
		t.Fatalf("expected exactly one hit logged, got %d", len(entries))
	}
	if !strings.Contains(entries[0].RequestBody, "Subject: Interop test") {
		t.Fatalf("expected the subject captured, got %q", entries[0].RequestBody)
	}
}

func TestUnregisterMockClosesListener(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{ID: "m5", Name: "temp", Enabled: true, ProtocolType: "smtp", SMTP: &mock.SMTPConfig{DefaultAccept: true}}
	registerOnFreePort(t, e, m)

	if err := e.UnregisterMock(m.ID); err != nil {
		t.Fatalf("UnregisterMock: %v", err)
	}
	if _, ok := e.listeners[m.ID]; ok {
		t.Fatal("expected the listener to be removed")
	}
}

func TestRegisterMockDisabledDoesNotListen(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{ID: "m6", Name: "disabled", Enabled: false, ProtocolType: "smtp", SMTP: &mock.SMTPConfig{Port: 0, DefaultAccept: true}}
	if err := e.RegisterMock(m); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	if _, ok := e.listeners[m.ID]; ok {
		t.Fatal("expected no listener for a disabled mock")
	}
}

// TestSMTPMockFaultLatencyJitter guards against a real gap: def.Fault was
// only ever read by the HTTP-family matchers — an SMTP mock's FaultConfig
// was silently never applied, so LatencyJitterMs did nothing.
func TestSMTPMockFaultLatencyJitter(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "fault-latency", Name: "fault-latency", Enabled: true, ProtocolType: "smtp",
		Fault: &mock.FaultConfig{LatencyJitterMs: 200},
		SMTP:  &mock.SMTPConfig{DefaultAccept: true},
	}
	addr := registerOnFreePort(t, e, m)

	// LatencyJitterMs is uniformly random in [0, 200ms] per hit, so assert
	// on the max across several trials rather than one sample.
	var maxElapsed time.Duration
	for i := 0; i < 8; i++ {
		start := time.Now()
		dialAndSend(t, addr, "alice@example.com", "bob@example.com", "Hi", "test")
		if elapsed := time.Since(start); elapsed > maxElapsed {
			maxElapsed = elapsed
		}
	}
	if maxElapsed < 50*time.Millisecond {
		t.Fatalf("expected at least one of 8 trials to show noticeable latency jitter (up to 200ms), max observed was %s", maxElapsed)
	}
}

// TestSMTPMockFaultTimeoutDropsConnection guards against the same gap for
// the "drop the connection" side — TimeoutRatePercent:100 should mean the
// message-accept reply never arrives, not silently ignored.
func TestSMTPMockFaultTimeoutDropsConnection(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "fault-timeout", Name: "fault-timeout", Enabled: true, ProtocolType: "smtp",
		Fault: &mock.FaultConfig{TimeoutRatePercent: 100},
		SMTP:  &mock.SMTPConfig{DefaultAccept: true},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)

	read := func() string {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return line
	}
	read() // banner
	fmt.Fprintf(conn, "EHLO test\r\n")
	read()
	fmt.Fprintf(conn, "MAIL FROM:<alice@example.com>\r\n")
	read()
	fmt.Fprintf(conn, "RCPT TO:<bob@example.com>\r\n")
	read()
	fmt.Fprintf(conn, "DATA\r\n")
	read() // "354 Start mail input..."
	fmt.Fprintf(conn, "Subject: Hi\r\n\r\ntest\r\n.\r\n")

	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err == nil {
		t.Fatalf("expected the connection to be dropped with no accept/reject reply, got %d bytes: %q", n, buf[:n])
	}
}

// TestSMTPSessionsListAndClose covers the admin "Connected sessions" API
// surface: a connected client shows up in ListSessions with growing message
// counters as it talks, and CloseSession forcibly drops the connection.
// SMTP has no SendToSession (see ListSessions' own doc comment).
func TestSMTPSessionsListAndClose(t *testing.T) {
	e := New()
	m := &mock.Definition{
		ID: "smtp-sessions", ProtocolType: "smtp", Enabled: true,
		SMTP: &mock.SMTPConfig{DefaultAccept: true},
	}
	addr := registerOnFreePort(t, e, m)
	defer e.UnregisterMock(m.ID)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read banner: %v", err)
	}

	var sessions []session.Info
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sessions = e.ListSessions(m.ID)
		if len(sessions) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 connected session, got %d", len(sessions))
	}
	if sessions[0].Protocol != "smtp" {
		t.Fatalf("expected protocol %q, got %q", "smtp", sessions[0].Protocol)
	}
	if sessions[0].MessagesOut == 0 {
		t.Fatal("expected the banner write to have bumped MessagesOut")
	}

	fmt.Fprintf(conn, "EHLO test\r\n")
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read EHLO ack: %v", err)
	}
	sessions = e.ListSessions(m.ID)
	if sessions[0].MessagesIn == 0 {
		t.Fatal("expected the EHLO command to have bumped MessagesIn")
	}

	if err := e.CloseSession(m.ID, sessions[0].ID); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := reader.ReadString('\n'); err == nil {
		t.Fatal("expected the client's next read to error after CloseSession")
	}

	if err := e.CloseSession(m.ID, "no-such-session"); err == nil {
		t.Fatal("expected an error closing a session id that doesn't exist")
	}
}

// rawConv is a tiny line-based SMTP conversation helper for tests that need
// to see the reply to each individual command.
type rawConv struct {
	t      *testing.T
	conn   net.Conn
	reader *bufio.Reader
}

func newRawConv(t *testing.T, addr string) *rawConv {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	c := &rawConv{t: t, conn: conn, reader: bufio.NewReader(conn)}
	c.reply() // banner
	return c
}

func (c *rawConv) reply() string {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := c.reader.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return strings.TrimRight(line, "\r\n")
}

func (c *rawConv) cmd(format string, args ...any) string {
	c.t.Helper()
	fmt.Fprintf(c.conn, format+"\r\n", args...)
	return c.reply()
}

func TestSMTPMockRefusesABadRecipientAtRCPTTime(t *testing.T) {
	e := New()
	m := &mock.Definition{
		ID: "m-rcpt", Name: "rcpt-stage", Enabled: true, ProtocolType: "smtp",
		SMTP: &mock.SMTPConfig{DefaultAccept: true, Rules: []mock.SMTPRule{
			{MatchField: "to", Match: "@blocked.test", Accept: false, ResponseCode: 550, ResponseMessage: "no such user"},
		}},
	}
	addr := registerOnFreePort(t, e, m)
	c := newRawConv(t, addr)

	c.cmd("EHLO test")
	if got := c.cmd("MAIL FROM:<a@ok.test>"); !strings.HasPrefix(got, "250") {
		t.Fatalf("MAIL FROM: %q", got)
	}
	if got := c.cmd("RCPT TO:<bob@blocked.test>"); got != "550 no such user" {
		t.Fatalf("expected the recipient refused at RCPT TO, got %q", got)
	}
	// A good recipient on the same envelope still goes through, and the
	// message is delivered to just that recipient.
	if got := c.cmd("RCPT TO:<carol@ok.test>"); !strings.HasPrefix(got, "250") {
		t.Fatalf("second RCPT TO: %q", got)
	}
	if got := c.cmd("DATA"); !strings.HasPrefix(got, "354") {
		t.Fatalf("DATA: %q", got)
	}
	fmt.Fprintf(c.conn, "Subject: hi\r\n\r\nbody\r\n.\r\n")
	if got := c.reply(); !strings.HasPrefix(got, "250") {
		t.Fatalf("message should be accepted for the valid recipient, got %q", got)
	}
}

func TestSMTPMockAllRecipientsRefusedMeansNoDATA(t *testing.T) {
	e := New()
	m := &mock.Definition{
		ID: "m-allref", Name: "all-refused", Enabled: true, ProtocolType: "smtp",
		SMTP: &mock.SMTPConfig{DefaultAccept: true, Rules: []mock.SMTPRule{{MatchField: "to", Match: "nobody", Accept: false}}},
	}
	addr := registerOnFreePort(t, e, m)
	c := newRawConv(t, addr)

	c.cmd("EHLO test")
	c.cmd("MAIL FROM:<a@ok.test>")
	if got := c.cmd("RCPT TO:<nobody@x.test>"); !strings.HasPrefix(got, "550") {
		t.Fatalf("expected 550 at RCPT TO, got %q", got)
	}
	if got := c.cmd("DATA"); !strings.HasPrefix(got, "554") {
		t.Fatalf("DATA with no accepted recipient should be 554, got %q", got)
	}
}

func TestSMTPMockRefusesASenderAtMAILFromTime(t *testing.T) {
	e := New()
	m := &mock.Definition{
		ID: "m-from", Name: "from-stage", Enabled: true, ProtocolType: "smtp",
		SMTP: &mock.SMTPConfig{DefaultAccept: true, Rules: []mock.SMTPRule{
			{MatchField: "from", Match: "spam.test", Accept: false, ResponseCode: 553, ResponseMessage: "sender not allowed"},
		}},
	}
	addr := registerOnFreePort(t, e, m)
	c := newRawConv(t, addr)

	c.cmd("EHLO test")
	if got := c.cmd("MAIL FROM:<x@spam.test>"); got != "553 sender not allowed" {
		t.Fatalf("expected the sender refused at MAIL FROM, got %q", got)
	}
}

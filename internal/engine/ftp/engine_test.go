package ftpengine

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
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
	m.FTP.Port = 0
	if err := e.RegisterMock(m); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	ln, ok := e.listeners[m.ID]
	if !ok {
		t.Fatal("expected a listener to be registered")
	}
	return ln.Addr().String()
}

// testClient is a minimal hand-rolled FTP control-connection client, just
// enough to exercise this mock end to end without a third-party FTP
// library.
type testClient struct {
	t      *testing.T
	conn   net.Conn
	reader *bufio.Reader
}

func dialClient(t *testing.T, addr string) *testClient {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	c := &testClient{t: t, conn: conn, reader: bufio.NewReader(conn)}
	c.expect("220")
	return c
}

func (c *testClient) send(line string) {
	c.t.Helper()
	if _, err := c.conn.Write([]byte(line + "\r\n")); err != nil {
		c.t.Fatalf("write %q: %v", line, err)
	}
}

func (c *testClient) readLine() string {
	c.t.Helper()
	line, err := c.reader.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read line: %v", err)
	}
	return strings.TrimRight(line, "\r\n")
}

func (c *testClient) expect(codePrefix string) string {
	c.t.Helper()
	line := c.readLine()
	if !strings.HasPrefix(line, codePrefix) {
		c.t.Fatalf("expected reply starting %q, got %q", codePrefix, line)
	}
	return line
}

// enterPasv sends PASV and dials the returned data port on 127.0.0.1,
// returning the open data connection.
func (c *testClient) enterPasv() net.Conn {
	c.t.Helper()
	c.send("PASV")
	reply := c.expect("227")
	return c.dialPasvReply("127.0.0.1", reply)
}

// enterPasvOnHost dials an already-received PASV reply's port on host,
// letting a caller that sent PASV itself (to inspect the raw reply first)
// still reuse the port-parsing/dial logic.
func (c *testClient) enterPasvOnHost(host, reply string) net.Conn {
	c.t.Helper()
	return c.dialPasvReply(host, reply)
}

func (c *testClient) dialPasvReply(host, reply string) net.Conn {
	c.t.Helper()
	start := strings.Index(reply, "(")
	end := strings.Index(reply, ")")
	if start == -1 || end == -1 {
		c.t.Fatalf("malformed PASV reply: %q", reply)
	}
	parts := strings.Split(reply[start+1:end], ",")
	if len(parts) != 6 {
		c.t.Fatalf("malformed PASV address: %q", reply)
	}
	p1, _ := strconv.Atoi(parts[4])
	p2, _ := strconv.Atoi(parts[5])
	port := p1*256 + p2
	dataConn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		c.t.Fatalf("dial data port: %v", err)
	}
	dataConn.SetDeadline(time.Now().Add(5 * time.Second))
	return dataConn
}

func TestFTPMockLoginAndRetr(t *testing.T) {
	logger := &fakeHitLogger{}
	e := New()
	e.SetHitLogger(logger)
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "m1", Name: "ftp-test", Enabled: true, ProtocolType: "ftp",
		FTP: &mock.FTPConfig{
			Files: []mock.FTPFile{{Name: "hello.txt", Content: "hello world"}},
		},
	}
	addr := registerOnFreePort(t, e, m)

	c := dialClient(t, addr)
	c.send("USER anonymous")
	c.expect("331")
	c.send("PASS anything")
	c.expect("230")

	data := c.enterPasv()
	c.send("RETR hello.txt")
	c.expect("150")
	content, err := io.ReadAll(data)
	if err != nil {
		t.Fatalf("read data conn: %v", err)
	}
	data.Close()
	c.expect("226")

	if string(content) != "hello world" {
		t.Fatalf("expected file content %q, got %q", "hello world", content)
	}

	entries := logger.all()
	if len(entries) != 1 || entries[0].Path != "RETR hello.txt" {
		t.Fatalf("expected exactly one RETR hit logged, got %+v", entries)
	}
}

// TestFTPMockPasvAdvertisesTheAddressTheClientActuallyReached guards against
// PASV always reporting 127.0.0.1 regardless of which interface the client
// connected through (the control listener binds all interfaces). Dialing
// via the 127.0.0.2 loopback alias instead of 127.0.0.1 must get back a
// PASV reply for 127.0.0.2, and the resulting data connection must actually
// be reachable at that address.
func TestFTPMockPasvAdvertisesTheAddressTheClientActuallyReached(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "m-pasv-addr", Name: "pasv-addr-test", Enabled: true, ProtocolType: "ftp",
		FTP: &mock.FTPConfig{Files: []mock.FTPFile{{Name: "f.txt", Content: "data"}}},
	}
	addr := registerOnFreePort(t, e, m)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split listener addr %q: %v", addr, err)
	}

	c := dialClient(t, net.JoinHostPort("127.0.0.2", port))
	c.send("USER anonymous")
	c.expect("331")
	c.send("PASS anything")
	c.expect("230")
	c.send("PASV")
	reply := c.expect("227")
	if !strings.Contains(reply, "127,0,0,2,") {
		t.Fatalf("expected PASV reply to advertise 127.0.0.2 (the address the client reached), got %q", reply)
	}

	data := c.enterPasvOnHost("127.0.0.2", reply)
	c.send("RETR f.txt")
	c.expect("150")
	content, err := io.ReadAll(data)
	if err != nil {
		t.Fatalf("read data conn: %v", err)
	}
	data.Close()
	if string(content) != "data" {
		t.Fatalf("expected file content %q, got %q", "data", content)
	}
}

func TestFTPMockRejectsWrongCredentials(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "m2", Name: "auth-test", Enabled: true, ProtocolType: "ftp",
		FTP: &mock.FTPConfig{Username: "admin", Password: "secret"},
	}
	addr := registerOnFreePort(t, e, m)

	c := dialClient(t, addr)
	c.send("USER admin")
	c.expect("331")
	c.send("PASS wrongpassword")
	c.expect("530")

	// The connection should be closed after a failed login — a further
	// read must fail rather than hang.
	c.conn.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := c.conn.Read(buf); err == nil {
		t.Fatal("expected the connection to be closed after a failed login")
	}
}

func TestFTPMockRetrOfUnknownFileFails(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{ID: "m3", Name: "no-such-file", Enabled: true, ProtocolType: "ftp", FTP: &mock.FTPConfig{}}
	addr := registerOnFreePort(t, e, m)

	c := dialClient(t, addr)
	c.send("USER anonymous")
	c.expect("331")
	c.send("PASS x")
	c.expect("230")

	c.send("RETR missing.txt")
	c.expect("550")
}

func TestFTPMockListAndStor(t *testing.T) {
	logger := &fakeHitLogger{}
	e := New()
	e.SetHitLogger(logger)
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "m4", Name: "list-stor-test", Enabled: true, ProtocolType: "ftp",
		FTP: &mock.FTPConfig{Files: []mock.FTPFile{{Name: "a.txt", Content: "AAAA"}, {Name: "b.txt", Content: "BB"}}},
	}
	addr := registerOnFreePort(t, e, m)

	c := dialClient(t, addr)
	c.send("USER anonymous")
	c.expect("331")
	c.send("PASS x")
	c.expect("230")

	listData := c.enterPasv()
	c.send("LIST")
	c.expect("150")
	listing, err := io.ReadAll(listData)
	if err != nil {
		t.Fatalf("read LIST data: %v", err)
	}
	listData.Close()
	c.expect("226")
	if !strings.Contains(string(listing), "a.txt") || !strings.Contains(string(listing), "b.txt") {
		t.Fatalf("expected both files in the listing, got %q", listing)
	}

	storData := c.enterPasv()
	c.send("STOR uploaded.txt")
	c.expect("150")
	if _, err := storData.Write([]byte("uploaded content")); err != nil {
		t.Fatalf("write STOR data: %v", err)
	}
	storData.Close()
	c.expect("226")

	entries := logger.all()
	var sawStor bool
	for _, e := range entries {
		if e.Path == "STOR uploaded.txt" && e.RequestBody == "uploaded content" {
			sawStor = true
		}
	}
	if !sawStor {
		t.Fatalf("expected the STOR upload content to be logged, got %+v", entries)
	}
}

func TestUnregisterMockClosesListener(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{ID: "m5", Name: "temp", Enabled: true, ProtocolType: "ftp", FTP: &mock.FTPConfig{}}
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

	m := &mock.Definition{ID: "m6", Name: "disabled", Enabled: false, ProtocolType: "ftp", FTP: &mock.FTPConfig{Port: 0}}
	if err := e.RegisterMock(m); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	if _, ok := e.listeners[m.ID]; ok {
		t.Fatal("expected no listener for a disabled mock")
	}
}

// TestFTPMockFaultLatencyJitter guards against a real gap: def.Fault was
// only ever read by the HTTP-family matchers — an FTP mock's FaultConfig
// was silently never applied, so LatencyJitterMs did nothing.
func TestFTPMockFaultLatencyJitter(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "fault-latency", Name: "fault-latency", Enabled: true, ProtocolType: "ftp",
		Fault: &mock.FaultConfig{LatencyJitterMs: 200},
		FTP:   &mock.FTPConfig{Files: []mock.FTPFile{{Name: "hello.txt", Content: "hello world"}}},
	}
	addr := registerOnFreePort(t, e, m)

	c := dialClient(t, addr)
	c.send("USER anonymous")
	c.expect("331")
	c.send("PASS anything")
	c.expect("230")

	// LatencyJitterMs is uniformly random in [0, 200ms] per hit, so assert
	// on the max across several trials rather than one sample.
	var maxElapsed time.Duration
	for i := 0; i < 8; i++ {
		data := c.enterPasv()
		start := time.Now()
		c.send("RETR hello.txt")
		c.expect("150")
		io.ReadAll(data)
		data.Close()
		c.expect("226")
		if elapsed := time.Since(start); elapsed > maxElapsed {
			maxElapsed = elapsed
		}
	}
	if maxElapsed < 50*time.Millisecond {
		t.Fatalf("expected at least one of 8 trials to show noticeable latency jitter (up to 200ms), max observed was %s", maxElapsed)
	}
}

// TestFTPMockFaultTimeoutDropsConnection guards against the same gap for
// the "drop the connection" side of fault injection.
func TestFTPMockFaultTimeoutDropsConnection(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "fault-timeout", Name: "fault-timeout", Enabled: true, ProtocolType: "ftp",
		Fault: &mock.FaultConfig{TimeoutRatePercent: 100},
		FTP:   &mock.FTPConfig{Files: []mock.FTPFile{{Name: "hello.txt", Content: "hello world"}}},
	}
	addr := registerOnFreePort(t, e, m)

	c := dialClient(t, addr)
	c.send("USER anonymous")
	c.expect("331")
	c.send("PASS anything")
	c.expect("230")

	data := c.enterPasv()
	defer data.Close()
	c.send("RETR hello.txt")

	c.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 16)
	n, err := c.conn.Read(buf)
	if err == nil {
		t.Fatalf("expected the control connection to be dropped with no 150/226 reply, got %d bytes: %q", n, buf[:n])
	}
}

// TestFTPMockRejectsTransferCommandsBeforeLogin guards against LIST/RETR/STOR/
// PASV working on a connection that never completed USER/PASS.
func TestFTPMockRejectsTransferCommandsBeforeLogin(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "m-nologin", Name: "nologin-test", Enabled: true, ProtocolType: "ftp",
		FTP: &mock.FTPConfig{Username: "u", Password: "p", Files: []mock.FTPFile{{Name: "f.txt", Content: "data"}}},
	}
	addr := registerOnFreePort(t, e, m)
	c := dialClient(t, addr)
	for _, cmd := range []string{"PASV", "LIST", "RETR f.txt", "STOR f.txt"} {
		c.send(cmd)
		c.expect("530")
	}
}

package tcpengine

import (
	"bufio"
	"context"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/storage"
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

func (f *fakeHitLogger) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries)
}

// registerOnFreePort registers m (with Port left at 0, letting the OS pick a
// free port) and returns the actual bound address — port 0 keeps the test
// independent of any specific port being free on the machine running it.
func registerOnFreePort(t *testing.T, e *Engine, m *mock.Definition) string {
	t.Helper()
	m.TCP.Port = 0
	if err := e.RegisterMock(m); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	ln, ok := e.listeners[m.ID]
	if !ok {
		t.Fatal("expected a listener to be registered")
	}
	return ln.Addr().String()
}

func TestTCPMockBannerAndInteractionRoundTrip(t *testing.T) {
	logger := &fakeHitLogger{}
	e := New()
	e.SetHitLogger(logger)
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "m1",
		Name:         "telnet-echo",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			Banner: "220 welcome\n",
			Interactions: []mock.TCPInteraction{
				{Match: "PING", MatchType: "exact", Response: "PONG\n"},
			},
			DefaultResponse: "unknown\n",
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)

	banner, err := reader.ReadString('\n')
	if err != nil || banner != "220 welcome\r\n" {
		t.Fatalf("expected banner %q, got %q (err=%v)", "220 welcome\r\n", banner, err)
	}

	if _, err := conn.Write([]byte("PING\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err := reader.ReadString('\n')
	if err != nil || resp != "PONG\r\n" {
		t.Fatalf("expected %q, got %q (err=%v)", "PONG\r\n", resp, err)
	}

	if _, err := conn.Write([]byte("gibberish\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err = reader.ReadString('\n')
	if err != nil || resp != "unknown\r\n" {
		t.Fatalf("expected default response %q, got %q (err=%v)", "unknown\r\n", resp, err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for logger.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := logger.count(); got != 2 {
		t.Fatalf("expected 2 hit-log entries recorded, got %d", got)
	}
}

// TestTCPMockCounterFunctionWorksEndToEnd confirms SetDynamicValues/the
// ownerID threading through matchTCPInteraction actually works on a real
// connection, not just in internal/mock's own template unit tests.
func TestTCPMockCounterFunctionWorksEndToEnd(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	store := mock.NewStore(db)

	e := New()
	e.SetDynamicValues(store)
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "m-counter",
		Name:         "counter-echo",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			Interactions: []mock.TCPInteraction{
				{Match: "NEXT", MatchType: "exact", Response: "{{counter \"seq\"}}\n"},
			},
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)

	for i, want := range []string{"1\r\n", "2\r\n"} {
		if _, err := conn.Write([]byte("NEXT\n")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		resp, err := reader.ReadString('\n')
		if err != nil || resp != want {
			t.Fatalf("hit %d: expected %q, got %q (err=%v)", i, want, resp, err)
		}
	}
}

func TestTCPMockCloseAfterEndsConnection(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "m2",
		Name:         "quit-test",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			Interactions: []mock.TCPInteraction{
				{Match: "QUIT", MatchType: "exact", Response: "bye\n", CloseAfter: true},
			},
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	if _, err := conn.Write([]byte("QUIT\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err := reader.ReadString('\n')
	if err != nil || resp != "bye\r\n" {
		t.Fatalf("expected %q, got %q (err=%v)", "bye\r\n", resp, err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err == nil {
		t.Fatalf("expected the server to close the connection after CloseAfter, got %d more bytes: %q", n, buf[:n])
	}
}

func TestUnregisterMockClosesListener(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "m3",
		Name:         "temp",
		Enabled:      true,
		ProtocolType: "tcp",
		TCP:          &mock.TCPConfig{DefaultResponse: "hi\n"},
	}
	addr := registerOnFreePort(t, e, m)

	if err := e.UnregisterMock(m.ID); err != nil {
		t.Fatalf("UnregisterMock: %v", err)
	}

	if _, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		t.Fatal("expected dialing a closed listener's port to fail")
	}
}

func TestRegisterMockDisabledDoesNotListen(t *testing.T) {
	e := New()
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID:           "m4",
		Name:         "disabled",
		Enabled:      false,
		ProtocolType: "tcp",
		TCP:          &mock.TCPConfig{Port: 0, DefaultResponse: "hi\n"},
	}
	if err := e.RegisterMock(m); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}
	if _, ok := e.listeners[m.ID]; ok {
		t.Fatal("expected no listener to be bound for a disabled mock")
	}
}

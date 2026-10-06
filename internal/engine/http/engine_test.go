package httpengine

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/storage"
)

func TestServeRestRendersTemplatedResponse(t *testing.T) {
	e := New()

	def := &mock.Definition{
		ID:           "m1",
		ProtocolType: "rest",
		Method:       http.MethodGet,
		PathPattern:  "/hello/{name}",
		Enabled:      true,
		Response: mock.ResponseTemplate{
			StatusCode:   200,
			BodyTemplate: `{"greeting":"hello {{.Request.PathParams.name}}"}`,
		},
	}
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := "127.0.0.1:18711"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop(context.Background())

	resp, err := http.Get("http://" + addr + "/hello/world")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	want := `{"greeting":"hello world"}`
	if string(body) != want {
		t.Fatalf("expected body %q, got %q", want, string(body))
	}
}

// TestServeRestCounterAndCSVFunctionsWorkEndToEnd confirms the
// SetDynamicValues threading actually reaches a real request through the
// real engine (not just internal/mock's own template unit tests) — the
// specific gap this whole feature's implementation was most at risk of
// missing, since most of it is threading a new parameter through 17
// scattered call sites.
func TestServeRestCounterAndCSVFunctionsWorkEndToEnd(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	store := mock.NewStore(db)
	if _, err := store.SetCSVSource("m3", "round_robin", "name\nAlice\nBob\n"); err != nil {
		t.Fatalf("SetCSVSource: %v", err)
	}

	e := New()
	e.SetDynamicValues(store)
	def := &mock.Definition{
		ID:           "m3",
		ProtocolType: "rest",
		Method:       http.MethodGet,
		PathPattern:  "/hits",
		Enabled:      true,
		Response: mock.ResponseTemplate{
			StatusCode:   200,
			BodyTemplate: `{"hit":{{counter "hits"}},"name":"{{csv "name"}}"}`,
		},
	}
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18712"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop(context.Background())

	for i, want := range []string{`{"hit":1,"name":"Alice"}`, `{"hit":2,"name":"Bob"}`} {
		resp, err := http.Get("http://" + addr + "/hits")
		if err != nil {
			t.Fatalf("GET %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != want {
			t.Fatalf("hit %d: expected body %q, got %q", i, want, string(body))
		}
	}
}

func TestUnregisterMockRemovesRoute(t *testing.T) {
	e := New()
	def := &mock.Definition{
		ID: "m2", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/gone", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{}`},
	}
	e.RegisterMock(def)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18712"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop(context.Background())

	e.UnregisterMock("m2")
	time.Sleep(20 * time.Millisecond)

	resp, err := http.Get("http://" + addr + "/gone")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after unregister, got %d", resp.StatusCode)
	}
}

// fixedPortResolver is a test double for ProjectPortResolver: one project
// ID maps to one fixed port, everything else resolves to 0 (shared
// default gateway).
type fixedPortResolver map[string]int

func (r fixedPortResolver) GatewayPortForProject(projectID string) (int, error) {
	return r[projectID], nil
}

func TestProjectWithDedicatedPortIsServedThereNotOnTheDefaultGateway(t *testing.T) {
	e := New()
	e.SetProjectPortResolver(fixedPortResolver{"proj-1": 18799})

	def := &mock.Definition{
		ID: "m3", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/dedicated", Enabled: true,
		ProjectID: "proj-1",
		Response:  mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{"ok":true}`},
	}
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defaultAddr := "127.0.0.1:18713"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: defaultAddr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop(context.Background())

	// Reachable on the project's dedicated port.
	resp, err := http.Get("http://127.0.0.1:18799/dedicated")
	if err != nil {
		t.Fatalf("GET dedicated port: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on the dedicated port, got %d", resp.StatusCode)
	}

	// NOT reachable on the shared default gateway.
	resp2, err := http.Get("http://" + defaultAddr + "/dedicated")
	if err != nil {
		t.Fatalf("GET default gateway: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("expected the dedicated-port mock to be absent from the default gateway, got %d", resp2.StatusCode)
	}

	// Unregistering the mock tears the now-unused dedicated listener down.
	if err := e.UnregisterMock("m3"); err != nil {
		t.Fatalf("UnregisterMock: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := http.Get("http://127.0.0.1:18799/dedicated"); err == nil {
		t.Fatal("expected the dedicated listener to be shut down after its only mock was removed")
	}
}

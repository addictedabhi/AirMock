package httpengine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
)

// fakeHitLogger is an in-memory HitLogger for tests, avoiding a real sqlite
// store just to assert a capture happened.
type fakeHitLogger struct {
	entries []*hitlog.Entry
}

func (f *fakeHitLogger) Record(e *hitlog.Entry) error {
	f.entries = append(f.entries, e)
	return nil
}

func TestProxyForwardsAndCapturesTheRealResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orders/1" {
			t.Errorf("expected upstream path /orders/1, got %s", r.URL.Path)
		}
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"1","status":"real"}`))
	}))
	defer upstream.Close()

	logger := &fakeHitLogger{}
	e := New()
	e.SetHitLogger(logger)
	e.RegisterMock(&mock.Definition{
		ID: "p1", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/proxy/*", Enabled: true,
		Mode:  "proxy",
		Proxy: &mock.ProxyConfig{TargetBaseURL: upstream.URL},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18810"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	resp, err := http.Get("http://" + addr + "/proxy/orders/1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("expected the real upstream status 201, got %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Upstream") != "yes" {
		t.Fatalf("expected the real upstream header to pass through, got %+v", resp.Header)
	}
	body, _ := io.ReadAll(resp.Body)
	want := `{"id":"1","status":"real"}`
	if string(body) != want {
		t.Fatalf("expected the real upstream body %q, got %q", want, body)
	}

	if len(logger.entries) != 1 {
		t.Fatalf("expected exactly one captured hit-log entry, got %d", len(logger.entries))
	}
	captured := logger.entries[0]
	if captured.Direction != hitlog.DirectionProxyCapture {
		t.Fatalf("expected direction=proxy-capture, got %q", captured.Direction)
	}
	if captured.ResponseStatus != 201 || captured.ResponseBody != want {
		t.Fatalf("captured entry doesn't match the real response: %+v", captured)
	}
	if captured.MockID != "p1" {
		t.Fatalf("expected captured entry linked to mock p1, got %q", captured.MockID)
	}
}

// TestProxyRedactsHeadersInCaptureButNotInTheRealResponse guards against a
// real bug: serveProxy used to log raw request/response headers, bypassing
// the exact same redaction every inbound mock hit already gets — meaning
// proxy-capture, of all traffic directions, was the one most likely to leak
// a real Authorization/session header from a live upstream into the hit log.
func TestProxyRedactsHeadersInCaptureButNotInTheRealResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer real-secret-token" {
			t.Errorf("expected the real Authorization header to reach upstream, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Set-Cookie", "session=real-session-value")
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	logger := &fakeHitLogger{}
	e := New()
	e.SetHitLogger(logger)
	e.RegisterMock(&mock.Definition{
		ID: "p2", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/proxy2/*", Enabled: true,
		Mode:  "proxy",
		Proxy: &mock.ProxyConfig{TargetBaseURL: upstream.URL},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18811"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/proxy2/orders/1", nil)
	req.Header.Set("Authorization", "Bearer real-secret-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	// The real client still sees the real upstream response headers —
	// redaction must only affect what's written to the hit log.
	if got := resp.Header.Get("Set-Cookie"); got != "session=real-session-value" {
		t.Fatalf("expected the real Set-Cookie to reach the client unredacted, got %q", got)
	}

	if len(logger.entries) != 1 {
		t.Fatalf("expected exactly one captured hit-log entry, got %d", len(logger.entries))
	}
	captured := logger.entries[0]
	if captured.RequestHeaders["Authorization"] != "***REDACTED***" {
		t.Fatalf("expected the captured Authorization header to be redacted, got %q", captured.RequestHeaders["Authorization"])
	}
	if captured.ResponseHeaders["Set-Cookie"] != "***REDACTED***" {
		t.Fatalf("expected the captured Set-Cookie header to be redacted, got %q", captured.ResponseHeaders["Set-Cookie"])
	}
}

func TestProxyTargetURLDoesNotInventATrailingSlash(t *testing.T) {
	cases := []struct{ base, wildcard, reqPath, want string }{
		{"http://up.test", "", "/proxy", "http://up.test"},
		{"http://up.test/", "", "/proxy", "http://up.test"},
		{"http://up.test/api", "", "/proxy", "http://up.test/api"},
		{"http://up.test", "", "/proxy/", "http://up.test/"},
		{"http://up.test", "orders/1", "/proxy/orders/1", "http://up.test/orders/1"},
		{"http://up.test/api/", "orders/", "/proxy/orders/", "http://up.test/api/orders/"},
	}
	for _, c := range cases {
		if got := proxyTargetURL(c.base, c.wildcard, c.reqPath); got != c.want {
			t.Errorf("proxyTargetURL(%q, %q, %q) = %q, want %q", c.base, c.wildcard, c.reqPath, got, c.want)
		}
	}
}

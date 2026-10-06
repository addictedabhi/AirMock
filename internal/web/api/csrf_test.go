package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func guarded(allowed ...string) http.Handler {
	return SameOriginGuard(allowed)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
}

func send(h http.Handler, method, host string, hdr map[string]string) int {
	req := httptest.NewRequest(method, "http://"+host+"/api/mocks", strings.NewReader("x"))
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestSameOriginGuardBlocksCrossSiteWrites(t *testing.T) {
	h := guarded()
	cases := []struct {
		name   string
		method string
		hdr    map[string]string
		want   int
	}{
		{"foreign Origin simple POST", "POST", map[string]string{"Origin": "http://evil.example", "Content-Type": "text/plain"}, 403},
		{"foreign Origin DELETE", "DELETE", map[string]string{"Origin": "https://evil.example"}, 403},
		{"opaque null Origin", "POST", map[string]string{"Origin": "null"}, 403},
		{"same-site other port", "POST", map[string]string{"Origin": "http://localhost:3000"}, 403},
		{"cross-site fetch metadata, no Origin", "POST", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"same-origin", "POST", map[string]string{"Origin": "http://localhost:8080", "Sec-Fetch-Site": "same-origin"}, 204},
		{"same Origin differing only by case", "PUT", map[string]string{"Origin": "http://LOCALHOST:8080"}, 204},
		{"non-browser client (no Origin)", "POST", nil, 204},
		{"direct navigation fetch metadata", "POST", map[string]string{"Sec-Fetch-Site": "none"}, 204},
		{"GET is never blocked", "GET", map[string]string{"Origin": "http://evil.example"}, 204},
	}
	for _, c := range cases {
		if got := send(h, c.method, "localhost:8080", c.hdr); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestSameOriginGuardHonoursConfiguredOrigins(t *testing.T) {
	h := guarded("https://mocks.corp.example")
	if got := send(h, "POST", "localhost:8080", map[string]string{"Origin": "https://mocks.corp.example"}); got != 204 {
		t.Fatalf("allow-listed origin should pass, got %d", got)
	}
	if got := send(h, "POST", "localhost:8080", map[string]string{"Origin": "https://other.example"}); got != 403 {
		t.Fatalf("other origins should still be blocked, got %d", got)
	}
}

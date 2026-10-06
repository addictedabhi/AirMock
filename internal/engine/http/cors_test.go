package httpengine

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
)

func startCORSEngine(t *testing.T, cfg *CORSConfig, defs ...*mock.Definition) (*Engine, string) {
	t.Helper()
	e := New()
	if cfg != nil {
		e.SetCORS(*cfg)
	}
	for _, d := range defs {
		e.RegisterMock(d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	addr := freeAddr(t)
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return e, "http://" + addr
}

func hello() *mock.Definition {
	return &mock.Definition{
		ID: "hello", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/hello", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, Headers: map[string]string{"X-Custom": "1"}, BodyTemplate: "hi"},
	}
}

func do(t *testing.T, method, url string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestHeadIsServedForGETMocksWithHeadersAndNoBody(t *testing.T) {
	_, base := startCORSEngine(t, nil, hello())
	resp, body := do(t, http.MethodHead, base+"/hello", nil)
	if resp.StatusCode != 200 || resp.Header.Get("X-Custom") != "1" || body != "" {
		t.Fatalf("expected 200 + X-Custom header + empty body for HEAD, got %d %q %q", resp.StatusCode, resp.Header.Get("X-Custom"), body)
	}
}

func TestAnExplicitHeadMockWinsOverTheImplicitOne(t *testing.T) {
	head := &mock.Definition{
		ID: "head", ProtocolType: "rest", Method: http.MethodHead, PathPattern: "/hello", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 204},
	}
	_, base := startCORSEngine(t, nil, hello(), head)
	if resp, _ := do(t, http.MethodHead, base+"/hello", nil); resp.StatusCode != 204 {
		t.Fatalf("explicit HEAD mock should be used, got %d", resp.StatusCode)
	}
}

func TestPreflightIsAnsweredWithPermissiveDefaults(t *testing.T) {
	_, base := startCORSEngine(t, nil, hello())
	resp, _ := do(t, http.MethodOptions, base+"/hello", map[string]string{
		"Origin": "http://app.example", "Access-Control-Request-Method": "PUT", "Access-Control-Request-Headers": "authorization, content-type",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 for a preflight, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin = %q, want *", got)
	}
	if !strings.Contains(resp.Header.Get("Access-Control-Allow-Methods"), "PUT") {
		t.Fatalf("Allow-Methods = %q", resp.Header.Get("Access-Control-Allow-Methods"))
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); !strings.EqualFold(got, "authorization, content-type") {
		t.Fatalf("requested headers must be reflected (a bare * does not cover Authorization), got %q", got)
	}
	if resp.Header.Get("Access-Control-Max-Age") == "" {
		t.Fatal("expected Access-Control-Max-Age")
	}
}

func TestActualResponsesCarryAllowOriginAndExposeHeaders(t *testing.T) {
	_, base := startCORSEngine(t, nil, hello())
	resp, _ := do(t, http.MethodGet, base+"/hello", map[string]string{"Origin": "http://app.example"})
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" || resp.Header.Get("Access-Control-Expose-Headers") != "*" {
		t.Fatalf("expected CORS headers on a normal response, got %v", resp.Header)
	}
	// No Origin: not a CORS request, nothing added.
	resp, _ = do(t, http.MethodGet, base+"/hello", nil)
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("no CORS headers expected without an Origin, got %v", resp.Header)
	}
}

func TestCORSCanBeRestrictedToListedOriginsWithCredentials(t *testing.T) {
	cfg := CORSConfig{Enabled: true, AllowOrigin: "http://a.example, http://b.example", AllowMethods: "GET", AllowHeaders: "*", AllowCredentials: true, MaxAgeSecs: 60}
	_, base := startCORSEngine(t, &cfg, hello())

	resp, _ := do(t, http.MethodGet, base+"/hello", map[string]string{"Origin": "http://b.example"})
	if resp.Header.Get("Access-Control-Allow-Origin") != "http://b.example" || resp.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("listed origin must be echoed with credentials allowed, got %v", resp.Header)
	}
	if resp.Header.Get("Access-Control-Expose-Headers") == "*" {
		t.Fatal("a wildcard Expose-Headers is ignored by browsers when credentials are allowed")
	}
	resp, _ = do(t, http.MethodGet, base+"/hello", map[string]string{"Origin": "http://evil.example"})
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("an unlisted origin must get no Allow-Origin, got %v", resp.Header)
	}
}

func TestCORSCanBeDisabled(t *testing.T) {
	cfg := CORSConfig{Enabled: false}
	_, base := startCORSEngine(t, &cfg, hello())
	resp, _ := do(t, http.MethodOptions, base+"/hello", map[string]string{"Origin": "http://app.example", "Access-Control-Request-Method": "GET"})
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("with CORS off the preflight is just an unrouted OPTIONS, got %d %v", resp.StatusCode, resp.Header)
	}
}

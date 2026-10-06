package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/config"
)

func TestConfigHandlerReturnsPorts(t *testing.T) {
	cfg := &config.Config{AdminPort: 8080, GatewayPort: 8081, GatewayTLSPort: 8443}
	r := chi.NewRouter()
	r.Route("/api/config", NewConfigHandler(cfg).Routes)

	req := httptest.NewRequest(http.MethodGet, "/api/config/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["adminPort"] != float64(8080) || body["gatewayPort"] != float64(8081) || body["gatewayTlsPort"] != float64(8443) {
		t.Fatalf("unexpected config body: %+v", body)
	}
}

// TestConfigHandlerReturnsVersionAndRuntimeInfo guards the fields Settings'
// About card depends on — version comes straight from cfg (set by main.go
// from the ldflags-injected build version at startup, "dev" for a plain
// `go build`), while goVersion/os/arch are always available from the
// runtime package regardless of build config.
func TestConfigHandlerReturnsVersionAndRuntimeInfo(t *testing.T) {
	cfg := &config.Config{AdminPort: 8080, GatewayPort: 8081, GatewayTLSPort: 8443, Version: "1.2.3"}
	r := chi.NewRouter()
	r.Route("/api/config", NewConfigHandler(cfg).Routes)

	req := httptest.NewRequest(http.MethodGet, "/api/config/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["version"] != "1.2.3" {
		t.Fatalf("expected the configured version to be returned, got %+v", body["version"])
	}
	if body["goVersion"] != runtime.Version() {
		t.Fatalf("expected the actual Go runtime version, got %+v", body["goVersion"])
	}
	if body["os"] != runtime.GOOS || body["arch"] != runtime.GOARCH {
		t.Fatalf("expected the actual OS/arch, got os=%+v arch=%+v", body["os"], body["arch"])
	}
}

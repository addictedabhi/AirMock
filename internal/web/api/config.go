package api

import (
	"net/http"
	"runtime"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/config"
)

// ConfigHandler exposes the instance's runtime network configuration and
// build/runtime identity read-only — the admin UI uses the network fields
// to show accurate "how to test this mock" connection examples (which
// gateway port a REST/SOAP/GraphQL/WS mock is actually reachable on)
// without hardcoding the default, and the version/goVersion/os/arch fields
// for Settings' About card.
type ConfigHandler struct {
	cfg *config.Config
}

func NewConfigHandler(cfg *config.Config) *ConfigHandler {
	return &ConfigHandler{cfg: cfg}
}

func (h *ConfigHandler) Routes(r chi.Router) {
	r.Get("/", h.get)
}

func (h *ConfigHandler) get(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"adminPort":      h.cfg.AdminPort,
		"gatewayPort":    h.cfg.GatewayPort,
		"gatewayTlsPort": h.cfg.GatewayTLSPort,
		"version":        h.cfg.Version,
		"commit":         h.cfg.Commit,
		"buildDate":      h.cfg.BuildDate,
		"dirty":          h.cfg.Modified,
		"adminHost":      h.cfg.AdminHost,
		"goVersion":      runtime.Version(),
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
	})
}

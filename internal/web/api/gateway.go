package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/certs"
	httpengine "github.com/addictedabhi/airmock/internal/engine/http"
)

type GatewayHandler struct {
	certStore *certs.Store
	engine    *httpengine.Engine
	tlsAddr   string
}

func NewGatewayHandler(certStore *certs.Store, engine *httpengine.Engine, tlsAddr string) *GatewayHandler {
	return &GatewayHandler{certStore: certStore, engine: engine, tlsAddr: tlsAddr}
}

func (h *GatewayHandler) Routes(r chi.Router) {
	r.Get("/tls", h.getTLS)
	r.Put("/tls", h.putTLS)
}

type tlsSettingsBody struct {
	Enabled        bool   `json:"enabled"`
	CertID         string `json:"certId"`
	BundleID       string `json:"bundleId,omitempty"`
	ClientCertMode string `json:"clientCertMode"`
	ClientCAID     string `json:"clientCaId"`
}

func (h *GatewayHandler) getTLS(w http.ResponseWriter, r *http.Request) {
	gs, err := h.certStore.GetGatewaySettings()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":        gs.Enabled,
		"certId":         gs.CertID,
		"bundleId":       gs.BundleID,
		"clientCertMode": gs.ClientCertMode,
		"clientCaId":     gs.ClientCAID,
		"port":           h.tlsAddr,
	})
}

func (h *GatewayHandler) putTLS(w http.ResponseWriter, r *http.Request) {
	var body tlsSettingsBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	gs := &certs.GatewaySettings{
		Enabled:        body.Enabled,
		CertID:         body.CertID,
		BundleID:       body.BundleID,
		ClientCertMode: certs.ClientCertMode(body.ClientCertMode),
		ClientCAID:     body.ClientCAID,
	}

	if err := ApplyGatewayTLS(r.Context(), h.certStore, h.engine, h.tlsAddr, gs); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := h.certStore.SaveGatewaySettings(gs); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, gs)
}

// resolveBundleTLSRefs resolves bundleID (if set) into its server
// cert/CA IDs, which take precedence over any directly-set certID/
// clientCAID — picking a bundle is meant to replace picking the two
// individually. Shared by ApplyGatewayTLS and ProjectTLSResolver so a
// bundle resolves identically everywhere it's used.
func resolveBundleTLSRefs(store *certs.Store, bundleID, certID, clientCAID string) (resolvedCertID, resolvedClientCAID string, err error) {
	resolvedCertID, resolvedClientCAID = certID, clientCAID
	if bundleID == "" {
		return resolvedCertID, resolvedClientCAID, nil
	}
	b, err := store.GetBundle(bundleID)
	if err != nil {
		return "", "", fmt.Errorf("resolve certificate bundle: %w", err)
	}
	if b.ServerCertID != "" {
		resolvedCertID = b.ServerCertID
	}
	if b.CAID != "" {
		resolvedClientCAID = b.CAID
	}
	return resolvedCertID, resolvedClientCAID, nil
}

// ApplyGatewayTLS resolves gs's referenced certificates (directly, or via a
// bundle) and brings the engine's HTTPS listener to match: started/updated
// if enabled, stopped if not. Shared by the admin API (live updates) and
// server startup (restoring persisted settings), so both paths behave
// identically.
func ApplyGatewayTLS(ctx context.Context, store *certs.Store, engine *httpengine.Engine, tlsAddr string, gs *certs.GatewaySettings) error {
	if !gs.Enabled {
		return engine.DisableTLS(ctx)
	}

	certID, clientCAID, err := resolveBundleTLSRefs(store, gs.BundleID, gs.CertID, gs.ClientCAID)
	if err != nil {
		return err
	}
	if certID == "" {
		return errors.New("certId (or a bundle with a server certificate) is required when TLS is enabled")
	}
	cert, err := store.Get(certID)
	if err != nil {
		return fmt.Errorf("resolve cert: %w", err)
	}
	if cert.Kind != certs.KindServer {
		return fmt.Errorf("certificate %q is not a server certificate", cert.Name)
	}

	settings := httpengine.TLSSettings{
		CertPEM: []byte(cert.CertPEM),
		KeyPEM:  []byte(cert.KeyPEM),
	}

	switch gs.ClientCertMode {
	case certs.ClientCertOptional, certs.ClientCertRequired:
		if clientCAID == "" {
			return fmt.Errorf("clientCaId (or a bundle with a CA) is required when clientCertMode is %q", gs.ClientCertMode)
		}
		ca, err := store.Get(clientCAID)
		if err != nil {
			return fmt.Errorf("resolve client CA: %w", err)
		}
		if ca.Kind != certs.KindCA {
			return fmt.Errorf("certificate %q is not a CA", ca.Name)
		}
		settings.ClientCertMode = string(gs.ClientCertMode)
		settings.ClientCAPEM = []byte(ca.CertPEM)
	}

	return engine.EnsureTLS(ctx, tlsAddr, settings)
}

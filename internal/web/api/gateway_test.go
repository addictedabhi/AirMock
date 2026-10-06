package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/certs"
	httpengine "github.com/addictedabhi/airmock/internal/engine/http"
	"github.com/addictedabhi/airmock/internal/storage"
)

// newTestGatewayRouter builds a router mounting GatewayHandler over a real
// *httpengine.Engine (not fakeEngine) — GatewayHandler's putTLS ultimately
// calls engine.EnsureTLS, which starts a real crypto/tls listener via
// http.Server.ListenAndServeTLS. Binding "127.0.0.1:0" lets the OS pick an
// ephemeral, always-free port so tests never collide, and DisableTLS is
// deferred via t.Cleanup so each test's listener is torn down before the
// next one starts.
func newTestGatewayRouter(t *testing.T) (chi.Router, *certs.Store, *httpengine.Engine) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	certStore := certs.NewStore(db)
	eng := httpengine.New()
	t.Cleanup(func() { eng.DisableTLS(context.Background()) })

	r := chi.NewRouter()
	r.Route("/api/gateway", NewGatewayHandler(certStore, eng, "127.0.0.1:0").Routes)
	return r, certStore, eng
}

func mustGenerateGatewayCert(t *testing.T, store *certs.Store, name string, kind certs.Kind, issuer *certs.Certificate) *certs.Certificate {
	t.Helper()
	c, err := certs.Generate(certs.GenerateRequest{
		Name: name, Kind: kind, CommonName: name + ".local",
		KeyAlgorithm: certs.KeyECDSA, ValidDays: 30, Issuer: issuer,
	})
	if err != nil {
		t.Fatalf("certs.Generate(%s): %v", name, err)
	}
	if err := store.Save(c); err != nil {
		t.Fatalf("store.Save(%s): %v", name, err)
	}
	return c
}

func TestGetTLSDefaultsToUnconfigured(t *testing.T) {
	r, _, _ := newTestGatewayRouter(t)

	rec := doJSON(t, r, http.MethodGet, "/api/gateway/tls", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["enabled"] != false {
		t.Fatalf("expected enabled=false by default, got %+v", body)
	}
	if body["certId"] != "" || body["clientCaId"] != "" {
		t.Fatalf("expected empty certId/clientCaId by default, got %+v", body)
	}
	if body["clientCertMode"] != "" {
		t.Fatalf("expected clientCertMode empty by default, got %+v", body)
	}
	if body["port"] != "127.0.0.1:0" {
		t.Fatalf("expected the configured tlsAddr to be reported as port, got %+v", body)
	}
}

func TestPutTLSSuccessWithValidServerCert(t *testing.T) {
	r, certStore, _ := newTestGatewayRouter(t)
	ca := mustGenerateGatewayCert(t, certStore, "root-ca", certs.KindCA, nil)
	server := mustGenerateGatewayCert(t, certStore, "gateway-server", certs.KindServer, ca)

	rec := doJSON(t, r, http.MethodPut, "/api/gateway/tls", tlsSettingsBody{
		Enabled: true, CertID: server.ID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var gs certs.GatewaySettings
	if err := json.Unmarshal(rec.Body.Bytes(), &gs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !gs.Enabled || gs.CertID != server.ID {
		t.Fatalf("unexpected saved settings: %+v", gs)
	}

	// Confirm it was actually persisted, not just echoed back.
	getRec := doJSON(t, r, http.MethodGet, "/api/gateway/tls", nil)
	var got map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if got["enabled"] != true || got["certId"] != server.ID {
		t.Fatalf("expected persisted settings to round-trip via GET, got %+v", got)
	}
}

func TestPutTLSFailsForNonExistentCertID(t *testing.T) {
	r, _, _ := newTestGatewayRouter(t)

	rec := doJSON(t, r, http.MethodPut, "/api/gateway/tls", tlsSettingsBody{
		Enabled: true, CertID: "does-not-exist",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-existent certId, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPutTLSFailsForWrongKindCertID(t *testing.T) {
	r, certStore, _ := newTestGatewayRouter(t)
	ca := mustGenerateGatewayCert(t, certStore, "root-ca-2", certs.KindCA, nil)

	// A CA cert is not a server cert — putTLS must reject it.
	rec := doJSON(t, r, http.MethodPut, "/api/gateway/tls", tlsSettingsBody{
		Enabled: true, CertID: ca.ID,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when certId is not a server certificate, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPutTLSRequireClientCertSuccessWithValidCA(t *testing.T) {
	r, certStore, _ := newTestGatewayRouter(t)
	ca := mustGenerateGatewayCert(t, certStore, "root-ca-3", certs.KindCA, nil)
	server := mustGenerateGatewayCert(t, certStore, "gateway-server-3", certs.KindServer, ca)
	clientCA := mustGenerateGatewayCert(t, certStore, "client-ca-3", certs.KindCA, nil)

	rec := doJSON(t, r, http.MethodPut, "/api/gateway/tls", tlsSettingsBody{
		Enabled: true, CertID: server.ID, ClientCertMode: "required", ClientCAID: clientCA.ID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var gs certs.GatewaySettings
	if err := json.Unmarshal(rec.Body.Bytes(), &gs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if gs.ClientCertMode != certs.ClientCertRequired || gs.ClientCAID != clientCA.ID {
		t.Fatalf("unexpected saved settings: %+v", gs)
	}
}

func TestPutTLSRequireClientCertFailsForNonCAClientID(t *testing.T) {
	r, certStore, _ := newTestGatewayRouter(t)
	ca := mustGenerateGatewayCert(t, certStore, "root-ca-4", certs.KindCA, nil)
	server := mustGenerateGatewayCert(t, certStore, "gateway-server-4", certs.KindServer, ca)
	// A server cert (not a CA) used as the client CA must be rejected.
	notACA := mustGenerateGatewayCert(t, certStore, "not-a-ca-4", certs.KindServer, ca)

	rec := doJSON(t, r, http.MethodPut, "/api/gateway/tls", tlsSettingsBody{
		Enabled: true, CertID: server.ID, ClientCertMode: "required", ClientCAID: notACA.ID,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when clientCaId is not a CA certificate, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPutTLSRequireClientCertFailsWhenClientCAIDMissing(t *testing.T) {
	r, certStore, _ := newTestGatewayRouter(t)
	ca := mustGenerateGatewayCert(t, certStore, "root-ca-5", certs.KindCA, nil)
	server := mustGenerateGatewayCert(t, certStore, "gateway-server-5", certs.KindServer, ca)

	rec := doJSON(t, r, http.MethodPut, "/api/gateway/tls", tlsSettingsBody{
		Enabled: true, CertID: server.ID, ClientCertMode: "required",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when clientCertMode is required without a clientCaId, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestPutTLSOptionalClientCertAcceptsMissingClientCert confirms the new
// tri-state's middle option: "optional" still requires a valid clientCaId
// to configure (there'd be nothing to verify against otherwise), but unlike
// "required" it doesn't reject an unauthenticated connection at the TLS
// layer — that distinction is exercised end-to-end in tls_test.go; this
// just guards the settings save/validate path accepts it.
func TestPutTLSOptionalClientCertAcceptsValidCA(t *testing.T) {
	r, certStore, _ := newTestGatewayRouter(t)
	ca := mustGenerateGatewayCert(t, certStore, "root-ca-6", certs.KindCA, nil)
	server := mustGenerateGatewayCert(t, certStore, "gateway-server-6", certs.KindServer, ca)
	clientCA := mustGenerateGatewayCert(t, certStore, "client-ca-6", certs.KindCA, nil)

	rec := doJSON(t, r, http.MethodPut, "/api/gateway/tls", tlsSettingsBody{
		Enabled: true, CertID: server.ID, ClientCertMode: "optional", ClientCAID: clientCA.ID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var gs certs.GatewaySettings
	if err := json.Unmarshal(rec.Body.Bytes(), &gs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if gs.ClientCertMode != certs.ClientCertOptional {
		t.Fatalf("expected ClientCertMode=optional to round-trip, got %+v", gs)
	}
}

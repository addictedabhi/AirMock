package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/certs"
	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/storage"
)

func newTestCertificatesRouter(t *testing.T) (chi.Router, *certs.Store, *mock.Store) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	certStore := certs.NewStore(db)
	mockStore := mock.NewStore(db)
	r := chi.NewRouter()
	r.Route("/api/certificates", NewCertificatesHandler(certStore, mockStore, nil, "").Routes)
	return r, certStore, mockStore
}

func mustGenerateCert(t *testing.T, store *certs.Store, name string) *certs.Certificate {
	t.Helper()
	c, err := certs.Generate(certs.GenerateRequest{
		Name: name, Kind: certs.KindCA, CommonName: "localhost",
		KeyAlgorithm: certs.KeyECDSA, ValidDays: 30,
	})
	if err != nil {
		t.Fatalf("certs.Generate: %v", err)
	}
	if err := store.Save(c); err != nil {
		t.Fatalf("store.Save: %v", err)
	}
	return c
}

// TestCertificateUsageReportsSMTPMocks guards against the usage report only
// ever inspecting TCP mocks' TLS binding — an SMTP mock's own dedicated
// listener supports the exact same per-mock TLS binding shape
// (SMTPConfig.TLS *TCPTLSConfig), so a certificate referenced only by an
// SMTP mock must show up here too, or a user could delete it thinking
// nothing depends on it and silently break that mock's listener.
func TestCertificateUsageReportsSMTPMocks(t *testing.T) {
	r, certStore, mockStore := newTestCertificatesRouter(t)
	cert := mustGenerateCert(t, certStore, "smtp-cert")

	if _, err := mockStore.Create(&mock.Definition{
		Name: "smtp-mock", ProtocolType: "smtp", Enabled: true,
		SMTP: &mock.SMTPConfig{Port: 2525, TLS: &mock.TCPTLSConfig{CertificateID: cert.ID}},
	}); err != nil {
		t.Fatalf("create smtp mock: %v", err)
	}

	rec := doJSON(t, r, "GET", "/api/certificates/"+cert.ID+"/usage", nil)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var u certUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatalf("decode usage response: %v", err)
	}
	if len(u.SMTPMocks) != 1 || u.SMTPMocks[0].Name != "smtp-mock" {
		t.Fatalf("expected the SMTP mock to be reported as a usage reference, got %+v", u)
	}
}

func TestCertificateUsageReportsTCPMocks(t *testing.T) {
	r, certStore, mockStore := newTestCertificatesRouter(t)
	cert := mustGenerateCert(t, certStore, "tcp-cert")

	if _, err := mockStore.Create(&mock.Definition{
		Name: "tcp-mock", ProtocolType: "tcp", Enabled: true,
		TCP: &mock.TCPConfig{Port: 9025, TLS: &mock.TCPTLSConfig{CertificateID: cert.ID}},
	}); err != nil {
		t.Fatalf("create tcp mock: %v", err)
	}

	rec := doJSON(t, r, "GET", "/api/certificates/"+cert.ID+"/usage", nil)
	var u certUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatalf("decode usage response: %v", err)
	}
	if len(u.TCPMocks) != 1 || u.TCPMocks[0].Name != "tcp-mock" {
		t.Fatalf("expected the TCP mock to be reported as a usage reference, got %+v", u)
	}
}

// TestCertificateUsageReportsMocksReferencingItOnlyAsClientCAID guards
// against a real gap: usage() only ever checked CertificateID (the
// presented server/leaf cert), never ClientCAID (the mTLS client-verify
// CA) — a CA referenced solely as a TCP/SMTP mock's ClientCAID showed zero
// usage, so deleting it gave no warning even though the referencing mock's
// listener would fail to bind on its next RegisterMock (clientCAPool's
// certProvider.Get(ClientCAID) would then return ErrNotFound).
func TestCertificateUsageReportsMocksReferencingItOnlyAsClientCAID(t *testing.T) {
	r, certStore, mockStore := newTestCertificatesRouter(t)
	ca := mustGenerateCert(t, certStore, "client-ca")
	serverCert := mustGenerateCert(t, certStore, "server-cert")

	if _, err := mockStore.Create(&mock.Definition{
		Name: "mtls-tcp-mock", ProtocolType: "tcp", Enabled: true,
		TCP: &mock.TCPConfig{Port: 9026, TLS: &mock.TCPTLSConfig{
			CertificateID: serverCert.ID, ClientCertMode: "required", ClientCAID: ca.ID,
		}},
	}); err != nil {
		t.Fatalf("create tcp mock: %v", err)
	}
	if _, err := mockStore.Create(&mock.Definition{
		Name: "mtls-smtp-mock", ProtocolType: "smtp", Enabled: true,
		SMTP: &mock.SMTPConfig{Port: 2526, TLS: &mock.TCPTLSConfig{
			CertificateID: serverCert.ID, ClientCertMode: "required", ClientCAID: ca.ID,
		}},
	}); err != nil {
		t.Fatalf("create smtp mock: %v", err)
	}

	rec := doJSON(t, r, "GET", "/api/certificates/"+ca.ID+"/usage", nil)
	var u certUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatalf("decode usage response: %v", err)
	}
	if len(u.TCPMocks) != 1 || u.TCPMocks[0].Name != "mtls-tcp-mock" {
		t.Fatalf("expected the TCP mock referencing this cert as ClientCAID to be reported, got %+v", u)
	}
	if len(u.SMTPMocks) != 1 || u.SMTPMocks[0].Name != "mtls-smtp-mock" {
		t.Fatalf("expected the SMTP mock referencing this cert as ClientCAID to be reported, got %+v", u)
	}
}

// TestCertificateUsageIgnoresClientCAIDWhenClientCertModeIsNone confirms
// a stale/leftover ClientCAID value on a mock that doesn't require/accept a
// client cert doesn't spuriously flag that cert as "in use" — only
// ClientCertMode "optional"/"required" makes ClientCAID an actual live
// reference, matching internal/engine/tcp's wrapTLS, which only ever calls
// clientCAPool in those two modes.
func TestCertificateUsageIgnoresClientCAIDWhenClientCertModeIsNone(t *testing.T) {
	r, certStore, mockStore := newTestCertificatesRouter(t)
	ca := mustGenerateCert(t, certStore, "unused-ca")
	serverCert := mustGenerateCert(t, certStore, "server-cert-2")

	if _, err := mockStore.Create(&mock.Definition{
		Name: "plain-tls-tcp-mock", ProtocolType: "tcp", Enabled: true,
		TCP: &mock.TCPConfig{Port: 9027, TLS: &mock.TCPTLSConfig{
			CertificateID: serverCert.ID, ClientCAID: ca.ID,
		}},
	}); err != nil {
		t.Fatalf("create tcp mock: %v", err)
	}

	rec := doJSON(t, r, "GET", "/api/certificates/"+ca.ID+"/usage", nil)
	var u certUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatalf("decode usage response: %v", err)
	}
	if len(u.TCPMocks) != 0 {
		t.Fatalf("expected no usage reported for a ClientCAID that's inert (ClientCertMode none), got %+v", u)
	}
}

func TestCertificateUsageReportsNothingWhenUnreferenced(t *testing.T) {
	r, certStore, _ := newTestCertificatesRouter(t)
	cert := mustGenerateCert(t, certStore, "unused-cert")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/certificates/"+cert.ID+"/usage", nil)
	r.ServeHTTP(rec, req)
	var u certUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatalf("decode usage response: %v", err)
	}
	if u.GatewayServer || u.GatewayClientCA || len(u.TCPMocks) != 0 || len(u.SMTPMocks) != 0 || len(u.IssuedCerts) != 0 {
		t.Fatalf("expected no usage references for an unreferenced cert, got %+v", u)
	}
}

// TestCertificateListEmpty guards store.List's `out := []*Certificate{}`
// initialization surfacing all the way through the handler as a JSON `[]`,
// not `null` — a client that does `for (const c of list)` on `null` throws.
func TestCertificateListEmpty(t *testing.T) {
	r, _, _ := newTestCertificatesRouter(t)

	rec := doJSON(t, r, "GET", "/api/certificates", nil)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("expected an empty JSON array body, got %q", got)
	}
}

// TestCertificateListReturnsEveryCreatedCertificateWithoutKeyPEM guards both
// that list reports every stored certificate and that it never leaks the
// private key (model.go tags KeyPEM json:"-"; download is the only place
// the key is meant to leave the store).
func TestCertificateListReturnsEveryCreatedCertificateWithoutKeyPEM(t *testing.T) {
	r, certStore, _ := newTestCertificatesRouter(t)
	c1 := mustGenerateCert(t, certStore, "cert-one")
	c2 := mustGenerateCert(t, certStore, "cert-two")

	rec := doJSON(t, r, "GET", "/api/certificates", nil)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "PRIVATE KEY") {
		t.Fatalf("list response must never expose the private key PEM, got %s", rec.Body.String())
	}
	var list []*certs.Certificate
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 certificates, got %d: %+v", len(list), list)
	}
	names := map[string]bool{}
	for _, c := range list {
		names[c.Name] = true
		if c.KeyPEM != "" {
			t.Fatalf("expected KeyPEM to never be populated from the JSON response, got %q", c.KeyPEM)
		}
	}
	if !names[c1.Name] || !names[c2.Name] {
		t.Fatalf("expected both created certificates in the list, got %+v", list)
	}
}

// TestCertificateGenerateSelfSignedCASucceeds covers the one case Generate
// allows without an issuer: a CA can self-sign.
func TestCertificateGenerateSelfSignedCASucceeds(t *testing.T) {
	r, _, _ := newTestCertificatesRouter(t)

	rec := doJSON(t, r, "POST", "/api/certificates", generateRequest{
		Name: "root-ca", Kind: "ca", CommonName: "root-ca",
		KeyAlgorithm: "ecdsa", ValidDays: 30,
	})
	if rec.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var c certs.Certificate
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode generate response: %v", err)
	}
	if c.ID == "" || c.Kind != certs.KindCA || c.CertPEM == "" || c.IssuerID != "" {
		t.Fatalf("expected a self-signed CA certificate, got %+v", c)
	}
}

func TestCertificateGenerateServerCertSignedByExistingCA(t *testing.T) {
	r, certStore, _ := newTestCertificatesRouter(t)
	ca := mustGenerateCert(t, certStore, "signing-ca")

	rec := doJSON(t, r, "POST", "/api/certificates", generateRequest{
		Name: "leaf-server", Kind: "server", CommonName: "example.com",
		KeyAlgorithm: "ecdsa", ValidDays: 30, IssuerID: ca.ID,
	})
	if rec.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var c certs.Certificate
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode generate response: %v", err)
	}
	if c.Kind != certs.KindServer || c.IssuerID != ca.ID || c.CertPEM == "" {
		t.Fatalf("expected a server cert issued by %q, got %+v", ca.ID, c)
	}
}

func TestCertificateGenerateClientCertSignedByExistingCA(t *testing.T) {
	r, certStore, _ := newTestCertificatesRouter(t)
	ca := mustGenerateCert(t, certStore, "signing-ca")

	rec := doJSON(t, r, "POST", "/api/certificates", generateRequest{
		Name: "leaf-client", Kind: "client", CommonName: "client-1",
		KeyAlgorithm: "ecdsa", ValidDays: 30, IssuerID: ca.ID,
	})
	if rec.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var c certs.Certificate
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode generate response: %v", err)
	}
	if c.Kind != certs.KindClient || c.IssuerID != ca.ID {
		t.Fatalf("expected a client cert issued by %q, got %+v", ca.ID, c)
	}
}

// TestCertificateGenerateServerCertWithoutIssuerFails guards
// certs.Generate's rule that only a CA may be self-signed — a server cert
// with no issuerId at all must be rejected, not silently self-signed.
func TestCertificateGenerateServerCertWithoutIssuerFails(t *testing.T) {
	r, _, _ := newTestCertificatesRouter(t)

	rec := doJSON(t, r, "POST", "/api/certificates", generateRequest{
		Name: "orphan-server", Kind: "server", CommonName: "example.com",
		KeyAlgorithm: "ecdsa", ValidDays: 30,
	})
	if rec.Code != 400 {
		t.Fatalf("expected 400 for a server cert with no issuer, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCertificateGenerateWithInvalidIssuerIDFails covers the handler-level
// lookup failure (h.store.Get(req.IssuerID)), distinct from the
// Generate-level "no issuer at all" case above.
func TestCertificateGenerateWithInvalidIssuerIDFails(t *testing.T) {
	r, _, _ := newTestCertificatesRouter(t)

	rec := doJSON(t, r, "POST", "/api/certificates", generateRequest{
		Name: "leaf-server", Kind: "server", CommonName: "example.com",
		KeyAlgorithm: "ecdsa", ValidDays: 30, IssuerID: "does-not-exist",
	})
	if rec.Code != 400 {
		t.Fatalf("expected 400 for an invalid issuerId, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCertificateGenerateUnknownKindFails hits Generate's switch-default
// branch directly: a valid CA issuer is supplied (so the earlier
// "must be signed by an issuer" and "issuer is not a CA" checks both pass)
// and only the unrecognized Kind itself trips the failure.
func TestCertificateGenerateUnknownKindFails(t *testing.T) {
	r, certStore, _ := newTestCertificatesRouter(t)
	ca := mustGenerateCert(t, certStore, "signing-ca")

	rec := doJSON(t, r, "POST", "/api/certificates", generateRequest{
		Name: "bogus-cert", Kind: "bogus", CommonName: "example.com",
		KeyAlgorithm: "ecdsa", ValidDays: 30, IssuerID: ca.ID,
	})
	if rec.Code != 400 {
		t.Fatalf("expected 400 for an unknown certificate kind, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCertificateGetReturnsFullCertificateWithoutPrivateKey(t *testing.T) {
	r, certStore, _ := newTestCertificatesRouter(t)
	cert := mustGenerateCert(t, certStore, "get-me")

	rec := doJSON(t, r, "GET", "/api/certificates/"+cert.ID, nil)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "PRIVATE KEY") {
		t.Fatalf("get response must never expose the private key PEM, got %s", rec.Body.String())
	}
	var c certs.Certificate
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if c.ID != cert.ID || c.CertPEM == "" {
		t.Fatalf("expected the full certificate for %q, got %+v", cert.ID, c)
	}
}

func TestCertificateGetNotFound(t *testing.T) {
	r, _, _ := newTestCertificatesRouter(t)

	rec := doJSON(t, r, "GET", "/api/certificates/does-not-exist", nil)
	if rec.Code != 404 {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCertificateDownloadReturnsCertAndKeyPEM guards download's documented
// purpose as "the one place the private key ever leaves the store" — unlike
// get/list, keyPem must actually be present here.
func TestCertificateDownloadReturnsCertAndKeyPEM(t *testing.T) {
	r, certStore, _ := newTestCertificatesRouter(t)
	cert := mustGenerateCert(t, certStore, "download-me")

	rec := doJSON(t, r, "GET", "/api/certificates/"+cert.ID+"/download", nil)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode download response: %v", err)
	}
	if body["certPem"] == "" {
		t.Fatalf("expected a non-empty certPem, got %+v", body)
	}
	if body["keyPem"] == "" || !strings.Contains(body["keyPem"], "PRIVATE KEY") {
		t.Fatalf("expected download (unlike get/list) to expose the private key PEM, got %+v", body)
	}
}

func TestCertificateDownloadNotFound(t *testing.T) {
	r, _, _ := newTestCertificatesRouter(t)

	rec := doJSON(t, r, "GET", "/api/certificates/does-not-exist/download", nil)
	if rec.Code != 404 {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCertificateRenewKeepsIDAndExtendsValidity guards renew's documented
// contract: same id (so existing certId/clientCaId references keep
// pointing at something valid), same CreatedAt, but a fresh validity
// window sized off the requested ValidDays.
func TestCertificateRenewKeepsIDAndExtendsValidity(t *testing.T) {
	r, certStore, _ := newTestCertificatesRouter(t)
	cert := mustGenerateCert(t, certStore, "renew-me") // ValidDays: 30 baked into mustGenerateCert

	rec := doJSON(t, r, "POST", "/api/certificates/"+cert.ID+"/renew", renewRequest{ValidDays: 90})
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var renewed certs.Certificate
	if err := json.Unmarshal(rec.Body.Bytes(), &renewed); err != nil {
		t.Fatalf("decode renew response: %v", err)
	}
	if renewed.ID != cert.ID {
		t.Fatalf("expected renew to keep the same id, got %q want %q", renewed.ID, cert.ID)
	}
	if !renewed.CreatedAt.Equal(cert.CreatedAt) {
		t.Fatalf("expected renew to keep the original CreatedAt, got %v want %v", renewed.CreatedAt, cert.CreatedAt)
	}
	validity := renewed.NotAfter.Sub(renewed.NotBefore)
	if validity < 89*24*time.Hour || validity > 91*24*time.Hour {
		t.Fatalf("expected the renewed cert's validity window to reflect the requested 90 days, got %v", validity)
	}
	if !renewed.NotAfter.After(cert.NotAfter) {
		t.Fatalf("expected renew to extend NotAfter beyond the original %v, got %v", cert.NotAfter, renewed.NotAfter)
	}

	// the store itself must reflect the renewal, still under the same id
	stored, err := certStore.Get(cert.ID)
	if err != nil {
		t.Fatalf("store.Get after renew: %v", err)
	}
	if !stored.NotAfter.Equal(renewed.NotAfter) {
		t.Fatalf("expected the stored cert to match the renewed response, got %v want %v", stored.NotAfter, renewed.NotAfter)
	}
}

// countingFakeEngine is a no-op engine.Engine that also records every
// RegisterMock call — used to prove refreshListenersForCert actually
// re-dispatches a mock referencing the renewed certificate, rather than
// just updating the certs store and leaving an already-running listener
// (which cached the cert's PEM bytes once at registration time) none the
// wiser.
type countingFakeEngine struct {
	name          string
	registeredIDs []string
}

func (f *countingFakeEngine) Name() string { return f.name }
func (f *countingFakeEngine) Start(ctx context.Context, cfg engine.ListenerConfig) error {
	return nil
}
func (f *countingFakeEngine) Stop(ctx context.Context) error { return nil }
func (f *countingFakeEngine) RegisterMock(m *mock.Definition) error {
	f.registeredIDs = append(f.registeredIDs, m.ID)
	return nil
}
func (f *countingFakeEngine) UnregisterMock(id string) error { return nil }

// TestCertificateRenewRefreshesReferencingTCPMock is the regression test
// for a real report: renewing a certificate updated the certs store, but a
// TCP mock's already-running TLS listener (which loads its certificate
// once at RegisterMock time, see internal/engine/tcp/engine.go's wrapTLS)
// kept serving the stale pre-renewal certificate indefinitely — nothing
// ever told it to reload. refreshListenersForCert must re-dispatch every
// mock referencing the renewed cert so its listener picks up the fresh one
// immediately, not just "eventually, whenever that mock next happens to be
// edited for an unrelated reason."
func TestCertificateRenewRefreshesReferencingTCPMock(t *testing.T) {
	r, certStore, mockStore := newTestCertificatesRouter(t)
	cert := mustGenerateCert(t, certStore, "renew-refresh")

	fake := &countingFakeEngine{name: "tcp"}
	engine.Register(fake)

	def, err := mockStore.Create(&mock.Definition{
		Name: "tls-tcp-mock", ProtocolType: "tcp", Enabled: true,
		TCP: &mock.TCPConfig{Port: 19999, TLS: &mock.TCPTLSConfig{CertificateID: cert.ID}},
	})
	if err != nil {
		t.Fatalf("create TCP mock: %v", err)
	}

	rec := doJSON(t, r, "POST", "/api/certificates/"+cert.ID+"/renew", renewRequest{ValidDays: 90})
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	found := false
	for _, id := range fake.registeredIDs {
		if id == def.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the TCP mock referencing the renewed cert to be re-dispatched, registeredIDs=%v", fake.registeredIDs)
	}
}

func TestCertificateRenewNotFound(t *testing.T) {
	r, _, _ := newTestCertificatesRouter(t)

	rec := doJSON(t, r, "POST", "/api/certificates/does-not-exist/renew", renewRequest{ValidDays: 30})
	if rec.Code != 404 {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCertificateDeleteRemovesCertificate(t *testing.T) {
	r, certStore, _ := newTestCertificatesRouter(t)
	cert := mustGenerateCert(t, certStore, "delete-me")

	rec := doJSON(t, r, "DELETE", "/api/certificates/"+cert.ID, nil)
	if rec.Code != 204 {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, r, "GET", "/api/certificates/"+cert.ID, nil)
	if rec.Code != 404 {
		t.Fatalf("expected the deleted certificate to 404 afterwards, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCertificateDeleteNotFound(t *testing.T) {
	r, _, _ := newTestCertificatesRouter(t)

	rec := doJSON(t, r, "DELETE", "/api/certificates/does-not-exist", nil)
	if rec.Code != 404 {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/certs"
	"github.com/addictedabhi/airmock/internal/storage"
)

func newTestCertBundlesRouter(t *testing.T) (chi.Router, *certs.Store) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	certStore := certs.NewStore(db)
	r := chi.NewRouter()
	r.Route("/api/cert-bundles", NewCertBundlesHandler(certStore).Routes)
	return r, certStore
}

// TestGenerateBundleCreatesCAServerAndClientTogether guards the one-shot
// flow this replaces (three separate, unlinked POST /api/certificates
// calls from the browser) — one request must now produce a real, persisted
// Bundle referencing all three generated certs.
func TestGenerateBundleCreatesCAServerAndClientTogether(t *testing.T) {
	r, certStore := newTestCertBundlesRouter(t)

	rec := doJSON(t, r, http.MethodPost, "/api/cert-bundles/generate", generateBundleRequest{
		Name: "acme", WithClientCert: true, ValidDays: 30,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var b certs.Bundle
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.CAID == "" || b.ServerCertID == "" || b.ClientCertID == "" {
		t.Fatalf("expected all three cert IDs populated, got %+v", b)
	}

	ca, err := certStore.Get(b.CAID)
	if err != nil || ca.Kind != certs.KindCA {
		t.Fatalf("expected a real CA cert, got %+v, err=%v", ca, err)
	}
	server, err := certStore.Get(b.ServerCertID)
	if err != nil || server.Kind != certs.KindServer || server.IssuerID != ca.ID {
		t.Fatalf("expected a server cert issued by the bundle's CA, got %+v, err=%v", server, err)
	}
	client, err := certStore.Get(b.ClientCertID)
	if err != nil || client.Kind != certs.KindClient || client.IssuerID != ca.ID {
		t.Fatalf("expected a client cert issued by the bundle's CA, got %+v, err=%v", client, err)
	}
}

func TestGenerateBundleWithoutClientCertOmitsIt(t *testing.T) {
	r, _ := newTestCertBundlesRouter(t)

	rec := doJSON(t, r, http.MethodPost, "/api/cert-bundles/generate", generateBundleRequest{Name: "no-client"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var b certs.Bundle
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.ClientCertID != "" {
		t.Fatalf("expected no client cert when WithClientCert is false, got %+v", b)
	}
}

// TestCreateBundleFromExistingCertsValidatesKinds guards the "assemble from
// what's already in the store" path — a server cert used as the CAID (or
// any other kind mismatch) must be rejected up front, not just fail later
// when someone tries to apply the bundle.
func TestCreateBundleFromExistingCertsValidatesKinds(t *testing.T) {
	r, certStore := newTestCertBundlesRouter(t)
	ca := mustGenerateCert(t, certStore, "existing-ca")
	notACA := mustGenerateCert(t, certStore, "not-a-ca")
	notACA.Kind = certs.KindServer // mustGenerateCert always makes a CA; force a mismatch
	certStore.Update(notACA)

	rec := doJSON(t, r, http.MethodPost, "/api/cert-bundles/", bundleRequest{Name: "b1", CAID: notACA.ID})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-CA CAID, got %d: %s", rec.Code, rec.Body.String())
	}

	okRec := doJSON(t, r, http.MethodPost, "/api/cert-bundles/", bundleRequest{Name: "b2", CAID: ca.ID})
	if okRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for a valid CA-only bundle, got %d: %s", okRec.Code, okRec.Body.String())
	}
}

// TestBundleNameMustBeUnique uses the from-existing-certs `create` path
// (not `generate`) specifically because two different underlying CA certs
// can share a bundle name attempt without colliding on cert names first —
// isolating the bundle-name uniqueness check itself from constituent-cert
// naming collisions (generate's own deterministic "{name}-ca" naming means
// re-using a bundle name there fails on the cert Save before the bundle
// name check is ever reached, which is fine, just a different code path).
func TestBundleNameMustBeUnique(t *testing.T) {
	r, certStore := newTestCertBundlesRouter(t)
	caA := mustGenerateCert(t, certStore, "dupe-ca-a")
	caB := mustGenerateCert(t, certStore, "dupe-ca-b")

	rec := doJSON(t, r, http.MethodPost, "/api/cert-bundles/", bundleRequest{Name: "dupe", CAID: caA.ID})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected first create to succeed, got %d: %s", rec.Code, rec.Body.String())
	}

	rec2 := doJSON(t, r, http.MethodPost, "/api/cert-bundles/", bundleRequest{Name: "dupe", CAID: caB.ID})
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a duplicate bundle name, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestDeleteBundleThenList(t *testing.T) {
	r, _ := newTestCertBundlesRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/cert-bundles/generate", generateBundleRequest{Name: "to-delete"})
	var b certs.Bundle
	json.Unmarshal(rec.Body.Bytes(), &b)

	delRec := doJSON(t, r, http.MethodDelete, "/api/cert-bundles/"+b.ID, nil)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", delRec.Code, delRec.Body.String())
	}

	listRec := doJSON(t, r, http.MethodGet, "/api/cert-bundles/", nil)
	var list []*certs.Bundle
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected the bundle list empty after delete, got %+v", list)
	}
}

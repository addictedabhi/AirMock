package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/certs"
)

type CertBundlesHandler struct {
	store *certs.Store
}

func NewCertBundlesHandler(store *certs.Store) *CertBundlesHandler {
	return &CertBundlesHandler{store: store}
}

func (h *CertBundlesHandler) Routes(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.create)
	r.Post("/generate", h.generate)
	r.Put("/{id}", h.update)
	r.Delete("/{id}", h.delete)
}

func (h *CertBundlesHandler) list(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.ListBundles()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type bundleRequest struct {
	Name         string `json:"name"`
	CAID         string `json:"caId"`
	ServerCertID string `json:"serverCertId"`
	ClientCertID string `json:"clientCertId"`
}

// create groups already-existing certificates into a named bundle — the
// "pick from what's already in the store" path, as opposed to generate
// below, which makes brand-new certs for the bundle in one action.
func (h *CertBundlesHandler) create(w http.ResponseWriter, r *http.Request) {
	var req bundleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	if err := h.validateBundleRefs(req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	b, err := h.store.CreateBundle(&certs.Bundle{
		Name: req.Name, CAID: req.CAID, ServerCertID: req.ServerCertID, ClientCertID: req.ClientCertID,
	})
	if errors.Is(err, certs.ErrDuplicateBundleName) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (h *CertBundlesHandler) update(w http.ResponseWriter, r *http.Request) {
	var req bundleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := h.validateBundleRefs(req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	b := &certs.Bundle{ID: chi.URLParam(r, "id"), Name: req.Name, CAID: req.CAID, ServerCertID: req.ServerCertID, ClientCertID: req.ClientCertID}
	updated, err := h.store.UpdateBundle(b)
	if errors.Is(err, certs.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, certs.ErrDuplicateBundleName) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// validateBundleRefs confirms each referenced cert exists and is the right
// Kind for its role — the same guard ApplyGatewayTLS already applies at
// TLS-apply time, just surfaced earlier (at bundle-authoring time) so a
// typo'd or wrong-kind cert ID is rejected immediately rather than only
// once someone tries to actually apply the bundle to a mock.
func (h *CertBundlesHandler) validateBundleRefs(req bundleRequest) error {
	if req.CAID == "" {
		return errors.New("caId is required")
	}
	if err := h.requireKind(req.CAID, certs.KindCA); err != nil {
		return err
	}
	if req.ServerCertID != "" {
		if err := h.requireKind(req.ServerCertID, certs.KindServer); err != nil {
			return err
		}
	}
	if req.ClientCertID != "" {
		if err := h.requireKind(req.ClientCertID, certs.KindClient); err != nil {
			return err
		}
	}
	return nil
}

func (h *CertBundlesHandler) requireKind(id string, kind certs.Kind) error {
	c, err := h.store.Get(id)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", id, err)
	}
	if c.Kind != kind {
		return fmt.Errorf("certificate %q is not a %s certificate", c.Name, kind)
	}
	return nil
}

func (h *CertBundlesHandler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.DeleteBundle(chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, certs.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type generateBundleRequest struct {
	Name         string   `json:"name"`
	KeyAlgorithm string   `json:"keyAlgorithm"`
	ValidDays    int      `json:"validDays"`
	CommonName   string   `json:"commonName"`
	SANs         []string `json:"sans"`
	// WithClientCert, when true, also generates a client cert signed by the
	// new CA (for mTLS testing) — off lets a caller generate a CA+server-only
	// bundle when no client-cert verification is planned.
	WithClientCert bool `json:"withClientCert"`
}

// generate is the one-shot "make me a CA + server cert (+ optional client
// cert) and group them as a named bundle" flow — replaces what the UI
// previously did as three separate, unlinked POST /api/certificates calls
// (see Certificates.svelte's old generateBundle), now atomic and actually
// persisted as a Bundle instead of three certs related only by naming
// convention.
func (h *CertBundlesHandler) generate(w http.ResponseWriter, r *http.Request) {
	var req generateBundleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	keyAlgo := certs.KeyAlgorithm(req.KeyAlgorithm)
	if keyAlgo == "" {
		keyAlgo = certs.KeyECDSA
	}
	validDays := req.ValidDays
	if validDays <= 0 {
		validDays = 365
	}

	serverCN := req.CommonName
	if serverCN == "" {
		serverCN = req.Name
	}

	ca, err := h.generateAndSave(req.Name+"-ca", certs.KindCA, req.Name+" Root CA", nil, nil, keyAlgo, validDays)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	server, err := h.generateAndSave(req.Name+"-server", certs.KindServer, serverCN, req.SANs, ca, keyAlgo, validDays)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	b := &certs.Bundle{Name: req.Name, CAID: ca.ID, ServerCertID: server.ID}
	if req.WithClientCert {
		client, err := h.generateAndSave(req.Name+"-client", certs.KindClient, req.Name+" client", nil, ca, keyAlgo, validDays)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		b.ClientCertID = client.ID
	}

	created, err := h.store.CreateBundle(b)
	if errors.Is(err, certs.ErrDuplicateBundleName) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *CertBundlesHandler) generateAndSave(name string, kind certs.Kind, commonName string, sans []string, issuer *certs.Certificate, keyAlgo certs.KeyAlgorithm, validDays int) (*certs.Certificate, error) {
	c, err := certs.Generate(certs.GenerateRequest{
		Name: name, Kind: kind, CommonName: commonName, SANs: sans, KeyAlgorithm: keyAlgo, ValidDays: validDays, Issuer: issuer,
	})
	if err != nil {
		return nil, fmt.Errorf("generate %s: %w", kind, err)
	}
	if err := h.store.Save(c); err != nil {
		return nil, fmt.Errorf("save %s: %w", kind, err)
	}
	return c, nil
}

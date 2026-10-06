package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/certs"
	"github.com/addictedabhi/airmock/internal/engine"
	httpengine "github.com/addictedabhi/airmock/internal/engine/http"
	"github.com/addictedabhi/airmock/internal/mock"
)

type CertificatesHandler struct {
	store      *certs.Store
	mockStore  *mock.Store
	httpEngine *httpengine.Engine
	tlsAddr    string
}

// httpEngine/tlsAddr are optional (nil/"" is fine, e.g. in tests that don't
// exercise renewal) — only needed for refreshListenersForCert to re-apply
// the shared gateway's TLS settings after a renewal, mirroring
// NewGatewayHandler's own dependencies since ApplyGatewayTLS is shared
// between them.
func NewCertificatesHandler(store *certs.Store, mockStore *mock.Store, httpEngine *httpengine.Engine, tlsAddr string) *CertificatesHandler {
	return &CertificatesHandler{store: store, mockStore: mockStore, httpEngine: httpEngine, tlsAddr: tlsAddr}
}

func (h *CertificatesHandler) Routes(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.generate)
	r.Post("/import", h.importCert)
	r.Get("/{id}", h.get)
	r.Get("/{id}/download", h.download)
	r.Get("/{id}/usage", h.usage)
	r.Post("/{id}/renew", h.renew)
	r.Delete("/{id}", h.delete)
}

type generateRequest struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	CommonName   string   `json:"commonName"`
	SANs         []string `json:"sans"`
	KeyAlgorithm string   `json:"keyAlgorithm"`
	ValidDays    int      `json:"validDays"`
	IssuerID     string   `json:"issuerId"`
}

func (h *CertificatesHandler) list(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *CertificatesHandler) generate(w http.ResponseWriter, r *http.Request) {
	var req generateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	genReq := certs.GenerateRequest{
		Name:         req.Name,
		Kind:         certs.Kind(req.Kind),
		CommonName:   req.CommonName,
		SANs:         req.SANs,
		KeyAlgorithm: certs.KeyAlgorithm(req.KeyAlgorithm),
		ValidDays:    req.ValidDays,
	}
	if req.IssuerID != "" {
		issuer, err := h.store.Get(req.IssuerID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		genReq.Issuer = issuer
	}

	cert, err := certs.Generate(genReq)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := h.store.Save(cert); err != nil {
		if errors.Is(err, certs.ErrDuplicateName) {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, cert)
}

// maxImportUploadSize caps a single cert-import request — generous for even
// a PKCS#12 bundle carrying a full chain, small enough that one request
// can't be used to exhaust memory.
const maxImportUploadSize = 10 << 20 // 10 MiB

// importCert accepts externally-issued certificate material — a PEM/DER
// cert (optionally with a fullchain of intermediates), a matching PEM/DER
// private key (optionally passphrase-encrypted), or a PKCS#12/.pfx bundle —
// as a multipart form so both file uploads and pasted PEM text work through
// the same endpoint. Each field can arrive either as an uploaded file
// (certFile/keyFile/p12File) or as raw pasted text (certPem/keyPem).
func (h *CertificatesHandler) importCert(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxImportUploadSize); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	req := certs.ImportRequest{
		Name:       r.FormValue("name"),
		Kind:       certs.Kind(r.FormValue("kind")),
		Passphrase: r.FormValue("passphrase"),
	}

	var err error
	if req.P12Data, err = readImportField(r, "p12File", ""); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(req.P12Data) == 0 {
		if req.CertData, err = readImportField(r, "certFile", "certPem"); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if req.KeyData, err = readImportField(r, "keyFile", "keyPem"); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}

	cert, err := certs.Import(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := h.store.Save(cert); err != nil {
		if errors.Is(err, certs.ErrDuplicateName) {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, cert)
}

// readImportField reads an uploaded file field if present, falling back to
// a plain pasted-text form field of the same purpose (e.g. "certFile" or
// "certPem" — a paste box and a file picker for the same piece of material).
func readImportField(r *http.Request, fileField, textField string) ([]byte, error) {
	if f, _, err := r.FormFile(fileField); err == nil {
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			return nil, err
		}
		return data, nil
	}
	if textField != "" {
		if v := r.FormValue(textField); v != "" {
			return []byte(v), nil
		}
	}
	return nil, nil
}

func (h *CertificatesHandler) get(w http.ResponseWriter, r *http.Request) {
	c, err := h.store.Get(chi.URLParam(r, "id"))
	if errors.Is(err, certs.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// download returns the cert+key PEM together — the one place the private
// key ever leaves the store, used to configure an external client for mTLS.
func (h *CertificatesHandler) download(w http.ResponseWriter, r *http.Request) {
	c, err := h.store.Get(chi.URLParam(r, "id"))
	if errors.Is(err, certs.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"certPem": c.CertPEM,
		"keyPem":  c.KeyPEM,
	})
}

type certUsage struct {
	GatewayServer   bool       `json:"gatewayServer"`
	GatewayClientCA bool       `json:"gatewayClientCa"`
	TCPMocks        []usageRef `json:"tcpMocks"`
	SMTPMocks       []usageRef `json:"smtpMocks"`
	Projects        []usageRef `json:"projects"`
	Bundles         []usageRef `json:"bundles"`
	IssuedCerts     []usageRef `json:"issuedCerts"`
}

type usageRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// tlsConfigReferences reports whether cfg binds id in either TLS role — the
// presented server/leaf certificate or the client-verify CA (only relevant
// when ClientCertMode is optional/required) — resolved through cfg's
// BundleID first, since a bundle's own server/CA certs take precedence over
// any directly-set CertificateID/ClientCAID (see resolveBundleTLSRefs). A
// cert used purely as a ClientCAID, or only reachable via a bundle, is
// still surfaced as "in use" rather than only certs bound as the
// listener's own presented cert.
func tlsConfigReferences(store *certs.Store, cfg *mock.TCPTLSConfig, id string) bool {
	certID, clientCAID, err := resolveBundleTLSRefs(store, cfg.BundleID, cfg.CertificateID, cfg.ClientCAID)
	if err != nil {
		// Bundle no longer resolves (e.g. deleted) — fall back to the raw
		// stored fields so usage still reflects what's on record rather than
		// silently reporting "not in use".
		certID, clientCAID = cfg.CertificateID, cfg.ClientCAID
	}
	mode := certs.ClientCertMode(cfg.ClientCertMode)
	requiresClientCA := mode == certs.ClientCertOptional || mode == certs.ClientCertRequired
	return certID == id || (requiresClientCA && clientCAID == id)
}

// usage reports every place a certificate is actually referenced from, so
// the UI can show a concrete "this will break X, Y, Z" warning before
// deleting it instead of a generic "might break something" disclaimer.
func (h *CertificatesHandler) usage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u := certUsage{TCPMocks: []usageRef{}, SMTPMocks: []usageRef{}, Projects: []usageRef{}, Bundles: []usageRef{}, IssuedCerts: []usageRef{}}

	h.usageFromGateway(&u, id)
	h.usageFromMocksAndProjects(&u, id)
	h.usageFromBundles(&u, id)
	h.usageFromIssuedCerts(&u, id)

	writeJSON(w, http.StatusOK, u)
}

func (h *CertificatesHandler) usageFromGateway(u *certUsage, id string) {
	gs, err := h.store.GetGatewaySettings()
	if err != nil {
		return
	}
	certID, clientCAID, err := resolveBundleTLSRefs(h.store, gs.BundleID, gs.CertID, gs.ClientCAID)
	if err != nil {
		certID, clientCAID = gs.CertID, gs.ClientCAID
	}
	u.GatewayServer = certID == id
	u.GatewayClientCA = (gs.ClientCertMode == certs.ClientCertOptional || gs.ClientCertMode == certs.ClientCertRequired) && clientCAID == id
}

// usageFromMocksAndProjects checks BOTH the presented server/leaf cert and
// the CA an mTLS-enabled mock/project verifies incoming client certs
// against (via ClientCertMode) — a cert referenced only as a ClientCAID
// used to report zero usage here, so deleting it showed no warning at all
// even though the referencing mock's listener would fail to bind on its
// very next RegisterMock (see internal/engine/tcp/engine.go's
// clientCAPool) once the CA no longer resolves. Project.TLS was previously
// never checked here at all, so a project using this cert as its
// dedicated-port server cert (or CA) reported zero usage — same blind spot.
func (h *CertificatesHandler) usageFromMocksAndProjects(u *certUsage, id string) {
	if h.mockStore == nil {
		return
	}
	h.usageFromMocks(u, id)
	h.usageFromProjects(u, id)
}

func (h *CertificatesHandler) usageFromMocks(u *certUsage, id string) {
	defs, err := h.mockStore.List()
	if err != nil {
		return
	}
	for _, d := range defs {
		if d.TCP != nil && d.TCP.TLS != nil && tlsConfigReferences(h.store, d.TCP.TLS, id) {
			u.TCPMocks = append(u.TCPMocks, usageRef{ID: d.ID, Name: d.Name})
		}
		if d.SMTP != nil && d.SMTP.TLS != nil && tlsConfigReferences(h.store, d.SMTP.TLS, id) {
			u.SMTPMocks = append(u.SMTPMocks, usageRef{ID: d.ID, Name: d.Name})
		}
	}
}

func (h *CertificatesHandler) usageFromProjects(u *certUsage, id string) {
	projects, err := h.mockStore.ListProjects()
	if err != nil {
		return
	}
	for _, p := range projects {
		if p.TLS != nil && tlsConfigReferences(h.store, p.TLS, id) {
			u.Projects = append(u.Projects, usageRef{ID: p.ID, Name: p.Name})
		}
	}
}

func (h *CertificatesHandler) usageFromBundles(u *certUsage, id string) {
	bundles, err := h.store.ListBundles()
	if err != nil {
		return
	}
	for _, b := range bundles {
		if b.CAID == id || b.ServerCertID == id || b.ClientCertID == id {
			u.Bundles = append(u.Bundles, usageRef{ID: b.ID, Name: b.Name})
		}
	}
}

func (h *CertificatesHandler) usageFromIssuedCerts(u *certUsage, id string) {
	all, err := h.store.List()
	if err != nil {
		return
	}
	for _, c := range all {
		if c.IssuerID == id {
			u.IssuedCerts = append(u.IssuedCerts, usageRef{ID: c.ID, Name: c.Name})
		}
	}
}

type renewRequest struct {
	ValidDays int `json:"validDays"`
}

// renew regenerates a certificate's key/cert material in place — same
// name/kind/commonName/SANs/issuer, a fresh key and validity window — and
// keeps the same id, so every existing certId/clientCaId reference (a
// gateway TLS binding, a TCP mock's own listener cert) keeps pointing at
// something valid without needing to be manually rebound.
func (h *CertificatesHandler) renew(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := h.store.Get(id)
	if errors.Is(err, certs.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	var req renewRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // optional body: 0/missing just falls back to 365 below

	genReq := certs.GenerateRequest{
		Name:         existing.Name,
		Kind:         existing.Kind,
		CommonName:   existing.CommonName,
		SANs:         existing.SANs,
		KeyAlgorithm: existing.KeyAlgorithm,
		ValidDays:    req.ValidDays,
	}
	if existing.IssuerID != "" {
		issuer, err := h.store.Get(existing.IssuerID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("issuer CA no longer exists"))
			return
		}
		genReq.Issuer = issuer
	}

	renewed, err := certs.Generate(genReq)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	renewed.ID = existing.ID
	renewed.IssuerID = existing.IssuerID
	renewed.CreatedAt = existing.CreatedAt

	if err := h.store.Update(renewed); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Best-effort: the renewal itself already succeeded and is persisted
	// above regardless of what happens here. Without this, a renewed
	// certificate keeping the "same id" (see this handler's own doc
	// comment) only means the STORED config references still resolve — an
	// already-running listener that read this cert's PEM bytes once at
	// startup/registration (the gateway's cached TLSSettings, a TCP/SMTP
	// mock's own wrapTLS, a project's dedicated-port listener) keeps
	// serving the stale pre-renewal certificate indefinitely, since
	// nothing else ever tells it to re-read the store.
	h.refreshListenersForCert(r.Context(), renewed.ID)
	writeJSON(w, http.StatusOK, renewed)
}

// refreshListenersForCert re-applies a just-renewed certificate to every
// live listener that references it, reusing the exact same "who uses this
// cert" computation the /usage endpoint already does (so a project reached
// only through a bundle, or a cert used purely as a client-verify CA, is
// covered here too, not just a direct server-cert binding). Best-effort:
// logged, never surfaced to the renew response — the renewal itself already
// succeeded, and a listener this misses keeps serving the stale cert only
// until its next natural RegisterMock (e.g. the mock is next edited), not
// forever.
func (h *CertificatesHandler) refreshListenersForCert(ctx context.Context, id string) {
	u := certUsage{TCPMocks: []usageRef{}, SMTPMocks: []usageRef{}, Projects: []usageRef{}, Bundles: []usageRef{}, IssuedCerts: []usageRef{}}
	h.usageFromGateway(&u, id)
	h.usageFromMocksAndProjects(&u, id)

	if u.GatewayServer || u.GatewayClientCA {
		h.refreshGatewayTLS(ctx, id)
	}
	h.refreshMockListeners(id, append(append([]usageRef{}, u.TCPMocks...), u.SMTPMocks...))
	h.refreshProjectListeners(id, u.Projects)
}

func (h *CertificatesHandler) refreshGatewayTLS(ctx context.Context, id string) {
	if h.httpEngine == nil {
		return
	}
	gs, err := h.store.GetGatewaySettings()
	if err != nil {
		return
	}
	if err := ApplyGatewayTLS(ctx, h.store, h.httpEngine, h.tlsAddr, gs); err != nil {
		log.Printf("airmock: renewed certificate %q but failed to re-apply gateway TLS: %v", id, err)
	}
}

func (h *CertificatesHandler) refreshMockListeners(id string, refs []usageRef) {
	for _, ref := range refs {
		def, err := h.mockStore.Get(ref.ID)
		if err != nil {
			continue
		}
		if err := engine.Dispatch(def); err != nil {
			log.Printf("airmock: renewed certificate %q but failed to refresh mock %q (%s): %v", id, def.Name, def.ID, err)
		}
	}
}

// A project's dedicated-port listener is rebuilt from ALL of that project's
// currently-registered mocks as a side effect of RegisterMock (see
// httpengine.Engine.rebuild) — re-dispatching just one of them is enough to
// resync the whole port, so this stops at the first match per project
// rather than dispatching every mock in it.
func (h *CertificatesHandler) refreshProjectListeners(id string, refs []usageRef) {
	if len(refs) == 0 {
		return
	}
	defs, err := h.mockStore.List()
	if err != nil {
		return
	}
	for _, ref := range refs {
		def := firstEnabledMockForProject(defs, ref.ID)
		if def == nil {
			continue
		}
		if err := engine.Dispatch(def); err != nil {
			log.Printf("airmock: renewed certificate %q but failed to refresh project %q (%s): %v", id, def.Name, ref.ID, err)
		}
	}
}

func firstEnabledMockForProject(defs []*mock.Definition, projectID string) *mock.Definition {
	for _, def := range defs {
		if def.ProjectID == projectID && def.Enabled {
			return def
		}
	}
	return nil
}

func (h *CertificatesHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.store.Delete(id); err != nil {
		if errors.Is(err, certs.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

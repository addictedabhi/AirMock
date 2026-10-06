package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/addictedabhi/airmock/internal/apiclient"
	"github.com/addictedabhi/airmock/internal/certs"
	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/scheduledevent"
	"github.com/addictedabhi/airmock/internal/smtp"
)

const backupFormatVersion = 1

// BackupHandler exports/imports everything that makes up a "setup" as one
// JSON file: certificates + bundles, mock projects + mocks, email
// templates, and the API client's workspaces/environments/collections, plus
// scheduled events. Deliberately NOT included: certs.GatewaySettings and
// smtp.Settings (both single, instance-wide runtime toggles rather than
// "content" — merging them has no sensible meaning, and blindly overwriting
// a working gateway TLS/SMTP relay config on import is exactly the kind of
// surprise a backup restore shouldn't spring) and internal/settings.Settings
// (redacted-header list, retention policy — same reasoning).
type BackupHandler struct {
	certStore      *certs.Store
	mockStore      *mock.Store
	apiClientStore *apiclient.Store
	smtpStore      *smtp.Store
	eventStore     *scheduledevent.Store
}

func NewBackupHandler(certStore *certs.Store, mockStore *mock.Store, apiClientStore *apiclient.Store, smtpStore *smtp.Store, eventStore *scheduledevent.Store) *BackupHandler {
	return &BackupHandler{certStore: certStore, mockStore: mockStore, apiClientStore: apiClientStore, smtpStore: smtpStore, eventStore: eventStore}
}

func (h *BackupHandler) Routes(r chi.Router) {
	r.Get("/export", h.export)
	r.Post("/import", h.doImport)
}

// backupCertificate re-includes KeyPEM (certs.Certificate marks it
// `json:"-"` everywhere else — the one place the key ever leaves the store
// is the single-cert /download endpoint) since a backup that can't actually
// restore a working certificate isn't a backup.
type backupCertificate struct {
	certs.Certificate
	KeyPEM string `json:"keyPem"`
}

type backupFile struct {
	Version         int                      `json:"version"`
	ExportedAt      time.Time                `json:"exportedAt"`
	Certificates    []backupCertificate      `json:"certificates"`
	CertBundles     []*certs.Bundle          `json:"certBundles"`
	MockProjects    []*mock.Project          `json:"mockProjects"`
	Mocks           []*mock.Definition       `json:"mocks"`
	EmailTemplates  []*smtp.Template         `json:"emailTemplates"`
	Workspaces      []*apiclient.Workspace   `json:"workspaces"`
	Environments    []*apiclient.Environment `json:"environments"`
	Collections     []*apiclient.Collection  `json:"collections"`
	ScheduledEvents []*scheduledevent.Event  `json:"scheduledEvents"`
}

func (h *BackupHandler) export(w http.ResponseWriter, r *http.Request) {
	out := backupFile{Version: backupFormatVersion, ExportedAt: time.Now().UTC()}

	rawCerts, err := h.certStore.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("list certificates: %w", err))
		return
	}
	for _, c := range rawCerts {
		out.Certificates = append(out.Certificates, backupCertificate{Certificate: *c, KeyPEM: c.KeyPEM})
	}

	if out.CertBundles, err = h.certStore.ListBundles(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("list cert bundles: %w", err))
		return
	}
	if out.MockProjects, err = h.mockStore.ListProjects(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("list mock projects: %w", err))
		return
	}
	if out.Mocks, err = h.mockStore.List(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("list mocks: %w", err))
		return
	}
	if out.EmailTemplates, err = h.smtpStore.ListTemplates(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("list email templates: %w", err))
		return
	}
	if out.Workspaces, err = h.apiClientStore.ListWorkspaces(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("list workspaces: %w", err))
		return
	}
	for _, ws := range out.Workspaces {
		envs, err := h.apiClientStore.ListEnvironments(ws.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("list environments: %w", err))
			return
		}
		out.Environments = append(out.Environments, envs...)
		cols, err := h.apiClientStore.ListCollections(ws.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("list collections: %w", err))
			return
		}
		out.Collections = append(out.Collections, cols...)
	}
	if out.ScheduledEvents, err = h.eventStore.List(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("list scheduled events: %w", err))
		return
	}

	w.Header().Set("Content-Disposition", `attachment; filename="airmock-backup.json"`)
	writeJSON(w, http.StatusOK, out)
}

type importSummary struct {
	Imported []string     `json:"imported"`
	Skipped  []importSkip `json:"skipped"`
}

func newImportSummary() importSummary {
	return importSummary{Imported: []string{}, Skipped: []importSkip{}}
}

func (s *importSummary) ok(label string) { s.Imported = append(s.Imported, label) }
func (s *importSummary) skip(label, reason string) {
	s.Skipped = append(s.Skipped, importSkip{Name: label, Reason: reason})
}

// doImport merges every entity in the uploaded file into this instance
// (see NewBackupHandler's doc comment for what's deliberately excluded).
// Every entity always gets a brand-new ID — reusing an id from the file
// would either collide with something already here or silently impersonate
// it on another instance, neither of which "import" should mean — and a
// name collision is resolved by suffixing rather than skipping (the choice
// this feature made: a merge-import should still land the content, just
// not under a name that's already taken), falling back to skip-with-reason
// only when suffixing can't help (e.g. a REST mock's method+path already
// claimed by something else). Failing partway through is not fatal to the
// rest of the batch — every entity gets its own summary entry.
func (h *BackupHandler) doImport(w http.ResponseWriter, r *http.Request) {
	var in backupFile
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	result := struct {
		Certificates    importSummary `json:"certificates"`
		CertBundles     importSummary `json:"certBundles"`
		MockProjects    importSummary `json:"mockProjects"`
		Mocks           importSummary `json:"mocks"`
		EmailTemplates  importSummary `json:"emailTemplates"`
		Workspaces      importSummary `json:"workspaces"`
		Environments    importSummary `json:"environments"`
		Collections     importSummary `json:"collections"`
		ScheduledEvents importSummary `json:"scheduledEvents"`
	}{
		Certificates: newImportSummary(), CertBundles: newImportSummary(), MockProjects: newImportSummary(),
		Mocks: newImportSummary(), EmailTemplates: newImportSummary(), Workspaces: newImportSummary(),
		Environments: newImportSummary(), Collections: newImportSummary(), ScheduledEvents: newImportSummary(),
	}

	certIDMap := h.importCertificates(in.Certificates, &result.Certificates)
	bundleIDMap := h.importBundles(in.CertBundles, certIDMap, &result.CertBundles)
	templateIDMap := h.importEmailTemplates(in.EmailTemplates, &result.EmailTemplates)
	projectIDMap := h.importMockProjects(in.MockProjects, certIDMap, bundleIDMap, &result.MockProjects)
	h.importMocks(in.Mocks, projectIDMap, certIDMap, bundleIDMap, templateIDMap, &result.Mocks)

	workspaceIDMap := h.importWorkspaces(in.Workspaces, &result.Workspaces)
	h.importEnvironments(in.Environments, workspaceIDMap, &result.Environments)
	h.importCollections(in.Collections, workspaceIDMap, &result.Collections)
	h.importScheduledEvents(in.ScheduledEvents, &result.ScheduledEvents)

	writeJSON(w, http.StatusOK, result)
}

// uniqueSuffix tries base, then "base (imported)", "base (imported 2)", ...
// up to a small cap, calling attempt(name) until it stops reporting a
// name-collision. attempt returns (isDuplicateNameError, otherErr) so a
// non-name failure (a validation error, an unresolvable reference) surfaces
// immediately instead of being retried pointlessly under different names.
func uniqueSuffix(base string, attempt func(name string) (isDuplicate bool, err error)) (string, error) {
	name := base
	for i := 0; i < 20; i++ {
		isDup, err := attempt(name)
		if !isDup {
			return name, err
		}
		if i == 0 {
			name = base + " (imported)"
		} else {
			name = fmt.Sprintf("%s (imported %d)", base, i+1)
		}
	}
	return "", fmt.Errorf("could not find a free name for %q after 20 attempts", base)
}

// pendingIssuer is a newly-imported cert's own new id, paired with its
// ORIGINAL (pre-import) IssuerID — resolved against idMap in the second
// pass below, once every cert in the batch has a new id assigned.
type pendingIssuer struct {
	newID            string
	originalIssuerID string
}

func (h *BackupHandler) importCertificates(in []backupCertificate, sum *importSummary) map[string]string {
	idMap := map[string]string{}
	// Two passes: IssuerID references another cert in THIS SAME batch, whose
	// new id isn't known until it's been created — insert every cert first
	// with IssuerID cleared, then patch it in once idMap is complete.
	pending := make([]pendingIssuer, 0, len(in))
	for _, bc := range in {
		oldID := bc.ID
		c := bc.Certificate
		// Unlike mock.Store.Create/CreateBundle/CreateProject, certs.Store.Save
		// does NOT auto-generate an id when empty — Generate() is normally
		// what assigns one before Save is ever called. Since import doesn't
		// go through Generate (there's no crypto to (re)generate, just PEM
		// material to insert as-is), a fresh id has to be assigned here.
		c.ID = uuid.NewString()
		issuerWas := c.IssuerID
		c.IssuerID = ""
		c.KeyPEM = bc.KeyPEM

		var created *certs.Certificate
		name, err := uniqueSuffix(c.Name, func(name string) (bool, error) {
			c.Name = name
			saveErr := h.certStore.Save(&c)
			if errors.Is(saveErr, certs.ErrDuplicateName) {
				return true, saveErr
			}
			if saveErr == nil {
				created = &c
			}
			return false, saveErr
		})
		if err != nil {
			sum.skip(bc.Name, err.Error())
			continue
		}
		idMap[oldID] = created.ID
		pending = append(pending, pendingIssuer{newID: created.ID, originalIssuerID: issuerWas})
		sum.ok(name)
	}
	for _, p := range pending {
		if p.originalIssuerID == "" {
			continue
		}
		newIssuer, ok := idMap[p.originalIssuerID]
		if !ok {
			continue // issuer wasn't part of this import (or failed) — leave self-signed rather than dangling
		}
		full, err := h.certStore.Get(p.newID)
		if err != nil {
			continue
		}
		full.IssuerID = newIssuer
		h.certStore.Update(full)
	}
	return idMap
}

func (h *BackupHandler) importBundles(in []*certs.Bundle, certIDMap map[string]string, sum *importSummary) map[string]string {
	idMap := map[string]string{}
	for _, b := range in {
		nb := certs.Bundle{
			CAID:         certIDMap[b.CAID],
			ServerCertID: certIDMap[b.ServerCertID],
			ClientCertID: certIDMap[b.ClientCertID],
		}
		if b.CAID != "" && nb.CAID == "" {
			sum.skip(b.Name, "referenced CA certificate was not imported")
			continue
		}
		var created *certs.Bundle
		name, err := uniqueSuffix(b.Name, func(name string) (bool, error) {
			nb.Name = name
			c, createErr := h.certStore.CreateBundle(&nb)
			if errors.Is(createErr, certs.ErrDuplicateBundleName) {
				return true, createErr
			}
			if createErr == nil {
				created = c
			}
			return false, createErr
		})
		if err != nil {
			sum.skip(b.Name, err.Error())
			continue
		}
		idMap[b.ID] = created.ID
		sum.ok(name)
	}
	return idMap
}

func (h *BackupHandler) importEmailTemplates(in []*smtp.Template, sum *importSummary) map[string]string {
	idMap := map[string]string{}
	for _, t := range in {
		nt := smtp.Template{Subject: t.Subject, HTMLBody: t.HTMLBody}
		var created *smtp.Template
		name, err := uniqueSuffix(t.Name, func(name string) (bool, error) {
			nt.Name = name
			c, createErr := h.smtpStore.CreateTemplate(&nt)
			if errors.Is(createErr, smtp.ErrDuplicateName) {
				return true, createErr
			}
			if createErr == nil {
				created = c
			}
			return false, createErr
		})
		if err != nil {
			sum.skip(t.Name, err.Error())
			continue
		}
		idMap[t.ID] = created.ID
		sum.ok(name)
	}
	return idMap
}

// remapTLS rewrites a TCPTLSConfig's cert/bundle/CA references through the
// id maps built from this same import — an id this batch didn't (re)create
// (e.g. it referenced a cert that failed to import) is left blank rather
// than pointing at nothing that exists.
func remapTLS(tls *mock.TCPTLSConfig, certIDMap, bundleIDMap map[string]string) *mock.TCPTLSConfig {
	if tls == nil {
		return nil
	}
	return &mock.TCPTLSConfig{
		CertificateID:  certIDMap[tls.CertificateID],
		BundleID:       bundleIDMap[tls.BundleID],
		ClientCertMode: tls.ClientCertMode,
		ClientCAID:     certIDMap[tls.ClientCAID],
	}
}

func (h *BackupHandler) importMockProjects(in []*mock.Project, certIDMap, bundleIDMap map[string]string, sum *importSummary) map[string]string {
	idMap := map[string]string{}
	for _, p := range in {
		// GatewayPort is deliberately NOT carried over: it's a literal port
		// number, and a project's own dedicated port is generally
		// unique instance-wide (two projects sharing one port both serve
		// on it, so "unique method+path per gateway" also spans the two)
		// — reusing it verbatim would collide the moment two mocks from
		// the two projects share a method+path, exactly the failure mode
		// that made this decision. Imports land on the shared default
		// gateway; the port (and its TLS) can be reassigned by hand
		// afterward if a dedicated one is still wanted.
		np := mock.Project{BasePath: p.BasePath, TLS: remapTLS(p.TLS, certIDMap, bundleIDMap)}
		var created *mock.Project
		name, err := uniqueSuffix(p.Name, func(name string) (bool, error) {
			np.Name = name
			c, createErr := h.mockStore.CreateProject(&np)
			if errors.Is(createErr, mock.ErrDuplicateProjectName) {
				return true, createErr
			}
			if createErr == nil {
				created = c
			}
			return false, createErr
		})
		if err != nil {
			sum.skip(p.Name, err.Error())
			continue
		}
		idMap[p.ID] = created.ID
		sum.ok(name)
	}
	return idMap
}

// remapMockForImport rewrites a mock's every cross-entity reference
// (project, TLS cert/bundle, email template) through this import's id maps,
// clearing its own id so mockStore.Create assigns a fresh one.
func remapMockForImport(d *mock.Definition, projectIDMap, certIDMap, bundleIDMap, templateIDMap map[string]string) mock.Definition {
	nd := *d
	nd.ID = ""
	if d.ProjectID != "" {
		nd.ProjectID = projectIDMap[d.ProjectID] // "" if that project didn't import — falls back to ungrouped
	}
	if d.TCP != nil {
		tcp := *d.TCP
		tcp.TLS = remapTLS(d.TCP.TLS, certIDMap, bundleIDMap)
		nd.TCP = &tcp
	}
	if d.SMTP != nil {
		s := *d.SMTP
		s.TLS = remapTLS(d.SMTP.TLS, certIDMap, bundleIDMap)
		nd.SMTP = &s
	}
	if d.AsyncConfig != nil && d.AsyncConfig.EmailTemplateID != "" {
		ac := *d.AsyncConfig
		ac.EmailTemplateID = templateIDMap[d.AsyncConfig.EmailTemplateID]
		nd.AsyncConfig = &ac
	}
	if nd.ProtocolType == "" {
		nd.ProtocolType = "rest"
	}
	return nd
}

func (h *BackupHandler) importOneMock(nd mock.Definition, originalName string, sum *importSummary) {
	var created *mock.Definition
	name, err := uniqueSuffix(originalName, func(name string) (bool, error) {
		candidate := nd
		candidate.Name = name
		if verr := validateMockShape(&candidate); verr != nil {
			return false, verr
		}
		c, createErr := h.mockStore.Create(&candidate)
		if errors.Is(createErr, mock.ErrDuplicateName) {
			return true, createErr
		}
		if createErr == nil {
			created = c
		}
		return false, createErr
	})
	if err != nil {
		sum.skip(originalName, err.Error())
		return
	}
	if created.Enabled {
		if dispatchErr := engine.Dispatch(created); dispatchErr != nil {
			sum.skip(name, "created but failed to start: "+dispatchErr.Error())
			return
		}
	}
	sum.ok(name)
}

func (h *BackupHandler) importMocks(in []*mock.Definition, projectIDMap, certIDMap, bundleIDMap, templateIDMap map[string]string, sum *importSummary) {
	for _, d := range in {
		nd := remapMockForImport(d, projectIDMap, certIDMap, bundleIDMap, templateIDMap)
		h.importOneMock(nd, d.Name, sum)
	}
}

func (h *BackupHandler) importWorkspaces(in []*apiclient.Workspace, sum *importSummary) map[string]string {
	idMap := map[string]string{}
	for _, ws := range in {
		var created *apiclient.Workspace
		name, err := uniqueSuffix(ws.Name, func(name string) (bool, error) {
			c, createErr := h.apiClientStore.CreateWorkspace(&apiclient.Workspace{Name: name})
			if errors.Is(createErr, apiclient.ErrDuplicateWorkspaceName) {
				return true, createErr
			}
			if createErr == nil {
				created = c
			}
			return false, createErr
		})
		if err != nil {
			sum.skip(ws.Name, err.Error())
			continue
		}
		idMap[ws.ID] = created.ID
		sum.ok(name)
	}
	return idMap
}

func (h *BackupHandler) importEnvironments(in []*apiclient.Environment, workspaceIDMap map[string]string, sum *importSummary) {
	for _, e := range in {
		wsID := workspaceIDMap[e.WorkspaceID]
		if wsID == "" {
			sum.skip(e.Name, "referenced workspace was not imported")
			continue
		}
		ne := apiclient.Environment{WorkspaceID: wsID, Variables: e.Variables}
		var created *apiclient.Environment
		name, err := uniqueSuffix(e.Name, func(name string) (bool, error) {
			ne.Name = name
			c, createErr := h.apiClientStore.CreateEnvironment(&ne)
			if errors.Is(createErr, apiclient.ErrDuplicateEnvironmentName) {
				return true, createErr
			}
			if createErr == nil {
				created = c
			}
			return false, createErr
		})
		if err != nil {
			sum.skip(e.Name, err.Error())
			continue
		}
		_ = created
		sum.ok(name)
	}
}

func (h *BackupHandler) importCollections(in []*apiclient.Collection, workspaceIDMap map[string]string, sum *importSummary) {
	for _, c := range in {
		wsID := workspaceIDMap[c.WorkspaceID]
		if wsID == "" {
			sum.skip(c.Name, "referenced workspace was not imported")
			continue
		}
		nc := apiclient.Collection{WorkspaceID: wsID, Items: c.Items, Variables: c.Variables}
		var created *apiclient.Collection
		name, err := uniqueSuffix(c.Name, func(name string) (bool, error) {
			nc.Name = name
			created2, createErr := h.apiClientStore.CreateCollection(&nc)
			if errors.Is(createErr, apiclient.ErrDuplicateCollectionName) {
				return true, createErr
			}
			if createErr == nil {
				created = created2
			}
			return false, createErr
		})
		if err != nil {
			sum.skip(c.Name, err.Error())
			continue
		}
		_ = created
		sum.ok(name)
	}
}

// importScheduledEvents doesn't need any id remapping — a scheduled event
// fires an outbound HTTP call to a fixed URL, entirely independent of any
// mock — so unlike everything else imported here, name collisions are
// simply allowed to co-exist rather than needing a uniqueness check at all.
func (h *BackupHandler) importScheduledEvents(in []*scheduledevent.Event, sum *importSummary) {
	for _, e := range in {
		ne := scheduledevent.Event{
			Name: e.Name, Enabled: e.Enabled, IntervalSecs: e.IntervalSecs,
			TargetURL: e.TargetURL, Method: e.Method, Headers: e.Headers, BodyTemplate: e.BodyTemplate,
		}
		if _, err := h.eventStore.Create(&ne); err != nil {
			sum.skip(e.Name, err.Error())
			continue
		}
		sum.ok(e.Name)
	}
}

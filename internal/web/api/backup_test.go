package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/apiclient"
	"github.com/addictedabhi/airmock/internal/certs"
	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/scheduledevent"
	"github.com/addictedabhi/airmock/internal/smtp"
	"github.com/addictedabhi/airmock/internal/storage"
)

func newTestBackupRouter(t *testing.T) (chi.Router, *certs.Store, *mock.Store, *apiclient.Store, *smtp.Store, *scheduledevent.Store) {
	t.Helper()
	engine.Register(&fakeEngine{name: "http"})

	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	certStore := certs.NewStore(db)
	mockStore := mock.NewStore(db)
	apiClientStore := apiclient.NewStore(db)
	smtpStore := smtp.NewStore(db)
	eventStore := scheduledevent.NewStore(db)

	r := chi.NewRouter()
	r.Route("/api/backup", NewBackupHandler(certStore, mockStore, apiClientStore, smtpStore, eventStore).Routes)
	return r, certStore, mockStore, apiClientStore, smtpStore, eventStore
}

// TestBackupExportImportRoundTrip is the load-bearing test for this whole
// feature: seeds one of every entity type WITH real cross-references
// between them (a CA-issued server cert, a bundle built from both, a
// project using that bundle for its dedicated port's TLS, a mock inside
// that project, an email template a mock's async config references, and a
// workspace/environment/collection triple) — then exports and re-imports
// into the SAME instance (a merge onto itself, the harshest test of name
// collision handling) and confirms every reference in the imported copies
// points at the NEW ids, not the original ones, and every name got the
// "(imported)" suffix rather than being skipped or silently colliding.
func TestBackupExportImportRoundTrip(t *testing.T) {
	r, certStore, mockStore, apiClientStore, smtpStore, eventStore := newTestBackupRouter(t)

	ca, err := certs.Generate(certs.GenerateRequest{Name: "root-ca", Kind: certs.KindCA, CommonName: "root-ca", KeyAlgorithm: certs.KeyECDSA, ValidDays: 30})
	if err != nil {
		t.Fatalf("generate ca: %v", err)
	}
	if err := certStore.Save(ca); err != nil {
		t.Fatalf("save ca: %v", err)
	}
	server, err := certs.Generate(certs.GenerateRequest{Name: "server-cert", Kind: certs.KindServer, CommonName: "mock.local", KeyAlgorithm: certs.KeyECDSA, ValidDays: 30, Issuer: ca})
	if err != nil {
		t.Fatalf("generate server: %v", err)
	}
	if err := certStore.Save(server); err != nil {
		t.Fatalf("save server: %v", err)
	}
	bundle, err := certStore.CreateBundle(&certs.Bundle{Name: "my-bundle", CAID: ca.ID, ServerCertID: server.ID})
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	proj, err := mockStore.CreateProject(&mock.Project{
		Name: "my-project", GatewayPort: 19600,
		TLS: &mock.TCPTLSConfig{BundleID: bundle.ID, ClientCertMode: "required", ClientCAID: ca.ID},
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	tmpl, err := smtpStore.CreateTemplate(&smtp.Template{Name: "welcome-email", Subject: "hi", HTMLBody: "<p>hi</p>"})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}

	m, err := mockStore.Create(&mock.Definition{
		Name: "my-mock", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/hello", Enabled: true,
		ProjectID: proj.ID,
		Response:  mock.ResponseTemplate{StatusCode: 200, BodyTemplate: "hi"},
		AsyncConfig: &mock.AsyncConfig{CallbackChannel: "email", EmailTemplateID: tmpl.ID},
	})
	if err != nil {
		t.Fatalf("create mock: %v", err)
	}

	ws, err := apiClientStore.CreateWorkspace(&apiclient.Workspace{Name: "my-workspace"})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := apiClientStore.CreateEnvironment(&apiclient.Environment{WorkspaceID: ws.ID, Name: "my-env", Variables: map[string]string{"k": "v"}}); err != nil {
		t.Fatalf("create environment: %v", err)
	}
	if _, err := apiClientStore.CreateCollection(&apiclient.Collection{WorkspaceID: ws.ID, Name: "my-collection"}); err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if _, err := eventStore.Create(&scheduledevent.Event{Name: "my-event", IntervalSecs: 60, TargetURL: "http://example.com", Method: "POST"}); err != nil {
		t.Fatalf("create event: %v", err)
	}

	// Export, then feed the exact same bytes back in as an import.
	exportRec := doJSON(t, r, http.MethodGet, "/api/backup/export", nil)
	if exportRec.Code != http.StatusOK {
		t.Fatalf("export: expected 200, got %d: %s", exportRec.Code, exportRec.Body.String())
	}
	var exported backupFile
	if err := json.Unmarshal(exportRec.Body.Bytes(), &exported); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}
	// Workspaces is 2, not 1 — every fresh instance seeds a seat "Default"
	// workspace via migration (000014_workspaces.up.sql), on top of the one
	// this test created.
	if len(exported.Certificates) != 2 || len(exported.CertBundles) != 1 || len(exported.MockProjects) != 1 ||
		len(exported.Mocks) != 1 || len(exported.EmailTemplates) != 1 || len(exported.Workspaces) != 2 ||
		len(exported.Environments) != 1 || len(exported.Collections) != 1 || len(exported.ScheduledEvents) != 1 {
		t.Fatalf("expected exactly one of each seeded entity in the export (2 workspaces incl. the seeded Default), got %+v", exported)
	}
	// The export must include the actual private key, not just the public
	// cert — otherwise a restored certificate would be useless.
	for _, c := range exported.Certificates {
		if c.KeyPEM == "" {
			t.Fatalf("expected KeyPEM populated in the export for cert %q, got empty", c.Name)
		}
	}

	// doJSON's json.Encode would base64-wrap raw []byte body instead of
	// passing it through — POST the export's exact bytes directly instead.
	importReq := httptest.NewRequest(http.MethodPost, "/api/backup/import", bytes.NewReader(exportRec.Body.Bytes()))
	importRec := httptest.NewRecorder()
	r.ServeHTTP(importRec, importReq)
	if importRec.Code != http.StatusOK {
		t.Fatalf("import: expected 200, got %d: %s", importRec.Code, importRec.Body.String())
	}

	// Every name collided with what already exists — confirm every category
	// suffixed rather than being skipped.
	var result struct {
		Certificates    importSummary `json:"certificates"`
		CertBundles     importSummary `json:"certBundles"`
		MockProjects    importSummary `json:"mockProjects"`
		Mocks           importSummary `json:"mocks"`
		EmailTemplates  importSummary `json:"emailTemplates"`
		Workspaces      importSummary `json:"workspaces"`
		Environments    importSummary `json:"environments"`
		Collections     importSummary `json:"collections"`
		ScheduledEvents importSummary `json:"scheduledEvents"`
	}
	if err := json.Unmarshal(importRec.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal import result: %v", err)
	}
	// Workspaces expects 2 imported (Default + my-workspace), everything
	// else expects 1 — this test only ever seeded one of each other entity.
	wantImported := map[string]int{
		"certificates": 2, "certBundles": 1, "mockProjects": 1, "mocks": 1, "emailTemplates": 1,
		"workspaces": 2, "environments": 1, "collections": 1, "scheduledEvents": 1,
	}
	for label, s := range map[string]importSummary{
		"certificates": result.Certificates, "certBundles": result.CertBundles, "mockProjects": result.MockProjects,
		"mocks": result.Mocks, "emailTemplates": result.EmailTemplates, "workspaces": result.Workspaces,
		"environments": result.Environments, "collections": result.Collections, "scheduledEvents": result.ScheduledEvents,
	} {
		if len(s.Skipped) != 0 {
			t.Fatalf("%s: expected nothing skipped, got %+v", label, s.Skipped)
		}
		if len(s.Imported) != wantImported[label] {
			t.Fatalf("%s: expected %d imported, got %+v", label, wantImported[label], s.Imported)
		}
	}

	// Now verify the ACTUAL remapping, not just that something landed —
	// this is the part most likely to have a subtle bug.
	allCerts, err := certStore.List()
	if err != nil {
		t.Fatalf("List certs: %v", err)
	}
	if len(allCerts) != 4 {
		t.Fatalf("expected 4 certs total (2 original + 2 imported), got %d", len(allCerts))
	}
	var importedCA, importedServer *certs.Certificate
	for _, c := range allCerts {
		if c.Name == "root-ca (imported)" {
			importedCA = c
		}
		if c.Name == "server-cert (imported)" {
			importedServer = c
		}
	}
	if importedCA == nil || importedServer == nil {
		t.Fatalf("expected both certs re-imported with an (imported) suffix, got %+v", allCerts)
	}
	if importedServer.IssuerID != importedCA.ID {
		t.Fatalf("expected the imported server cert's IssuerID remapped to the imported CA's new id (%s), got %q", importedCA.ID, importedServer.IssuerID)
	}
	if importedCA.ID == ca.ID || importedServer.ID == server.ID {
		t.Fatal("expected brand-new ids for the imported certs, not reuse of the originals'")
	}

	bundles, err := certStore.ListBundles()
	if err != nil {
		t.Fatalf("ListBundles: %v", err)
	}
	var importedBundle *certs.Bundle
	for _, b := range bundles {
		if b.Name == "my-bundle (imported)" {
			importedBundle = b
		}
	}
	if importedBundle == nil {
		t.Fatalf("expected the bundle re-imported with an (imported) suffix, got %+v", bundles)
	}
	if importedBundle.CAID != importedCA.ID || importedBundle.ServerCertID != importedServer.ID {
		t.Fatalf("expected the imported bundle to reference the IMPORTED certs, got CAID=%s ServerCertID=%s", importedBundle.CAID, importedBundle.ServerCertID)
	}

	projects, err := mockStore.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	var importedProject *mock.Project
	for _, p := range projects {
		if p.Name == "my-project (imported)" {
			importedProject = p
		}
	}
	if importedProject == nil {
		t.Fatalf("expected the project re-imported with an (imported) suffix, got %+v", projects)
	}
	if importedProject.TLS == nil || importedProject.TLS.BundleID != importedBundle.ID || importedProject.TLS.ClientCAID != importedCA.ID {
		t.Fatalf("expected the imported project's TLS to reference the imported bundle/CA, got %+v", importedProject.TLS)
	}

	templates, err := smtpStore.ListTemplates()
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	var importedTemplate *smtp.Template
	for _, tp := range templates {
		if tp.Name == "welcome-email (imported)" {
			importedTemplate = tp
		}
	}
	if importedTemplate == nil {
		t.Fatalf("expected the email template re-imported with an (imported) suffix, got %+v", templates)
	}

	mocks, err := mockStore.List()
	if err != nil {
		t.Fatalf("List mocks: %v", err)
	}
	var importedMock *mock.Definition
	for _, d := range mocks {
		if d.Name == "my-mock (imported)" {
			importedMock = d
		}
	}
	if importedMock == nil {
		t.Fatalf("expected the mock re-imported with an (imported) suffix, got %+v", mocks)
	}
	if importedMock.ID == m.ID {
		t.Fatal("expected the imported mock to have a brand-new id")
	}
	if importedMock.ProjectID != importedProject.ID {
		t.Fatalf("expected the imported mock's ProjectID remapped to the imported project (%s), got %q", importedProject.ID, importedMock.ProjectID)
	}
	if importedMock.AsyncConfig == nil || importedMock.AsyncConfig.EmailTemplateID != importedTemplate.ID {
		t.Fatalf("expected the imported mock's EmailTemplateID remapped to the imported template (%s), got %+v", importedTemplate.ID, importedMock.AsyncConfig)
	}

	workspaces, err := apiClientStore.ListWorkspaces()
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	var importedWorkspace *apiclient.Workspace
	for _, w := range workspaces {
		if w.Name == "my-workspace (imported)" {
			importedWorkspace = w
		}
	}
	if importedWorkspace == nil {
		t.Fatalf("expected the workspace re-imported with an (imported) suffix, got %+v", workspaces)
	}

	envs, err := apiClientStore.ListEnvironments(importedWorkspace.ID)
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(envs) != 1 || envs[0].Name != "my-env" || envs[0].Variables["k"] != "v" {
		t.Fatalf("expected the imported environment under the imported workspace, got %+v", envs)
	}

	cols, err := apiClientStore.ListCollections(importedWorkspace.ID)
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(cols) != 1 || cols[0].Name != "my-collection" {
		t.Fatalf("expected the imported collection under the imported workspace, got %+v", cols)
	}

	events, err := eventStore.List()
	if err != nil {
		t.Fatalf("List events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 scheduled events (original + imported, no dedup needed since they're name-independent), got %d", len(events))
	}
}

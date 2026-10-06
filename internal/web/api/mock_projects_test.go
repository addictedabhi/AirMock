package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/engine"
	httpengine "github.com/addictedabhi/airmock/internal/engine/http"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/storage"
)

// newTestMockProjectsRouter mounts MockProjectsHandler over a real
// mock.Store AND a real *httpengine.Engine (wired the same way server.go
// wires them: SetProjectPortResolver(store)) — needed to exercise the
// handler's Rebuild() call, not just the plain DB CRUD. Also registers the
// same fakeEngine (mocks_test.go) under "http" in the package-level engine
// registry, since MockProjectsHandler.delete now calls
// engine.DispatchUnregister for every mock it cascades away.
func newTestMockProjectsRouter(t *testing.T) (chi.Router, *mock.Store, *httpengine.Engine) {
	t.Helper()
	engine.Register(&fakeEngine{name: "http"})

	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := mock.NewStore(db)
	eng := httpengine.New()
	eng.SetProjectPortResolver(store)
	r := chi.NewRouter()
	r.Route("/api/mock-projects", NewMockProjectsHandler(store, eng, fakeWorkspaceLockChecker{}, fakeWorkspaceUnlockTracker{}).Routes)
	return r, store, eng
}

func TestMockProjectsListReturnsAllProjects(t *testing.T) {
	r, store, _ := newTestMockProjectsRouter(t)
	if _, err := store.CreateProject(&mock.Project{Name: "alpha"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := store.CreateProject(&mock.Project{Name: "beta"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	rec := doJSON(t, r, http.MethodGet, "/api/mock-projects/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var list []*mock.Project
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Checks both names specifically, not just the count — a length-only
	// check would pass even if list returned two copies of the same
	// project (e.g. a wrong WHERE clause producing a duplicate row).
	names := map[string]bool{}
	for _, p := range list {
		names[p.Name] = true
	}
	if len(list) != 2 || !names["alpha"] || !names["beta"] {
		t.Fatalf("expected both alpha and beta projects, got %+v", list)
	}
}

func TestMockProjectsCreateSuccess(t *testing.T) {
	r, store, _ := newTestMockProjectsRouter(t)

	rec := doJSON(t, r, http.MethodPost, "/api/mock-projects/", mock.Project{Name: "new-project", BasePath: "/np"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created mock.Project
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected the created project to have an assigned id")
	}
	if created.Name != "new-project" || created.BasePath != "/np" {
		t.Fatalf("unexpected created project: %+v", created)
	}

	got, err := store.GetProject(created.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Name != "new-project" {
		t.Fatalf("expected the project to actually be persisted, got %+v", got)
	}
}

func TestMockProjectsCreateRejectsMissingName(t *testing.T) {
	r, _, _ := newTestMockProjectsRouter(t)

	rec := doJSON(t, r, http.MethodPost, "/api/mock-projects/", mock.Project{BasePath: "/no-name"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing name, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMockProjectsCreateRejectsDuplicateName(t *testing.T) {
	r, _, _ := newTestMockProjectsRouter(t)

	rec := doJSON(t, r, http.MethodPost, "/api/mock-projects/", mock.Project{Name: "dup"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected first create to succeed with 201, got %d: %s", rec.Code, rec.Body.String())
	}

	rec2 := doJSON(t, r, http.MethodPost, "/api/mock-projects/", mock.Project{Name: " DUP "})
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a case/whitespace-insensitive duplicate name, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestMockProjectsUpdateSuccess(t *testing.T) {
	r, store, _ := newTestMockProjectsRouter(t)
	created, err := store.CreateProject(&mock.Project{Name: "original"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	rec := doJSON(t, r, http.MethodPut, "/api/mock-projects/"+created.ID, mock.Project{Name: "renamed", BasePath: "/renamed"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var updated mock.Project
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if updated.Name != "renamed" || updated.BasePath != "/renamed" {
		t.Fatalf("unexpected updated project: %+v", updated)
	}

	got, err := store.GetProject(created.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Name != "renamed" {
		t.Fatalf("expected the rename to be persisted, got %+v", got)
	}
}

func TestMockProjectsUpdateNotFound(t *testing.T) {
	r, _, _ := newTestMockProjectsRouter(t)

	rec := doJSON(t, r, http.MethodPut, "/api/mock-projects/does-not-exist", mock.Project{Name: "whatever"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for updating an unknown id, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMockProjectsUpdateRejectsDuplicateName(t *testing.T) {
	r, store, _ := newTestMockProjectsRouter(t)
	if _, err := store.CreateProject(&mock.Project{Name: "taken"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	other, err := store.CreateProject(&mock.Project{Name: "other"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	rec := doJSON(t, r, http.MethodPut, "/api/mock-projects/"+other.ID, mock.Project{Name: "taken"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for renaming into an existing name, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMockProjectsDeleteSuccess(t *testing.T) {
	r, store, _ := newTestMockProjectsRouter(t)
	created, err := store.CreateProject(&mock.Project{Name: "to-delete"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	rec := doJSON(t, r, http.MethodDelete, "/api/mock-projects/"+created.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := store.GetProject(created.ID); err != mock.ErrNotFound {
		t.Fatalf("expected the project to be gone after delete, got err=%v", err)
	}
}

func TestMockProjectsDeleteNotFound(t *testing.T) {
	r, _, _ := newTestMockProjectsRouter(t)

	rec := doJSON(t, r, http.MethodDelete, "/api/mock-projects/does-not-exist", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for deleting an unknown id, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestMockProjectsDeleteCascadesToItsMocks guards the current, intentionally
// destructive behavior: deleting a project deletes every mock inside it
// too, not just the project's own grouping row — since this is exactly the
// kind of destructive-by-default behavior an admin API test should pin
// down explicitly.
func TestMockProjectsDeleteCascadesToItsMocks(t *testing.T) {
	r, store, _ := newTestMockProjectsRouter(t)
	proj, err := store.CreateProject(&mock.Project{Name: "with-mocks"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	m, err := store.Create(&mock.Definition{Name: "grouped-mock", Method: "GET", PathPattern: "/grouped", Enabled: true, ProjectID: proj.ID})
	if err != nil {
		t.Fatalf("Create mock: %v", err)
	}
	other, err := store.Create(&mock.Definition{Name: "unrelated", Method: "GET", PathPattern: "/unrelated", Enabled: true})
	if err != nil {
		t.Fatalf("Create unrelated mock: %v", err)
	}

	rec := doJSON(t, r, http.MethodDelete, "/api/mock-projects/"+proj.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := store.Get(m.ID); err != mock.ErrNotFound {
		t.Fatalf("expected the project's mock to be deleted, got err=%v", err)
	}
	if _, err := store.Get(other.ID); err != nil {
		t.Fatalf("expected an unrelated (ungrouped) mock to survive, got err=%v", err)
	}
}

// TestUpdateProjectDedicatedPortTakesEffectImmediately guards the actual
// bug this closes: a project's OWN GatewayPort setting previously sat inert
// in the DB until some unrelated mock event (create/update/delete/toggle)
// happened to trigger the http engine's rebuild() as a side effect. Setting
// the port via PUT /api/mock-projects/{id} alone — with NO mock event at
// all afterward — must now serve that project's mock on the new dedicated
// port right away.
func TestUpdateProjectDedicatedPortTakesEffectImmediately(t *testing.T) {
	r, store, eng := newTestMockProjectsRouter(t)
	proj, err := store.CreateProject(&mock.Project{Name: "dedicated"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Register the mock directly on the engine (mocks.go's create handler
	// would normally do this) — this project has no dedicated port yet, so
	// it's served on the shared default gateway (port 0 in resolvePort's
	// scheme, meaning "not a project-specific extra listener" for this test).
	def := &mock.Definition{
		ID: "m1", ProjectID: proj.ID, ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: "hit"},
	}
	if err := eng.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}

	rec := doJSON(t, r, http.MethodPut, "/api/mock-projects/"+proj.ID, mock.Project{Name: proj.Name, GatewayPort: 19410})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// No further mock event of any kind — if Rebuild() weren't wired in,
	// this dedicated port would simply never come up.
	resp, err := http.Get("http://127.0.0.1:19410/")
	if err != nil {
		t.Fatalf("expected the project's new dedicated port to be serving immediately, got: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 from the dedicated port, got %d", resp.StatusCode)
	}
}

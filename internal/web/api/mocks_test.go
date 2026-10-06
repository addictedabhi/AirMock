package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/storage"
)

// fakeEngine is a no-op engine.Engine stand-in — registered under "http" so
// Dispatch/DispatchUnregister (called by create/update/delete/import/bulk)
// succeed without needing a real HTTP listener in these handler-level tests.
type fakeEngine struct{ name string }

func (f *fakeEngine) Name() string                                               { return f.name }
func (f *fakeEngine) Start(ctx context.Context, cfg engine.ListenerConfig) error { return nil }
func (f *fakeEngine) Stop(ctx context.Context) error                             { return nil }
func (f *fakeEngine) RegisterMock(m *mock.Definition) error                      { return nil }
func (f *fakeEngine) UnregisterMock(id string) error                             { return nil }

// fakeWorkspaceLockChecker/fakeWorkspaceUnlockTracker stand in for
// apiclient.Store/wslock.Tracker in these handler-level tests — none of
// them exercise workspace locking, so every workspace reports as unlocked
// (never blocking a create/update/delete), matching today's behavior
// before workspace locks existed.
type fakeWorkspaceLockChecker struct{}

func (fakeWorkspaceLockChecker) IsWorkspaceLocked(id string) (bool, error) { return false, nil }

type fakeWorkspaceUnlockTracker struct{}

func (fakeWorkspaceUnlockTracker) IsUnlocked(clientID, workspaceID string) bool { return true }

func newTestMocksRouter(t *testing.T) (chi.Router, *mock.Store) {
	t.Helper()
	engine.Register(&fakeEngine{name: "http"})

	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := mock.NewStore(db)
	r := chi.NewRouter()
	r.Route("/api/mocks", NewMocksHandler(store, fakeWorkspaceLockChecker{}, fakeWorkspaceUnlockTracker{}).Routes)
	return r, store
}

func doJSON(t *testing.T, r chi.Router, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestExportAllReturnsEveryMock(t *testing.T) {
	r, store := newTestMocksRouter(t)
	if _, err := store.Create(&mock.Definition{Name: "a", Method: "GET", PathPattern: "/a", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Create(&mock.Definition{Name: "b", Method: "GET", PathPattern: "/b", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doJSON(t, r, http.MethodGet, "/api/mocks/export", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body exportedMocks
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Mocks) != 2 {
		t.Fatalf("expected 2 exported mocks, got %d", len(body.Mocks))
	}
}

func TestImportAllCreatesMocksAndSkipsDuplicates(t *testing.T) {
	r, store := newTestMocksRouter(t)
	if _, err := store.Create(&mock.Definition{Name: "existing", Method: "GET", PathPattern: "/existing", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	payload := exportedMocks{Mocks: []*mock.Definition{
		{ID: "some-foreign-id", Name: "fresh-mock", Method: "GET", PathPattern: "/fresh", Enabled: true},
		{Name: "existing", Method: "GET", PathPattern: "/existing", Enabled: true}, // name collision -> should be skipped
		{Name: "no-shape", ProtocolType: "tcp"},                                    // invalid shape -> should be skipped
	}}

	rec := doJSON(t, r, http.MethodPost, "/api/mocks/import", payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result importResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Imported) != 1 || result.Imported[0] != "fresh-mock" {
		t.Fatalf("expected exactly fresh-mock to be imported, got %+v", result.Imported)
	}
	if len(result.Skipped) != 2 {
		t.Fatalf("expected 2 skipped entries, got %+v", result.Skipped)
	}

	all, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 { // the pre-existing mock + the one freshly imported
		t.Fatalf("expected 2 mocks in the store after import, got %d", len(all))
	}
	for _, d := range all {
		if d.Name == "fresh-mock" && d.ID == "some-foreign-id" {
			t.Fatal("expected the imported mock to get a fresh ID, not reuse the exported one")
		}
	}
}

// TestImportAllAssignsRequestedProjectAndClearsForeignOnes guards the two
// behaviors of exportedMocks.ProjectID together: when the caller specifies
// a project, every imported mock lands there regardless of whatever
// projectId it originally carried in the exported JSON (which may not even
// exist on this instance — a project ID from a different AirMock install);
// when the caller specifies no project at all, any such foreign projectId
// must be cleared rather than preserved, or the imported mock would end up
// referencing a project ID that never resolves to anything and never
// render anywhere in the UI.
func TestImportAllAssignsRequestedProjectAndClearsForeignOnes(t *testing.T) {
	r, store := newTestMocksRouter(t)

	withProject := doJSON(t, r, http.MethodPost, "/api/mocks/import", exportedMocks{
		ProjectID: "my-project-id",
		Mocks:     []*mock.Definition{{Name: "grouped", Method: "GET", PathPattern: "/grouped", Enabled: true, ProjectID: "some-other-instances-project-id"}},
	})
	if withProject.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", withProject.Code, withProject.Body.String())
	}

	withoutProject := doJSON(t, r, http.MethodPost, "/api/mocks/import", exportedMocks{
		Mocks: []*mock.Definition{{Name: "ungrouped", Method: "GET", PathPattern: "/ungrouped", Enabled: true, ProjectID: "some-other-instances-project-id"}},
	})
	if withoutProject.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", withoutProject.Code, withoutProject.Body.String())
	}

	all, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, d := range all {
		switch d.Name {
		case "grouped":
			if d.ProjectID != "my-project-id" {
				t.Fatalf("expected the requested project to override the original projectId, got %q", d.ProjectID)
			}
		case "ungrouped":
			if d.ProjectID != "" {
				t.Fatalf("expected the foreign projectId to be cleared when no project was requested, got %q", d.ProjectID)
			}
		}
	}
}

func TestBulkActionEnableDisableDelete(t *testing.T) {
	r, store := newTestMocksRouter(t)
	a, err := store.Create(&mock.Definition{Name: "a", Method: "GET", PathPattern: "/a", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	b, err := store.Create(&mock.Definition{Name: "b", Method: "GET", PathPattern: "/b", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Disable both.
	rec := doJSON(t, r, http.MethodPost, "/api/mocks/bulk", map[string]any{"ids": []string{a.ID, b.ID}, "action": "disable"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var disableResult bulkActionResult
	if err := json.Unmarshal(rec.Body.Bytes(), &disableResult); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(disableResult.Succeeded) != 2 || len(disableResult.Failed) != 0 {
		t.Fatalf("expected both disables to succeed, got %+v", disableResult)
	}
	gotA, err := store.Get(a.ID)
	if err != nil || gotA.Enabled {
		t.Fatalf("expected mock a to be disabled, got %+v err=%v", gotA, err)
	}

	// Delete one, and include a bogus id — the bogus one should fail without
	// aborting the whole batch.
	rec2 := doJSON(t, r, http.MethodPost, "/api/mocks/bulk", map[string]any{"ids": []string{a.ID, "does-not-exist"}, "action": "delete"})
	var deleteResult bulkActionResult
	if err := json.Unmarshal(rec2.Body.Bytes(), &deleteResult); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(deleteResult.Succeeded) != 1 || deleteResult.Succeeded[0] != a.ID {
		t.Fatalf("expected exactly a.ID to succeed, got %+v", deleteResult.Succeeded)
	}
	if len(deleteResult.Failed) != 1 {
		t.Fatalf("expected exactly 1 failure for the bogus id, got %+v", deleteResult.Failed)
	}
	if _, err := store.Get(a.ID); err != mock.ErrNotFound {
		t.Fatalf("expected mock a to be deleted, got err=%v", err)
	}

	// Bad action name is rejected outright.
	rec3 := doJSON(t, r, http.MethodPost, "/api/mocks/bulk", map[string]any{"ids": []string{b.ID}, "action": "explode"})
	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid action, got %d", rec3.Code)
	}
}

func TestListReturnsEmptySliceThenEveryCreatedMock(t *testing.T) {
	r, store := newTestMocksRouter(t)

	rec := doJSON(t, r, http.MethodGet, "/api/mocks", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var empty []mock.Definition
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("expected an empty array (not null) with no mocks, got %v", empty)
	}

	if _, err := store.Create(&mock.Definition{Name: "a", Method: "GET", PathPattern: "/a"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = doJSON(t, r, http.MethodPost, "/api/mocks", map[string]any{"name": "b", "method": "GET", "pathPattern": "/b"})

	rec2 := doJSON(t, r, http.MethodGet, "/api/mocks", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var all []mock.Definition
	if err := json.Unmarshal(rec2.Body.Bytes(), &all); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 mocks, got %d: %+v", len(all), all)
	}
}

func TestCreateSucceedsAndDefaultsProtocolTypeToRest(t *testing.T) {
	r, store := newTestMocksRouter(t)

	rec := doJSON(t, r, http.MethodPost, "/api/mocks", map[string]any{
		"name": "no-protocol-given", "method": "GET", "pathPattern": "/no-protocol",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created mock.Definition
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected the created mock to be assigned an ID")
	}
	if created.ProtocolType != "rest" {
		t.Fatalf("expected protocolType to default to %q, got %q", "rest", created.ProtocolType)
	}

	persisted, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if persisted.Name != "no-protocol-given" {
		t.Fatalf("expected the mock to be persisted, got %+v", persisted)
	}
}

func TestCreateRejectsInvalidShape(t *testing.T) {
	r, _ := newTestMocksRouter(t)

	// rest protocol with no method/pathPattern fails validateMockShape.
	rec := doJSON(t, r, http.MethodPost, "/api/mocks", map[string]any{"name": "bad", "protocolType": "rest"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateReturnsConflictOnDuplicateNameOrEndpoint(t *testing.T) {
	r, _ := newTestMocksRouter(t)

	rec := doJSON(t, r, http.MethodPost, "/api/mocks", map[string]any{"name": "dup", "method": "GET", "pathPattern": "/dup-a"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Same name, different endpoint -> name conflict.
	recName := doJSON(t, r, http.MethodPost, "/api/mocks", map[string]any{"name": "dup", "method": "GET", "pathPattern": "/dup-b"})
	if recName.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate name, got %d: %s", recName.Code, recName.Body.String())
	}

	// Different name, same method+path -> endpoint conflict.
	recEndpoint := doJSON(t, r, http.MethodPost, "/api/mocks", map[string]any{"name": "dup-2", "method": "GET", "pathPattern": "/dup-a"})
	if recEndpoint.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate endpoint, got %d: %s", recEndpoint.Code, recEndpoint.Body.String())
	}
}

func TestGetReturnsFullDefinitionOrNotFound(t *testing.T) {
	r, store := newTestMocksRouter(t)
	created, err := store.Create(&mock.Definition{Name: "gettable", Method: "GET", PathPattern: "/gettable", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doJSON(t, r, http.MethodGet, "/api/mocks/"+created.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got mock.Definition
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != created.ID || got.Name != "gettable" || got.PathPattern != "/gettable" {
		t.Fatalf("expected the full definition back, got %+v", got)
	}

	recMissing := doJSON(t, r, http.MethodGet, "/api/mocks/does-not-exist", nil)
	if recMissing.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", recMissing.Code, recMissing.Body.String())
	}
}

func TestUpdateSucceedsAndPersistsChange(t *testing.T) {
	r, store := newTestMocksRouter(t)
	created, err := store.Create(&mock.Definition{Name: "original", Method: "GET", PathPattern: "/original", Enabled: false})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doJSON(t, r, http.MethodPut, "/api/mocks/"+created.ID, map[string]any{
		"name": "renamed", "protocolType": "rest", "method": "GET", "pathPattern": "/original", "enabled": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Assert the update took effect via a subsequent get (Dispatch itself
	// isn't directly observable through fakeEngine).
	getRec := doJSON(t, r, http.MethodGet, "/api/mocks/"+created.ID, nil)
	var got mock.Definition
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Name != "renamed" || !got.Enabled {
		t.Fatalf("expected the update to persist, got %+v", got)
	}
}

// trackingFakeEngine records every UnregisterMock call it receives — used
// to prove the OLD engine is actually told to drop a mock ID when a
// protocol-type change moves that ID to a different engine.
type trackingFakeEngine struct {
	fakeEngine
	mu           sync.Mutex
	unregistered []string
}

func (f *trackingFakeEngine) UnregisterMock(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unregistered = append(f.unregistered, id)
	return nil
}

func (f *trackingFakeEngine) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.unregistered))
	copy(out, f.unregistered)
	return out
}

// TestUpdateChangingProtocolTypeUnregistersFromTheOldEngine guards against
// a real gap: store.Update overwrites protocol_type unconditionally, and
// engine.Dispatch(updated) only ever registers with the mock's NEW
// protocol's engine — the OLD engine (a completely different Engine
// instance for a cross-family change like tcp -> rest) previously never
// learned this ID no longer belongs to it, leaking a stale listener/session
// forever with no way to close it short of a process restart.
func TestUpdateChangingProtocolTypeUnregistersFromTheOldEngine(t *testing.T) {
	r, store := newTestMocksRouter(t)
	tcpEngine := &trackingFakeEngine{fakeEngine: fakeEngine{name: "tcp"}}
	engine.Register(tcpEngine)

	created, err := store.Create(&mock.Definition{
		Name: "was-tcp", ProtocolType: "tcp", Enabled: true,
		TCP: &mock.TCPConfig{Port: 0},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doJSON(t, r, http.MethodPut, "/api/mocks/"+created.ID, map[string]any{
		"name": "was-tcp", "protocolType": "rest", "method": "GET", "pathPattern": "/now-rest", "enabled": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if calls := tcpEngine.calls(); len(calls) != 1 || calls[0] != created.ID {
		t.Fatalf("expected the tcp engine to be told to unregister %q exactly once, got %+v", created.ID, calls)
	}
}

// TestUpdateWithinSameProtocolFamilyDoesNotUnregisterFromTheSameEngine
// guards the flip side: rest -> soap (both the "http" family) must NOT
// trigger a spurious UnregisterMock call against the very engine that's
// also just been told to RegisterMock the new definition — that engine's
// own rebuild-from-current-state already handles an in-family protocol
// change correctly (registerRestRoute vs the SOAP grouping branch, both
// driven by the same map keyed by ID).
func TestUpdateWithinSameProtocolFamilyDoesNotUnregisterFromTheSameEngine(t *testing.T) {
	r, store := newTestMocksRouter(t)
	// newTestMocksRouter already registers a plain (non-tracking) fakeEngine
	// under "http"; replace it with a tracking one so this test can assert
	// on it directly without disturbing the shared registry's "tcp" entry.
	httpEngine := &trackingFakeEngine{fakeEngine: fakeEngine{name: "http"}}
	engine.Register(httpEngine)

	created, err := store.Create(&mock.Definition{Name: "was-rest", ProtocolType: "rest", Method: "GET", PathPattern: "/a", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doJSON(t, r, http.MethodPut, "/api/mocks/"+created.ID, map[string]any{
		"name": "was-rest", "protocolType": "soap", "method": "POST", "pathPattern": "/a", "enabled": true, "operationName": "Op", "soapAction": "urn:op",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if calls := httpEngine.calls(); len(calls) != 0 {
		t.Fatalf("expected no UnregisterMock calls for an in-family protocol change, got %+v", calls)
	}
}

func TestUpdateReturnsNotFoundForMissingID(t *testing.T) {
	r, _ := newTestMocksRouter(t)

	rec := doJSON(t, r, http.MethodPut, "/api/mocks/does-not-exist", map[string]any{
		"name": "whatever", "protocolType": "rest", "method": "GET", "pathPattern": "/whatever",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateReturnsConflictOnRenameToExistingName(t *testing.T) {
	r, store := newTestMocksRouter(t)
	if _, err := store.Create(&mock.Definition{Name: "taken", Method: "GET", PathPattern: "/taken"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	toRename, err := store.Create(&mock.Definition{Name: "renameable", Method: "GET", PathPattern: "/renameable"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doJSON(t, r, http.MethodPut, "/api/mocks/"+toRename.ID, map[string]any{
		"name": "taken", "protocolType": "rest", "method": "GET", "pathPattern": "/renameable",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate name on rename, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteSucceedsAndSubsequentGetIsNotFound(t *testing.T) {
	r, store := newTestMocksRouter(t)
	created, err := store.Create(&mock.Definition{Name: "deletable", Method: "GET", PathPattern: "/deletable", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doJSON(t, r, http.MethodDelete, "/api/mocks/"+created.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	getRec := doJSON(t, r, http.MethodGet, "/api/mocks/"+created.ID, nil)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d: %s", getRec.Code, getRec.Body.String())
	}
}

func TestDeleteReturnsNotFoundForMissingID(t *testing.T) {
	r, _ := newTestMocksRouter(t)

	rec := doJSON(t, r, http.MethodDelete, "/api/mocks/does-not-exist", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListVersionsReturnsSnapshotsAfterUpdate(t *testing.T) {
	r, store := newTestMocksRouter(t)
	created, err := store.Create(&mock.Definition{Name: "versioned", Method: "GET", PathPattern: "/v1", Enabled: false})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// No update yet -> no version history.
	recEmpty := doJSON(t, r, http.MethodGet, "/api/mocks/"+created.ID+"/versions", nil)
	if recEmpty.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recEmpty.Code, recEmpty.Body.String())
	}
	var emptyVersions []mock.Version
	if err := json.Unmarshal(recEmpty.Body.Bytes(), &emptyVersions); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(emptyVersions) != 0 {
		t.Fatalf("expected no versions before any update, got %+v", emptyVersions)
	}

	// Update once -> snapshotVersion records the pre-update state ("v1").
	updRec := doJSON(t, r, http.MethodPut, "/api/mocks/"+created.ID, map[string]any{
		"name": "versioned", "protocolType": "rest", "method": "GET", "pathPattern": "/v2", "enabled": true,
	})
	if updRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", updRec.Code, updRec.Body.String())
	}

	rec := doJSON(t, r, http.MethodGet, "/api/mocks/"+created.ID+"/versions", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var versions []mock.Version
	if err := json.Unmarshal(rec.Body.Bytes(), &versions); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("expected exactly 1 version snapshot, got %d: %+v", len(versions), versions)
	}
	if versions[0].Definition.PathPattern != "/v1" || versions[0].Definition.Enabled {
		t.Fatalf("expected the snapshot to hold the pre-update state, got %+v", versions[0].Definition)
	}

	// Sanity: the store itself also agrees.
	storeVersions, err := store.ListVersions(created.ID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(storeVersions) != 1 {
		t.Fatalf("expected 1 version in the store, got %d", len(storeVersions))
	}
}

func TestRestoreVersionOverwritesCurrentDefinition(t *testing.T) {
	r, store := newTestMocksRouter(t)
	created, err := store.Create(&mock.Definition{Name: "restorable", Method: "GET", PathPattern: "/original-path", Enabled: false})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update so the original state gets snapshotted as a version.
	updRec := doJSON(t, r, http.MethodPut, "/api/mocks/"+created.ID, map[string]any{
		"name": "restorable", "protocolType": "rest", "method": "GET", "pathPattern": "/changed-path", "enabled": true,
	})
	if updRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", updRec.Code, updRec.Body.String())
	}

	versions, err := store.ListVersions(created.ID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("expected 1 version snapshot, got %d", len(versions))
	}
	oldVersionID := versions[0].ID

	rec := doJSON(t, r, http.MethodPost, "/api/mocks/"+created.ID+"/versions/"+oldVersionID+"/restore", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var restored mock.Definition
	if err := json.Unmarshal(rec.Body.Bytes(), &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if restored.PathPattern != "/original-path" || restored.Enabled {
		t.Fatalf("expected the restore to bring back the original shape, got %+v", restored)
	}

	// Confirm it's actually persisted, not just returned in the response.
	current, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if current.PathPattern != "/original-path" || current.Enabled {
		t.Fatalf("expected the current definition to be overwritten back to the old snapshot, got %+v", current)
	}
}

func TestRestoreVersionNotFoundForMissingMockOrVersion(t *testing.T) {
	r, store := newTestMocksRouter(t)
	created, err := store.Create(&mock.Definition{Name: "target", Method: "GET", PathPattern: "/target", Enabled: false})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Update(&mock.Definition{ID: created.ID, Name: "target", Method: "GET", PathPattern: "/target-changed", Enabled: true}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	versions, err := store.ListVersions(created.ID)
	if err != nil || len(versions) != 1 {
		t.Fatalf("expected 1 version, got %+v err=%v", versions, err)
	}
	realVersionID := versions[0].ID

	// Unknown mock id.
	recBadMock := doJSON(t, r, http.MethodPost, "/api/mocks/does-not-exist/versions/"+realVersionID+"/restore", nil)
	if recBadMock.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown mock id, got %d: %s", recBadMock.Code, recBadMock.Body.String())
	}

	// Unknown version id on a real mock.
	recBadVersion := doJSON(t, r, http.MethodPost, "/api/mocks/"+created.ID+"/versions/does-not-exist/restore", nil)
	if recBadVersion.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown version id, got %d: %s", recBadVersion.Code, recBadVersion.Body.String())
	}
}

func TestValidateMockShape(t *testing.T) {
	cases := []struct {
		name    string
		def     mock.Definition
		wantErr bool
	}{
		{"rest requires method and path", mock.Definition{ProtocolType: "rest"}, true},
		{"rest with method and path is valid", mock.Definition{ProtocolType: "rest", Method: "GET", PathPattern: "/x"}, false},
		// chi.Mux.MethodFunc PANICS on any method outside its fixed set — these guard the reason validRESTMethods exists.
		{"rest rejects a made-up method", mock.Definition{ProtocolType: "rest", Method: "PURGE", PathPattern: "/x"}, true},
		{"rest rejects a typo'd method", mock.Definition{ProtocolType: "rest", Method: "GETT", PathPattern: "/x"}, true},
		{"rest accepts a lowercase method chi would itself uppercase", mock.Definition{ProtocolType: "rest", Method: "get", PathPattern: "/x"}, false},
		{"rest accepts every method chi actually supports", mock.Definition{ProtocolType: "rest", Method: "TRACE", PathPattern: "/x"}, false},
		{"tcp requires a positive port", mock.Definition{ProtocolType: "tcp", TCP: &mock.TCPConfig{Port: 0}}, true},
		{"tcp with a positive port is valid", mock.Definition{ProtocolType: "tcp", TCP: &mock.TCPConfig{Port: 9000}}, false},
		{"smtp requires an smtp config", mock.Definition{ProtocolType: "smtp"}, true},
		{"smtp requires a positive port", mock.Definition{ProtocolType: "smtp", SMTP: &mock.SMTPConfig{Port: 0}}, true},
		{"smtp with a positive port is valid", mock.Definition{ProtocolType: "smtp", SMTP: &mock.SMTPConfig{Port: 2525}}, false},
		{"ws requires a pathPattern", mock.Definition{ProtocolType: "ws"}, true},
		{"ws with a pathPattern is valid, no method needed", mock.Definition{ProtocolType: "ws", PathPattern: "/echo"}, false},
		{"mqtt requires an mqtt config", mock.Definition{ProtocolType: "mqtt"}, true},
		{"mqtt requires a positive port", mock.Definition{ProtocolType: "mqtt", MQTT: &mock.MQTTConfig{Port: 0}}, true},
		{"mqtt with a positive port is valid", mock.Definition{ProtocolType: "mqtt", MQTT: &mock.MQTTConfig{Port: 1883}}, false},
		{"ftp requires an ftp config", mock.Definition{ProtocolType: "ftp"}, true},
		{"ftp requires a positive port", mock.Definition{ProtocolType: "ftp", FTP: &mock.FTPConfig{Port: 0}}, true},
		{"ftp with a positive port is valid", mock.Definition{ProtocolType: "ftp", FTP: &mock.FTPConfig{Port: 2121}}, false},
		{"kafka requires a kafka config", mock.Definition{ProtocolType: "kafka"}, true},
		{"kafka requires a positive port", mock.Definition{ProtocolType: "kafka", Kafka: &mock.KafkaConfig{Port: 0}}, true},
		{"kafka with a positive port is valid", mock.Definition{ProtocolType: "kafka", Kafka: &mock.KafkaConfig{Port: 9092}}, false},
		{"smpp requires an smpp config", mock.Definition{ProtocolType: "smpp"}, true},
		{"smpp requires a positive port", mock.Definition{ProtocolType: "smpp", SMPP: &mock.SMPPConfig{Port: 0}}, true},
		{"smpp with a positive port is valid", mock.Definition{ProtocolType: "smpp", SMPP: &mock.SMPPConfig{Port: 2775}}, false},
		{"diameter requires a diameter config", mock.Definition{ProtocolType: "diameter"}, true},
		{"diameter requires a positive port", mock.Definition{ProtocolType: "diameter", Diameter: &mock.DiameterConfig{Port: 0}}, true},
		{"diameter with a positive port is valid", mock.Definition{ProtocolType: "diameter", Diameter: &mock.DiameterConfig{Port: 3868}}, false},
		{"jms requires a jms config", mock.Definition{ProtocolType: "jms"}, true},
		{"jms requires a positive port", mock.Definition{ProtocolType: "jms", JMS: &mock.JMSConfig{Port: 0}}, true},
		{"jms with a positive port is valid", mock.Definition{ProtocolType: "jms", JMS: &mock.JMSConfig{Port: 5672}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateMockShape(&c.def)
			if c.wantErr && err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

// ---- strict decoding, validation and warnings ----

func TestCreateRejectsUnknownFieldsInsteadOfDroppingThem(t *testing.T) {
	r, _ := newTestMocksRouter(t)

	rec := doJSON(t, r, http.MethodPost, "/api/mocks", map[string]any{
		"name": "typo", "method": "GET", "pathPattern": "/typo", "record": true,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unsupported field, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`record`)) {
		t.Fatalf("expected the error to name the unknown field, got %s", rec.Body.String())
	}
}

func TestUpdateRejectsUnknownNestedFields(t *testing.T) {
	r, store := newTestMocksRouter(t)
	created, err := store.Create(&mock.Definition{Name: "u", Method: "GET", PathPattern: "/u", Enabled: true, ProtocolType: "rest"})
	if err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, r, http.MethodPut, "/api/mocks/"+created.ID, map[string]any{
		"name": "u", "method": "GET", "pathPattern": "/u", "protocolType": "rest",
		"response": map[string]any{"statusCode": 200, "bodyy": "typo"},
	})
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("bodyy")) {
		t.Fatalf("expected 400 naming bodyy, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateWarnsWhenAnHTTPCallbackHasNoBodyTemplate(t *testing.T) {
	r, _ := newTestMocksRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/mocks", map[string]any{
		"name": "cb", "method": "POST", "pathPattern": "/cb", "mode": "async",
		"asyncConfig": map[string]any{
			"ackResponse":        map[string]any{"statusCode": 202},
			"callbackTargetMode": "fixed", "callbackFixedUrl": "http://localhost:9/hook",
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("a missing body template should warn, not fail: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Warnings []string `json:"warnings"`
		ID       string   `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID == "" || len(resp.Warnings) != 1 || !bytes.Contains([]byte(resp.Warnings[0]), []byte("callbackBodyTemplate")) {
		t.Fatalf("expected one warning about callbackBodyTemplate, got %+v", resp)
	}

	// With a body template there is nothing to warn about.
	rec = doJSON(t, r, http.MethodPost, "/api/mocks", map[string]any{
		"name": "cb2", "method": "POST", "pathPattern": "/cb2", "mode": "async",
		"asyncConfig": map[string]any{
			"ackResponse":        map[string]any{"statusCode": 202},
			"callbackTargetMode": "fixed", "callbackFixedUrl": "http://localhost:9/hook",
			"callbackBodyTemplate": `{"ok":true}`,
		},
	})
	if bytes.Contains(rec.Body.Bytes(), []byte("warnings")) {
		t.Fatalf("did not expect warnings: %s", rec.Body.String())
	}
}

func TestSMTPBannerMustStartWithAReplyCode(t *testing.T) {
	r, _ := newTestMocksRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/mocks", map[string]any{
		"name": "bad-banner", "protocolType": "smtp",
		"smtp": map[string]any{"port": 2525, "banner": "Welcome to my server"},
	})
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("220")) {
		t.Fatalf("expected 400 mentioning 220, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, ok := range []string{"220 mail.test ESMTP", "220-hello", ""} {
		validate := validateMockShape(&mock.Definition{ProtocolType: "smtp", SMTP: &mock.SMTPConfig{Port: 2525, Banner: ok}})
		if validate != nil {
			t.Fatalf("banner %q should be valid: %v", ok, validate)
		}
	}
}

func TestProxyModeNeedsATargetAndDiameterRulesAreValidated(t *testing.T) {
	err := validateMockShape(&mock.Definition{ProtocolType: "rest", Method: "GET", PathPattern: "/p/*", Mode: "proxy"})
	if err == nil {
		t.Fatal("a proxy mock with no target should be rejected")
	}
	if err := validateMockShape(&mock.Definition{ProtocolType: "rest", Method: "GET", PathPattern: "/p/*", Mode: "proxy", Proxy: &mock.ProxyConfig{TargetBaseURL: "http://up.test"}}); err != nil {
		t.Fatalf("a proxy mock with a target should be valid: %v", err)
	}

	bad := &mock.Definition{ProtocolType: "diameter", Diameter: &mock.DiameterConfig{Port: 3868, Rules: []mock.DiameterRule{{FinalUnitAction: "explode"}}}}
	if err := validateMockShape(bad); err == nil || !bytes.Contains([]byte(err.Error()), []byte("finalUnitAction")) {
		t.Fatalf("expected a finalUnitAction error, got %v", err)
	}
	neg := &mock.Definition{ProtocolType: "diameter", Diameter: &mock.DiameterConfig{Port: 3868, Rules: []mock.DiameterRule{{DelayMs: -1}}}}
	if err := validateMockShape(neg); err == nil {
		t.Fatal("a negative delayMs should be rejected")
	}
	good := &mock.Definition{ProtocolType: "diameter", Diameter: &mock.DiameterConfig{Port: 3868, Rules: []mock.DiameterRule{{FinalUnitAction: "terminate"}}}}
	if err := validateMockShape(good); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}
}

func TestDiameterGrantFieldsSurviveCreateAndGet(t *testing.T) {
	engine.Register(&fakeEngine{name: "diameter"})
	r, store := newTestMocksRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/mocks", map[string]any{
		"name": "ocs", "protocolType": "diameter",
		"diameter": map[string]any{"port": 3868, "rules": []map[string]any{{
			"ccRequestType": 1, "ratingGroup": 100, "subscriptionIdMatch": "9198",
			"grantedTotalOctets": 5242880, "grantedTime": 3600, "validityTime": 600,
			"finalUnitAction": "terminate", "delayMs": 50,
		}}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created mock.Definition
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	ru := got.Diameter.Rules[0]
	if ru.RatingGroup != 100 || ru.SubscriptionIDMatch != "9198" || ru.GrantedTotalOctets == nil || *ru.GrantedTotalOctets != 5242880 ||
		ru.GrantedTime == nil || *ru.GrantedTime != 3600 || ru.ValidityTime == nil || *ru.ValidityTime != 600 ||
		ru.FinalUnitAction != "terminate" || ru.DelayMs != 50 {
		t.Fatalf("grant settings were not persisted: %+v", ru)
	}
}

func TestUpdateAcceptsARoundTrippedWarningsField(t *testing.T) {
	r, store := newTestMocksRouter(t)
	created, err := store.Create(&mock.Definition{Name: "rt", Method: "GET", PathPattern: "/rt", Enabled: true, ProtocolType: "rest"})
	if err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, r, http.MethodPut, "/api/mocks/"+created.ID, map[string]any{
		"name": "rt", "method": "GET", "pathPattern": "/rt", "protocolType": "rest", "warnings": []string{"old advice"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("a returned warnings field must not make the next save fail: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCallbackWarningIsAlsoAHeaderAndOnGet(t *testing.T) {
	r, _ := newTestMocksRouter(t)
	body := map[string]any{
		"name": "hdr", "method": "POST", "pathPattern": "/hdr", "mode": "async",
		"asyncConfig": map[string]any{
			"ackResponse":        map[string]any{"statusCode": 202},
			"callbackTargetMode": "fixed", "callbackFixedUrl": "http://localhost:9/hook",
		},
	}
	rec := doJSON(t, r, http.MethodPost, "/api/mocks", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Values("X-AirMock-Warning"); len(got) != 1 || !bytes.Contains([]byte(got[0]), []byte("callbackBodyTemplate")) {
		t.Fatalf("expected an X-AirMock-Warning header, got %v", got)
	}
	var created struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// The advice is discoverable later too, not only at save time.
	rec = doJSON(t, r, http.MethodGet, "/api/mocks/"+created.ID, nil)
	var got struct{ Warnings []string }
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Warnings) != 1 {
		t.Fatalf("expected GET to repeat the warning, got %s", rec.Body.String())
	}
}

func TestCreateRejectsATemplateThatDoesNotParse(t *testing.T) {
	r, _ := newTestMocksRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/mocks", map[string]any{
		"name": "bad-tpl", "method": "GET", "pathPattern": "/bad-tpl", "enabled": true,
		"response": map[string]any{"statusCode": 200, "bodyTemplate": "{{ if }"},
	})
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("response.bodyTemplate")) {
		t.Fatalf("expected 400 naming response.bodyTemplate, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAMockWithABrokenTemplateCanStillBeDisabled(t *testing.T) {
	r, store := newTestMocksRouter(t)
	broken, err := store.Create(&mock.Definition{
		Name: "legacy", Method: "GET", PathPattern: "/legacy", ProtocolType: "rest", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: "{{ if }"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"name": "legacy", "method": "GET", "pathPattern": "/legacy", "protocolType": "rest",
		"response": map[string]any{"statusCode": 200, "bodyTemplate": "{{ if }"},
	}
	body["enabled"] = true
	if rec := doJSON(t, r, http.MethodPut, "/api/mocks/"+broken.ID, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("saving it still enabled should fail, got %d", rec.Code)
	}
	body["enabled"] = false
	if rec := doJSON(t, r, http.MethodPut, "/api/mocks/"+broken.ID, body); rec.Code != http.StatusOK {
		t.Fatalf("disabling a broken mock must still work, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSMPPRuleAddressMatchTypeIsValidated(t *testing.T) {
	ok := &mock.Definition{ProtocolType: "smpp", SMPP: &mock.SMPPConfig{Port: 2775, Rules: []mock.SMPPRule{
		{DestAddrPattern: "1555*"}, {DestAddrPattern: "1-9", DestAddrMatchType: "range"}, {DestAddrPattern: `^1555\d+$`, DestAddrMatchType: "regex"},
	}}}
	if err := validateMockShape(ok); err != nil {
		t.Fatalf("valid rules rejected: %v", err)
	}
	if err := validateMockShape(&mock.Definition{ProtocolType: "smpp", SMPP: &mock.SMPPConfig{Port: 2775, Rules: []mock.SMPPRule{{DestAddrMatchType: "fuzzy"}}}}); err == nil {
		t.Fatal("an unknown destAddrMatchType must be rejected")
	}
	if err := validateMockShape(&mock.Definition{ProtocolType: "smpp", SMPP: &mock.SMPPConfig{Port: 2775, Rules: []mock.SMPPRule{{DestAddrPattern: "(", DestAddrMatchType: "regex"}}}}); err == nil {
		t.Fatal("an invalid regex must be rejected at save time")
	}
}

// ---- unchanged detection and dry-run ----

func TestSavingAnIdenticalMockIsReportedUnchangedAndAddsNoVersion(t *testing.T) {
	r, store := newTestMocksRouter(t)
	body := map[string]any{"name": "same", "method": "GET", "pathPattern": "/same", "enabled": true,
		"response": map[string]any{"statusCode": 200, "bodyTemplate": "ok"}}
	rec := doJSON(t, r, http.MethodPost, "/api/mocks", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created mock.Definition
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rec = doJSON(t, r, http.MethodPut, "/api/mocks/"+created.ID, body)
	if rec.Code != http.StatusOK || rec.Header().Get("X-AirMock-Unchanged") != "true" {
		t.Fatalf("an identical PUT should report unchanged, got %d headers=%v", rec.Code, rec.Header())
	}
	if vs, _ := store.ListVersions(created.ID); len(vs) != 0 {
		t.Fatalf("an unchanged save must not add a version-history entry, got %d", len(vs))
	}

	body["response"] = map[string]any{"statusCode": 200, "bodyTemplate": "changed"}
	rec = doJSON(t, r, http.MethodPut, "/api/mocks/"+created.ID, body)
	if rec.Code != http.StatusOK || rec.Header().Get("X-AirMock-Unchanged") != "" {
		t.Fatalf("a real change must not be reported unchanged, got %d headers=%v", rec.Code, rec.Header())
	}
	if vs, _ := store.ListVersions(created.ID); len(vs) != 1 {
		t.Fatalf("a real change should add one version, got %d", len(vs))
	}
}

func TestDryRunValidatesWithoutPersisting(t *testing.T) {
	r, store := newTestMocksRouter(t)
	good := map[string]any{"name": "dry", "method": "GET", "pathPattern": "/dry", "enabled": true}

	rec := doJSON(t, r, http.MethodPost, "/api/mocks?dryRun=true", good)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out["dryRun"] != true || out["action"] != "create" {
		t.Fatalf("expected a would-create report, got %d %s", rec.Code, rec.Body.String())
	}
	if list, _ := store.List(); len(list) != 0 {
		t.Fatalf("a dry run must not create anything, found %d", len(list))
	}

	bad := map[string]any{"name": "dry2", "method": "GET", "pathPattern": "/dry2", "response": map[string]any{"bodyTemplate": "{{ if }"}}
	if rec := doJSON(t, r, http.MethodPost, "/api/mocks?dryRun=true", bad); rec.Code != http.StatusBadRequest {
		t.Fatalf("a dry run must report validation errors, got %d", rec.Code)
	}

	created, _ := store.Create(&mock.Definition{Name: "taken", Method: "GET", PathPattern: "/taken", ProtocolType: "rest", Enabled: true})
	dup := map[string]any{"name": "taken", "method": "GET", "pathPattern": "/other"}
	if rec := doJSON(t, r, http.MethodPost, "/api/mocks?dryRun=true", dup); rec.Code != http.StatusConflict {
		t.Fatalf("a dry run must report a name conflict, got %d", rec.Code)
	}
	upd := map[string]any{"name": "taken", "method": "GET", "pathPattern": "/taken", "enabled": false}
	rec = doJSON(t, r, http.MethodPut, "/api/mocks/"+created.ID+"?dryRun=true", upd)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out["action"] != "update" {
		t.Fatalf("expected a would-update report, got %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := store.Get(created.ID); !got.Enabled {
		t.Fatal("a dry-run update must not change the stored mock")
	}
}

// ---- project basePath ----

func TestProjectBasePathIsAppliedToAMockCreatedThroughTheAPI(t *testing.T) {
	r, store := newTestMocksRouter(t)
	proj, err := store.CreateProject(&mock.Project{Name: "billing", BasePath: "/api/billing/"})
	if err != nil {
		t.Fatal(err)
	}
	create := func(name, path string) mock.Definition {
		rec := doJSON(t, r, http.MethodPost, "/api/mocks", map[string]any{
			"name": name, "method": "GET", "pathPattern": path, "projectId": proj.ID, "enabled": true,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, rec.Code, rec.Body.String())
		}
		var d mock.Definition
		_ = json.Unmarshal(rec.Body.Bytes(), &d)
		return d
	}

	cases := []struct{ name, in, want string }{
		{"plain", "/hello", "/api/billing/hello"},
		{"no leading slash", "world", "/api/billing/world"},
		{"nested with params", "/orders/{id}", "/api/billing/orders/{id}"},
		{"already prefixed (as the UI stores it)", "/api/billing/invoices", "/api/billing/invoices"},
		{"only a textual prefix, not a path segment", "/api/billingx/other", "/api/billing/api/billingx/other"},
		{"the root of the project", "/", "/api/billing"},
	}
	for _, c := range cases {
		if got := create(c.name, c.in).PathPattern; got != c.want {
			t.Errorf("%s: pathPattern %q -> %q, want %q", c.name, c.in, got, c.want)
		}
	}

	// A mock outside any project is untouched.
	rec := doJSON(t, r, http.MethodPost, "/api/mocks", map[string]any{"name": "free", "method": "GET", "pathPattern": "/free", "enabled": true})
	var free mock.Definition
	_ = json.Unmarshal(rec.Body.Bytes(), &free)
	if free.PathPattern != "/free" {
		t.Fatalf("a mock with no project must keep its path, got %q", free.PathPattern)
	}
}

func TestReApplyingAProjectMockIsIdempotentAndUnchanged(t *testing.T) {
	r, store := newTestMocksRouter(t)
	proj, _ := store.CreateProject(&mock.Project{Name: "p", BasePath: "/v1"})
	body := map[string]any{"name": "m", "method": "GET", "pathPattern": "/thing", "projectId": proj.ID, "enabled": true}
	rec := doJSON(t, r, http.MethodPost, "/api/mocks", body)
	var created mock.Definition
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.PathPattern != "/v1/thing" {
		t.Fatalf("got %q", created.PathPattern)
	}
	// The same file applied again (unprefixed path, as the author wrote it)
	// must normalise to the stored path and be reported unchanged.
	rec = doJSON(t, r, http.MethodPut, "/api/mocks/"+created.ID, body)
	if rec.Code != http.StatusOK || rec.Header().Get("X-AirMock-Unchanged") != "true" {
		t.Fatalf("expected unchanged on re-apply, got %d %v", rec.Code, rec.Header())
	}
}

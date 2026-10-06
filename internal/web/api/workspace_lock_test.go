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
	"github.com/addictedabhi/airmock/internal/storage"
	"github.com/addictedabhi/airmock/internal/wslock"
)

// doJSONWithCookies is doJSON (mocks_test.go) plus cookie support — the
// workspace-lock flow is inherently cookie-based (clientIDCookieName), so
// these tests need to carry a cookie from one request's response into the
// next, unlike every other handler test in this package.
func doJSONWithCookies(t *testing.T, r chi.Router, method, path string, body any, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// newTestWorkspaceLockRouters wires the apiclient AND mocks handlers onto
// one router sharing the same apiclient.Store/wslock.Tracker — mirroring
// how internal/server/server.go wires both against the same instances, so
// a lock set through one is actually enforced by the other, the same as in
// the real app.
func newTestWorkspaceLockRouters(t *testing.T) (chi.Router, *apiclient.Store, *mock.Store) {
	t.Helper()
	engine.Register(&fakeEngine{name: "http"})

	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	apiClientStore := apiclient.NewStore(db)
	mockStore := mock.NewStore(db)
	tracker := wslock.New()

	r := chi.NewRouter()
	r.Route("/api/apiclient", NewAPIClientHandler(apiClientStore, certs.NewStore(db), nil, tracker).Routes)
	r.Route("/api/mocks", NewMocksHandler(mockStore, apiClientStore, tracker).Routes)
	r.Route("/api/mock-projects", NewMockProjectsHandler(mockStore, nil, apiClientStore, tracker).Routes)
	return r, apiClientStore, mockStore
}

func TestLockWorkspaceHandlerRejectsShortPassword(t *testing.T) {
	r, apiClientStore, _ := newTestWorkspaceLockRouters(t)
	ws, err := apiClientStore.CreateWorkspace(&apiclient.Workspace{Name: "Team A"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	rec := doJSON(t, r, "POST", "/api/apiclient/workspaces/"+ws.ID+"/lock", map[string]string{
		"credentialType": "password", "newPassword": "ab",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a too-short password, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLockThenChangeRequiresCurrentPassword(t *testing.T) {
	r, apiClientStore, _ := newTestWorkspaceLockRouters(t)
	ws, err := apiClientStore.CreateWorkspace(&apiclient.Workspace{Name: "Team A"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	// First-time lock needs no current password.
	rec := doJSON(t, r, "POST", "/api/apiclient/workspaces/"+ws.ID+"/lock", map[string]string{
		"credentialType": "pin", "newPassword": "1234",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 setting the first lock, got %d: %s", rec.Code, rec.Body.String())
	}

	// Changing it without the current PIN is refused.
	rec = doJSON(t, r, "POST", "/api/apiclient/workspaces/"+ws.ID+"/lock", map[string]string{
		"credentialType": "pin", "newPassword": "5678",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 changing the lock without the current PIN, got %d: %s", rec.Code, rec.Body.String())
	}

	// With the correct current PIN, it succeeds.
	rec = doJSON(t, r, "POST", "/api/apiclient/workspaces/"+ws.ID+"/lock", map[string]string{
		"credentialType": "pin", "newPassword": "5678", "currentPassword": "1234",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 changing the lock with the correct current PIN, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRemoveLockRequiresCorrectPassword(t *testing.T) {
	r, apiClientStore, _ := newTestWorkspaceLockRouters(t)
	ws, err := apiClientStore.CreateWorkspace(&apiclient.Workspace{Name: "Team A"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	doJSON(t, r, "POST", "/api/apiclient/workspaces/"+ws.ID+"/lock", map[string]string{
		"credentialType": "password", "newPassword": "hunter22",
	})

	rec := doJSON(t, r, "POST", "/api/apiclient/workspaces/"+ws.ID+"/remove-lock", map[string]string{"password": "wrong"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 removing the lock with the wrong password, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, r, "POST", "/api/apiclient/workspaces/"+ws.ID+"/remove-lock", map[string]string{"password": "hunter22"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 removing the lock with the correct password, got %d: %s", rec.Code, rec.Body.String())
	}

	list, _ := apiClientStore.ListWorkspaces()
	for _, w := range list {
		if w.ID == ws.ID && w.Locked {
			t.Fatalf("expected workspace to no longer be locked after remove-lock, got %+v", w)
		}
	}
}

func TestMockUpdateBlockedByLockedWorkspaceUntilUnlocked(t *testing.T) {
	r, apiClientStore, mockStore := newTestWorkspaceLockRouters(t)
	ws, err := apiClientStore.CreateWorkspace(&apiclient.Workspace{Name: "Team A"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := apiClientStore.LockWorkspace(ws.ID, "pin", "1234"); err != nil {
		t.Fatalf("LockWorkspace: %v", err)
	}

	m, err := mockStore.Create(&mock.Definition{
		Name: "locked-mock", Method: "GET", PathPattern: "/locked",
		Enabled: true, WorkspaceID: ws.ID,
		Response: mock.ResponseTemplate{StatusCode: 200},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Editing it with no unlock at all is refused with the distinguishable
	// workspace_locked shape.
	m.Enabled = false
	rec := doJSON(t, r, "PUT", "/api/mocks/"+m.ID, m)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 editing a mock in a locked, un-unlocked workspace, got %d: %s", rec.Code, rec.Body.String())
	}
	var errBody map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errBody["code"] != "workspace_locked" || errBody["workspaceId"] != ws.ID {
		t.Fatalf("expected a workspace_locked error body naming %q, got %+v", ws.ID, errBody)
	}

	// Unlock with the wrong PIN — still refused.
	rec = doJSON(t, r, "POST", "/api/apiclient/workspaces/"+ws.ID+"/unlock", map[string]string{"password": "0000"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 unlocking with the wrong PIN, got %d: %s", rec.Code, rec.Body.String())
	}

	// Unlock with the right PIN, carrying the resulting cookie forward.
	rec = doJSON(t, r, "POST", "/api/apiclient/workspaces/"+ws.ID+"/unlock", map[string]string{"password": "1234"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 unlocking with the correct PIN, got %d: %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected the unlock response to set a client-id cookie")
	}

	// The SAME edit now succeeds once the client-id cookie is carried
	// along — no re-prompt needed for the rest of this "session".
	rec = doJSONWithCookies(t, r, "PUT", "/api/mocks/"+m.ID, m, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 editing the mock after unlocking, got %d: %s", rec.Code, rec.Body.String())
	}

	// A delete of the same mock, still carrying the cookie, also succeeds
	// without any further prompt.
	rec = doJSONWithCookies(t, r, "DELETE", "/api/mocks/"+m.ID, nil, cookies)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 deleting the mock after unlocking, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMockDeleteBlockedWithoutClientIDCookieAtAll(t *testing.T) {
	r, apiClientStore, mockStore := newTestWorkspaceLockRouters(t)
	ws, err := apiClientStore.CreateWorkspace(&apiclient.Workspace{Name: "Team A"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := apiClientStore.LockWorkspace(ws.ID, "password", "hunter22"); err != nil {
		t.Fatalf("LockWorkspace: %v", err)
	}
	m, err := mockStore.Create(&mock.Definition{
		Name: "locked-mock", Method: "GET", PathPattern: "/locked2",
		Enabled: true, WorkspaceID: ws.ID,
		Response: mock.ResponseTemplate{StatusCode: 200},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A request with no client-id cookie whatsoever (never called unlock)
	// must be refused, not treated as trivially unlocked.
	rec := doJSON(t, r, "DELETE", "/api/mocks/"+m.ID, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 deleting a locked mock with no prior unlock, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMockNotMappedToAWorkspaceIsUnaffectedByAnyLock(t *testing.T) {
	r, apiClientStore, mockStore := newTestWorkspaceLockRouters(t)
	ws, err := apiClientStore.CreateWorkspace(&apiclient.Workspace{Name: "Team A"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := apiClientStore.LockWorkspace(ws.ID, "pin", "1234"); err != nil {
		t.Fatalf("LockWorkspace: %v", err)
	}

	m, err := mockStore.Create(&mock.Definition{
		Name: "ungrouped-mock", Method: "GET", PathPattern: "/ungrouped",
		Enabled: true, // no WorkspaceID at all
		Response: mock.ResponseTemplate{StatusCode: 200},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	m.Enabled = false
	rec := doJSON(t, r, "PUT", "/api/mocks/"+m.ID, m)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 editing an ungrouped mock regardless of an unrelated locked workspace, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestDeleteWorkspaceItselfBlockedWhenLocked guards a real gap: deleting a
// locked workspace's own mocks was enforced, but deleting the WORKSPACE
// itself was not — DELETE /api/apiclient/workspaces/{id} had no lock check
// at all, so anyone could delete a locked workspace (and everything mapped
// to it) without ever being asked for its password. Also guards a second,
// subtler gap found afterward: a browser that had already unlocked this
// workspace once this session (e.g. to edit one mock in it) must NOT be
// able to ride that same unlock into deleting the whole workspace — every
// delete demands the current password fresh, right then, exactly like
// removeWorkspaceLock already does for just clearing the lock.
func TestDeleteWorkspaceItselfBlockedWhenLocked(t *testing.T) {
	r, apiClientStore, _ := newTestWorkspaceLockRouters(t)
	ws, err := apiClientStore.CreateWorkspace(&apiclient.Workspace{Name: "Team A"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := apiClientStore.LockWorkspace(ws.ID, "pin", "1234"); err != nil {
		t.Fatalf("LockWorkspace: %v", err)
	}

	rec := doJSON(t, r, "DELETE", "/api/apiclient/workspaces/"+ws.ID, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 deleting a locked workspace with no password, got %d: %s", rec.Code, rec.Body.String())
	}

	// Unlocking (as if to edit a mock in it) must NOT be enough on its own
	// to then delete the workspace with no password.
	rec = doJSON(t, r, "POST", "/api/apiclient/workspaces/"+ws.ID+"/unlock", map[string]string{"password": "1234"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 unlocking with the correct PIN, got %d: %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()

	rec = doJSONWithCookies(t, r, "DELETE", "/api/apiclient/workspaces/"+ws.ID, nil, cookies)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 deleting a locked workspace with no password even after a prior unlock, got %d: %s", rec.Code, rec.Body.String())
	}

	// The correct password, supplied with the delete itself, succeeds.
	rec = doJSONWithCookies(t, r, "DELETE", "/api/apiclient/workspaces/"+ws.ID, map[string]string{"password": "1234"}, cookies)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 deleting the workspace with its correct password, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestDeleteWorkspaceItselfRejectsWrongPassword confirms a locked
// workspace's delete rejects an incorrect password with a plain 401,
// rather than deleting anyway or 500ing.
func TestDeleteWorkspaceItselfRejectsWrongPassword(t *testing.T) {
	r, apiClientStore, _ := newTestWorkspaceLockRouters(t)
	ws, err := apiClientStore.CreateWorkspace(&apiclient.Workspace{Name: "Team A"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := apiClientStore.LockWorkspace(ws.ID, "password", "hunter2"); err != nil {
		t.Fatalf("LockWorkspace: %v", err)
	}

	rec := doJSON(t, r, "DELETE", "/api/apiclient/workspaces/"+ws.ID, map[string]string{"password": "wrong"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 deleting with the wrong password, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestMockInLockedProjectWorkspaceBlockedWithoutUnlock guards the
// project-level mapping: a mock with no WorkspaceID of its own, but that
// belongs to a Project mapped to a locked workspace, must still be
// protected — editing/deleting mocks individually was already enforced,
// but grouping many mocks under one project mapped to a workspace (instead
// of setting WorkspaceID on each one) previously had no such gate at all.
func TestMockInLockedProjectWorkspaceBlockedWithoutUnlock(t *testing.T) {
	r, apiClientStore, mockStore := newTestWorkspaceLockRouters(t)
	ws, err := apiClientStore.CreateWorkspace(&apiclient.Workspace{Name: "Team A"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := apiClientStore.LockWorkspace(ws.ID, "pin", "1234"); err != nil {
		t.Fatalf("LockWorkspace: %v", err)
	}
	project, err := mockStore.CreateProject(&mock.Project{Name: "Billing API", WorkspaceID: ws.ID})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	m, err := mockStore.Create(&mock.Definition{Name: "get-invoice", ProtocolType: "rest", Method: "GET", PathPattern: "/invoice", Enabled: true, ProjectID: project.ID})
	if err != nil {
		t.Fatalf("Create mock: %v", err)
	}

	rec := doJSON(t, r, "PUT", "/api/mocks/"+m.ID, map[string]any{
		"name": "get-invoice-renamed", "protocolType": "rest", "method": "GET", "pathPattern": "/invoice", "enabled": true, "projectId": project.ID,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 editing a mock in a project mapped to a locked workspace, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, r, "DELETE", "/api/mocks/"+m.ID, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 deleting a mock in a project mapped to a locked workspace, got %d: %s", rec.Code, rec.Body.String())
	}

	// Also guards the project resource itself: renaming/deleting the whole
	// project it belongs to needs the same unlock.
	rec = doJSON(t, r, "PUT", "/api/mock-projects/"+project.ID, map[string]any{"name": "Billing API v2", "workspaceId": ws.ID})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 editing a project mapped to a locked workspace, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, r, "DELETE", "/api/mock-projects/"+project.ID, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 deleting a project mapped to a locked workspace, got %d: %s", rec.Code, rec.Body.String())
	}

	// Unlocking the workspace (session-scoped, carried via cookie) then lets
	// all of the above through.
	rec = doJSON(t, r, "POST", "/api/apiclient/workspaces/"+ws.ID+"/unlock", map[string]string{"password": "1234"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 unlocking, got %d: %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()

	rec = doJSONWithCookies(t, r, "PUT", "/api/mocks/"+m.ID, map[string]any{
		"name": "get-invoice-renamed", "protocolType": "rest", "method": "GET", "pathPattern": "/invoice", "enabled": true, "projectId": project.ID,
	}, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 editing the mock after unlocking, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestBulkActionBlockedByLockedWorkspace guards a real gap: the bulk
// enable/disable/delete/move endpoint (the bulk-action bar's "Enable"/
// "Disable"/"Delete"/"Move to project" buttons) went straight to the
// store with no lock check at all, bypassing it entirely for anyone using
// bulk actions instead of a single mock's own controls.
func TestBulkActionBlockedByLockedWorkspace(t *testing.T) {
	r, apiClientStore, mockStore := newTestWorkspaceLockRouters(t)
	ws, err := apiClientStore.CreateWorkspace(&apiclient.Workspace{Name: "Team A"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := apiClientStore.LockWorkspace(ws.ID, "pin", "1234"); err != nil {
		t.Fatalf("LockWorkspace: %v", err)
	}
	m, err := mockStore.Create(&mock.Definition{Name: "locked-bulk-target", ProtocolType: "rest", Method: "GET", PathPattern: "/locked-bulk-target", Enabled: true, WorkspaceID: ws.ID})
	if err != nil {
		t.Fatalf("Create mock: %v", err)
	}

	rec := doJSON(t, r, "POST", "/api/mocks/bulk", map[string]any{"ids": []string{m.ID}, "action": "disable"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from the bulk endpoint itself (per-item failures, not a request-level error), got %d: %s", rec.Code, rec.Body.String())
	}
	var result bulkActionResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode bulk result: %v", err)
	}
	if len(result.Succeeded) != 0 || len(result.Failed) != 1 {
		t.Fatalf("expected the locked mock's bulk action to fail, not succeed, got %+v", result)
	}

	fresh, err := mockStore.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !fresh.Enabled {
		t.Fatalf("expected the mock to remain enabled — the bulk disable must NOT have actually applied")
	}

	// Unlocking lets it through.
	rec = doJSON(t, r, "POST", "/api/apiclient/workspaces/"+ws.ID+"/unlock", map[string]string{"password": "1234"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 unlocking, got %d: %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	rec = doJSONWithCookies(t, r, "POST", "/api/mocks/bulk", map[string]any{"ids": []string{m.ID}, "action": "disable"}, cookies)
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode bulk result: %v", err)
	}
	if len(result.Succeeded) != 1 {
		t.Fatalf("expected the bulk action to succeed after unlocking, got %+v", result)
	}
}

// TestCollectionAndEnvironmentMutationsBlockedByLockedWorkspace guards a
// real gap: only listCollections/listEnvironments ever checked the
// workspace lock — create/update/delete/move on a collection, and
// create/update/delete on an environment, went straight to the store with
// no lock check at all.
func TestCollectionAndEnvironmentMutationsBlockedByLockedWorkspace(t *testing.T) {
	r, apiClientStore, _ := newTestWorkspaceLockRouters(t)
	ws, err := apiClientStore.CreateWorkspace(&apiclient.Workspace{Name: "Team A"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	other, err := apiClientStore.CreateWorkspace(&apiclient.Workspace{Name: "Team B"})
	if err != nil {
		t.Fatalf("CreateWorkspace (other): %v", err)
	}
	if err := apiClientStore.LockWorkspace(ws.ID, "pin", "1234"); err != nil {
		t.Fatalf("LockWorkspace: %v", err)
	}

	rec := doJSON(t, r, "POST", "/api/apiclient/collections", map[string]any{"name": "c1", "workspaceId": ws.ID, "items": []any{}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 creating a collection directly in a locked workspace, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, r, "POST", "/api/apiclient/environments", map[string]any{"name": "e1", "workspaceId": ws.ID, "variables": map[string]string{}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 creating an environment directly in a locked workspace, got %d: %s", rec.Code, rec.Body.String())
	}

	// Create a collection/environment in the OTHER (unlocked) workspace,
	// then confirm editing/deleting/moving them in is still blocked.
	rec = doJSON(t, r, "POST", "/api/apiclient/collections", map[string]any{"name": "c2", "workspaceId": other.ID, "items": []any{}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating a collection in the unlocked workspace, got %d: %s", rec.Code, rec.Body.String())
	}
	var coll map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &coll); err != nil {
		t.Fatalf("decode collection: %v", err)
	}
	collID := coll["id"].(string)

	rec = doJSON(t, r, "POST", "/api/apiclient/environments", map[string]any{"name": "e2", "workspaceId": other.ID, "variables": map[string]string{}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating an environment in the unlocked workspace, got %d: %s", rec.Code, rec.Body.String())
	}
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode environment: %v", err)
	}
	envID := env["id"].(string)

	// Moving the (unlocked-workspace) collection INTO the locked workspace
	// must be blocked.
	rec = doJSON(t, r, "POST", "/api/apiclient/collections/"+collID+"/move", map[string]string{"workspaceId": ws.ID})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 moving a collection into a locked workspace, got %d: %s", rec.Code, rec.Body.String())
	}

	// Now actually move it in via the store directly (bypassing the API,
	// simulating "it was already there") to test edit/delete being blocked.
	if _, err := apiClientStore.MoveCollection(collID, ws.ID); err != nil {
		t.Fatalf("MoveCollection (direct): %v", err)
	}
	rec = doJSON(t, r, "PUT", "/api/apiclient/collections/"+collID, map[string]any{"name": "c2-renamed", "items": []any{}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 editing a collection in a locked workspace, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, r, "DELETE", "/api/apiclient/collections/"+collID, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 deleting a collection in a locked workspace, got %d: %s", rec.Code, rec.Body.String())
	}

	// An environment has no "move" endpoint at all (workspace_id isn't
	// mutable via UpdateEnvironment, by design — see its own doc comment),
	// so simulate "this environment already existed in the now-locked
	// workspace" by creating it directly through the store instead.
	lockedEnv, err := apiClientStore.CreateEnvironment(&apiclient.Environment{Name: "e3", WorkspaceID: ws.ID, Variables: map[string]string{}})
	if err != nil {
		t.Fatalf("CreateEnvironment (direct): %v", err)
	}
	envID = lockedEnv.ID
	rec = doJSON(t, r, "PUT", "/api/apiclient/environments/"+envID, map[string]any{"name": "e2-renamed", "variables": map[string]string{}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 editing an environment in a locked workspace, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, r, "DELETE", "/api/apiclient/environments/"+envID, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 deleting an environment in a locked workspace, got %d: %s", rec.Code, rec.Body.String())
	}

	// Unlocking lets all of it through.
	rec = doJSON(t, r, "POST", "/api/apiclient/workspaces/"+ws.ID+"/unlock", map[string]string{"password": "1234"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 unlocking, got %d: %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	rec = doJSONWithCookies(t, r, "DELETE", "/api/apiclient/collections/"+collID, nil, cookies)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 deleting the collection after unlocking, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSONWithCookies(t, r, "DELETE", "/api/apiclient/environments/"+envID, nil, cookies)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 deleting the environment after unlocking, got %d: %s", rec.Code, rec.Body.String())
	}
}

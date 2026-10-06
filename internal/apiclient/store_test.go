package apiclient

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/addictedabhi/airmock/internal/storage"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func TestDefaultWorkspaceSeededByMigration(t *testing.T) {
	s := newTestStore(t)

	list, err := s.ListWorkspaces()
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(list) != 1 || list[0].ID != DefaultWorkspaceID {
		t.Fatalf("expected exactly one seeded %q workspace, got %+v", DefaultWorkspaceID, list)
	}
}

func TestCollectionWithoutWorkspaceIDDefaultsToDefaultWorkspace(t *testing.T) {
	s := newTestStore(t)

	created, err := s.CreateCollection(&Collection{Name: "no workspace specified"})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if created.WorkspaceID != DefaultWorkspaceID {
		t.Fatalf("expected WorkspaceID to default to %q, got %q", DefaultWorkspaceID, created.WorkspaceID)
	}
}

func TestDescriptionAndExpectedStatusRoundTripThroughCollectionItems(t *testing.T) {
	s := newTestStore(t)

	created, err := s.CreateCollection(&Collection{
		Name: "notes-and-assertions-test",
		Items: []Item{
			{
				ID: "req-1", Type: ItemRequest, Name: "Health check",
				Description: "Hits the health endpoint — should always return 204.",
				Request:     &RequestSpec{Method: "GET", URL: "http://example.com/health", ExpectedStatus: 204},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	got, err := s.GetCollection(created.ID)
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("expected the request item to round-trip, got %+v", got.Items)
	}
	item := got.Items[0]
	if item.Description != "Hits the health endpoint — should always return 204." {
		t.Fatalf("expected Description to round-trip, got %q", item.Description)
	}
	if item.Request == nil || item.Request.ExpectedStatus != 204 {
		t.Fatalf("expected ExpectedStatus to round-trip, got %+v", item.Request)
	}
}

// TestCollectionVariablesRoundTripThroughCreateUpdateGetList guards against
// a real gap: Collection.Variables (collection-scoped {{var}} defaults, the
// same concept a Postman collection's top-level `variable[]` maps to on
// import) had nowhere to persist — the store only ever saved items_json.
func TestCollectionVariablesRoundTripThroughCreateUpdateGetList(t *testing.T) {
	s := newTestStore(t)

	created, err := s.CreateCollection(&Collection{
		Name:      "vars-test",
		Variables: map[string]string{"baseUrl": "https://api.example.com"},
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if created.Variables["baseUrl"] != "https://api.example.com" {
		t.Fatalf("expected Variables echoed back from Create, got %+v", created.Variables)
	}

	got, err := s.GetCollection(created.ID)
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if got.Variables["baseUrl"] != "https://api.example.com" {
		t.Fatalf("expected Variables to round-trip through Get, got %+v", got.Variables)
	}

	got.Variables["apiVersion"] = "v2"
	updated, err := s.UpdateCollection(got)
	if err != nil {
		t.Fatalf("UpdateCollection: %v", err)
	}
	if updated.Variables["baseUrl"] != "https://api.example.com" || updated.Variables["apiVersion"] != "v2" {
		t.Fatalf("expected both variables after update, got %+v", updated.Variables)
	}

	list, err := s.ListCollections(created.WorkspaceID)
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	var found *Collection
	for _, c := range list {
		if c.ID == created.ID {
			found = c
		}
	}
	if found == nil || found.Variables["apiVersion"] != "v2" {
		t.Fatalf("expected Variables to round-trip through List, got %+v", found)
	}
}

func TestExtractRulesRoundTripThroughCollectionItems(t *testing.T) {
	s := newTestStore(t)

	created, err := s.CreateCollection(&Collection{
		Name: "extract-rules-test",
		Items: []Item{
			{
				ID: "req-1", Type: ItemRequest, Name: "Login",
				Request: &RequestSpec{
					Method: "POST", URL: "http://example.com/login",
					ExtractRules: []ExtractRule{{Path: "data.token", Variable: "authToken"}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	got, err := s.GetCollection(created.ID)
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].Request == nil {
		t.Fatalf("expected the request item to round-trip, got %+v", got.Items)
	}
	rules := got.Items[0].Request.ExtractRules
	if len(rules) != 1 || rules[0].Path != "data.token" || rules[0].Variable != "authToken" {
		t.Fatalf("expected ExtractRules to round-trip through storage, got %+v", rules)
	}
}

func TestListCollectionsScopedByWorkspace(t *testing.T) {
	s := newTestStore(t)

	ws, err := s.CreateWorkspace(&Workspace{Name: "Project B"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	if _, err := s.CreateCollection(&Collection{Name: "in default"}); err != nil {
		t.Fatalf("CreateCollection (default): %v", err)
	}
	if _, err := s.CreateCollection(&Collection{Name: "in project b", WorkspaceID: ws.ID}); err != nil {
		t.Fatalf("CreateCollection (project b): %v", err)
	}

	defaultList, err := s.ListCollections(DefaultWorkspaceID)
	if err != nil {
		t.Fatalf("ListCollections(default): %v", err)
	}
	if len(defaultList) != 1 || defaultList[0].Name != "in default" {
		t.Fatalf("expected exactly the default-workspace collection, got %+v", defaultList)
	}

	projectBList, err := s.ListCollections(ws.ID)
	if err != nil {
		t.Fatalf("ListCollections(project b): %v", err)
	}
	if len(projectBList) != 1 || projectBList[0].Name != "in project b" {
		t.Fatalf("expected exactly the project-b collection, got %+v", projectBList)
	}

	all, err := s.ListCollections("")
	if err != nil {
		t.Fatalf("ListCollections(\"\"): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected an empty workspaceID to return every collection, got %d", len(all))
	}
}

func TestDeleteWorkspaceCascadesCollectionsAndEnvironments(t *testing.T) {
	s := newTestStore(t)

	ws, err := s.CreateWorkspace(&Workspace{Name: "Temp Project"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if _, err := s.CreateCollection(&Collection{Name: "c1", WorkspaceID: ws.ID}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if _, err := s.CreateEnvironment(&Environment{Name: "e1", WorkspaceID: ws.ID}); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}

	if err := s.DeleteWorkspace(ws.ID); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}

	collections, err := s.ListCollections(ws.ID)
	if err != nil {
		t.Fatalf("ListCollections after delete: %v", err)
	}
	if len(collections) != 0 {
		t.Fatalf("expected the workspace's collections to be gone, got %+v", collections)
	}
	environments, err := s.ListEnvironments(ws.ID)
	if err != nil {
		t.Fatalf("ListEnvironments after delete: %v", err)
	}
	if len(environments) != 0 {
		t.Fatalf("expected the workspace's environments to be gone, got %+v", environments)
	}

	workspaces, err := s.ListWorkspaces()
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(workspaces) != 1 || workspaces[0].ID != DefaultWorkspaceID {
		t.Fatalf("expected only the default workspace to remain, got %+v", workspaces)
	}
}

// TestDeleteWorkspaceCleansUpItsCollectionsLoadTestRuns guards a real gap:
// DeleteWorkspace deletes collections via raw SQL rather than calling
// DeleteCollection, so without this explicit cleanup a deleted workspace's
// collections' load-test-run history (and those runs' samples) would sit
// orphaned forever — mirrors TestDeleteCollectionCleansUpItsLoadTestRuns in
// loadtest_runs_test.go.
func TestDeleteWorkspaceCleansUpItsCollectionsLoadTestRuns(t *testing.T) {
	s := newTestStore(t)

	ws, err := s.CreateWorkspace(&Workspace{Name: "Temp Project"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	coll, err := s.CreateCollection(&Collection{Name: "c1", WorkspaceID: ws.ID})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	run := &LoadTestRun{CollectionID: coll.ID, ItemID: "item-1", Method: "GET", URL: "http://x", Config: LoadTestConfig{}, Result: sampleLoadTestResult()}
	if err := s.SaveLoadTestRun(run); err != nil {
		t.Fatalf("SaveLoadTestRun: %v", err)
	}

	if err := s.DeleteWorkspace(ws.ID); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}

	if _, err := s.GetLoadTestRun(run.ID); err != ErrNotFound {
		t.Fatalf("expected the workspace's collection's load test run to be cleaned up, got %v", err)
	}
	var sampleCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM load_test_run_samples WHERE run_id = ?`, run.ID).Scan(&sampleCount); err != nil {
		t.Fatalf("count samples: %v", err)
	}
	if sampleCount != 0 {
		t.Fatalf("expected the run's samples to be deleted too, got %d left", sampleCount)
	}
}

func TestDeleteWorkspaceRefusesToDeleteTheLastOne(t *testing.T) {
	s := newTestStore(t)

	// The only workspace present here is also Default, so this exercises
	// ErrDefaultWorkspace (checked first) rather than ErrLastWorkspace —
	// see TestDeleteWorkspaceRefusesToDeleteDefaultEvenWhenNotLast for a
	// non-default sole workspace hitting ErrLastWorkspace instead.
	err := s.DeleteWorkspace(DefaultWorkspaceID)
	if !errors.Is(err, ErrDefaultWorkspace) {
		t.Fatalf("expected ErrDefaultWorkspace deleting the only workspace (which is Default), got %v", err)
	}

	list, err := s.ListWorkspaces()
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected the default workspace to still exist, got %+v", list)
	}
}

func TestDeleteWorkspaceRefusesToDeleteDefaultEvenWhenNotLast(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.CreateWorkspace(&Workspace{Name: "Second"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	// Even with another workspace available (so ErrLastWorkspace wouldn't
	// apply), Default must still be undeletable.
	err := s.DeleteWorkspace(DefaultWorkspaceID)
	if !errors.Is(err, ErrDefaultWorkspace) {
		t.Fatalf("expected ErrDefaultWorkspace deleting Default even with other workspaces present, got %v", err)
	}

	list, err := s.ListWorkspaces()
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected both workspaces to still exist, got %+v", list)
	}
}

func TestCreateCollectionRejectsDuplicateNameWithinSameWorkspace(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.CreateCollection(&Collection{Name: "Orders"}); err != nil {
		t.Fatalf("first CreateCollection: %v", err)
	}
	_, err := s.CreateCollection(&Collection{Name: " orders "})
	if !errors.Is(err, ErrDuplicateCollectionName) {
		t.Fatalf("expected ErrDuplicateCollectionName, got %v", err)
	}
}

func TestCreateCollectionAllowsSameNameInDifferentWorkspace(t *testing.T) {
	s := newTestStore(t)

	ws, err := s.CreateWorkspace(&Workspace{Name: "Other"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if _, err := s.CreateCollection(&Collection{Name: "Orders"}); err != nil {
		t.Fatalf("CreateCollection (default workspace): %v", err)
	}
	if _, err := s.CreateCollection(&Collection{Name: "Orders", WorkspaceID: ws.ID}); err != nil {
		t.Fatalf("expected the same name to be allowed in a different workspace, got %v", err)
	}
}

func TestUpdateCollectionRejectsRenamingIntoAnExistingName(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.CreateCollection(&Collection{Name: "Orders"}); err != nil {
		t.Fatalf("create Orders: %v", err)
	}
	invoices, err := s.CreateCollection(&Collection{Name: "Invoices"})
	if err != nil {
		t.Fatalf("create Invoices: %v", err)
	}

	invoices.Name = "Orders"
	if _, err := s.UpdateCollection(invoices); !errors.Is(err, ErrDuplicateCollectionName) {
		t.Fatalf("expected ErrDuplicateCollectionName, got %v", err)
	}
}

func TestCreateEnvironmentRejectsDuplicateNameWithinSameWorkspace(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.CreateEnvironment(&Environment{Name: "Staging"}); err != nil {
		t.Fatalf("first CreateEnvironment: %v", err)
	}
	_, err := s.CreateEnvironment(&Environment{Name: " staging "})
	if !errors.Is(err, ErrDuplicateEnvironmentName) {
		t.Fatalf("expected ErrDuplicateEnvironmentName, got %v", err)
	}
}

func TestCreateWorkspaceRejectsDuplicateName(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.CreateWorkspace(&Workspace{Name: "Client B"}); err != nil {
		t.Fatalf("first CreateWorkspace: %v", err)
	}
	_, err := s.CreateWorkspace(&Workspace{Name: " client b "})
	if !errors.Is(err, ErrDuplicateWorkspaceName) {
		t.Fatalf("expected ErrDuplicateWorkspaceName, got %v", err)
	}
}

func TestLockWorkspaceThenVerifyPassword(t *testing.T) {
	s := newTestStore(t)
	ws, err := s.CreateWorkspace(&Workspace{Name: "Secret Client"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	if err := s.LockWorkspace(ws.ID, "pin", "1234"); err != nil {
		t.Fatalf("LockWorkspace: %v", err)
	}

	if err := s.VerifyWorkspacePassword(ws.ID, "1234"); err != nil {
		t.Fatalf("expected correct password to verify, got %v", err)
	}
	if err := s.VerifyWorkspacePassword(ws.ID, "9999"); !errors.Is(err, ErrIncorrectWorkspacePassword) {
		t.Fatalf("expected ErrIncorrectWorkspacePassword for wrong pin, got %v", err)
	}

	list, err := s.ListWorkspaces()
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	var found *Workspace
	for _, w := range list {
		if w.ID == ws.ID {
			found = w
		}
	}
	if found == nil || !found.Locked || found.LockCredentialType != "pin" {
		t.Fatalf("expected ListWorkspaces to report the workspace as locked with credentialType=pin, got %+v", found)
	}
}

func TestVerifyWorkspacePasswordOnUnlockedWorkspace(t *testing.T) {
	s := newTestStore(t)
	ws, err := s.CreateWorkspace(&Workspace{Name: "Open Client"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := s.VerifyWorkspacePassword(ws.ID, "anything"); !errors.Is(err, ErrWorkspaceNotLocked) {
		t.Fatalf("expected ErrWorkspaceNotLocked, got %v", err)
	}
}

func TestClearWorkspaceLockRefusesWhenNotLocked(t *testing.T) {
	s := newTestStore(t)
	ws, err := s.CreateWorkspace(&Workspace{Name: "Open Client"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := s.ClearWorkspaceLock(ws.ID); !errors.Is(err, ErrWorkspaceNotLocked) {
		t.Fatalf("expected ErrWorkspaceNotLocked, got %v", err)
	}
}

func TestClearWorkspaceLockRemovesLock(t *testing.T) {
	s := newTestStore(t)
	ws, err := s.CreateWorkspace(&Workspace{Name: "Secret Client"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := s.LockWorkspace(ws.ID, "password", "hunter22"); err != nil {
		t.Fatalf("LockWorkspace: %v", err)
	}
	if err := s.ClearWorkspaceLock(ws.ID); err != nil {
		t.Fatalf("ClearWorkspaceLock: %v", err)
	}
	if err := s.VerifyWorkspacePassword(ws.ID, "hunter22"); !errors.Is(err, ErrWorkspaceNotLocked) {
		t.Fatalf("expected ErrWorkspaceNotLocked after clearing the lock, got %v", err)
	}
}

func TestWorkspaceLockRemembersPinLengthAndLearnsItForLegacyLocks(t *testing.T) {
	s := newTestStore(t)
	ws, err := s.CreateWorkspace(&Workspace{Name: "Six Digit"})
	if err != nil {
		t.Fatal(err)
	}
	pinLength := func() int {
		list, _ := s.ListWorkspaces()
		for _, w := range list {
			if w.ID == ws.ID {
				return w.LockPinLength
			}
		}
		return -1
	}
	if err := s.LockWorkspace(ws.ID, "pin", "123456"); err != nil {
		t.Fatal(err)
	}
	if got := pinLength(); got != 6 {
		t.Fatalf("LockPinLength = %d, want 6", got)
	}
	if err := s.LockWorkspace(ws.ID, "password", "a longer password"); err != nil {
		t.Fatal(err)
	}
	if got := pinLength(); got != 0 {
		t.Fatalf("a password lock has no PIN length, got %d", got)
	}

	// A PIN lock from before the length was recorded: unknown until a correct unlock.
	if err := s.LockWorkspace(ws.ID, "pin", "4321"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE workspaces SET lock_pin_length = 0 WHERE id = ?`, ws.ID); err != nil {
		t.Fatal(err)
	}
	if got := pinLength(); got != 0 {
		t.Fatalf("legacy lock length should be unknown, got %d", got)
	}
	if err := s.VerifyWorkspacePassword(ws.ID, "0000"); err == nil {
		t.Fatal("wrong PIN must fail")
	}
	if got := pinLength(); got != 0 {
		t.Fatalf("a failed unlock must not teach a length, got %d", got)
	}
	if err := s.VerifyWorkspacePassword(ws.ID, "4321"); err != nil {
		t.Fatal(err)
	}
	if got := pinLength(); got != 4 {
		t.Fatalf("LockPinLength after a correct unlock = %d, want 4", got)
	}
	if err := s.ClearWorkspaceLock(ws.ID); err != nil {
		t.Fatal(err)
	}
	if got := pinLength(); got != 0 {
		t.Fatalf("a cleared lock has no PIN length, got %d", got)
	}
}

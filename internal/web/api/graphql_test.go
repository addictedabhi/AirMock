package api

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/storage"
)

const fixtureGraphQLSDL = `
type Query {
  hello: String
}
`

func newTestGraphQLRouter(t *testing.T) (chi.Router, *mock.Store) {
	t.Helper()
	engine.Register(&fakeEngine{name: "http"})
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := mock.NewStore(db)
	r := chi.NewRouter()
	r.Route("/api/graphql-schema", NewGraphQLHandler(store).Routes)
	return r, store
}

// TestGraphQLImportAssignsRequestedProject mirrors the same WSDL/OpenAPI
// import behavior: an optional projectId groups every scaffolded mock
// under one Mock Project instead of leaving them all Ungrouped.
func TestGraphQLImportAssignsRequestedProject(t *testing.T) {
	r, store := newTestGraphQLRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/graphql-schema/import", map[string]any{
		"sdlContent": fixtureGraphQLSDL, "pathPattern": "/graphql", "projectId": "proj-1",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	all, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || all[0].ProjectID != "proj-1" {
		t.Fatalf("expected the scaffolded mock to be assigned to proj-1, got %+v", all)
	}
}

func TestGraphQLImportDefaultsToUngroupedWithoutAProjectID(t *testing.T) {
	r, store := newTestGraphQLRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/graphql-schema/import", map[string]any{
		"sdlContent": fixtureGraphQLSDL, "pathPattern": "/graphql",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	all, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || all[0].ProjectID != "" {
		t.Fatalf("expected the scaffolded mock to be ungrouped by default, got %+v", all)
	}
}

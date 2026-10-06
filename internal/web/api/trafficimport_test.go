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

func newTestTrafficImportRouter(t *testing.T) (chi.Router, *mock.Store) {
	t.Helper()
	engine.Register(&fakeEngine{name: "http"})

	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := mock.NewStore(db)
	r := chi.NewRouter()
	r.Route("/api/traffic-import", NewTrafficImportHandler(store).Routes)
	return r, store
}

const sampleHARWithBody = `{
  "log": {
    "entries": [
      {
        "request": {
          "method": "POST",
          "url": "https://api.example.com/orders",
          "postData": { "text": "{\"customerId\":\"c1\",\"amount\":42}" }
        },
        "response": {
          "status": 201,
          "content": { "mimeType": "application/json", "text": "{\"id\":\"o1\"}" }
        }
      }
    ]
  }
}`

// TestImportHARDerivesValidationFromCapturedBody guards against a real gap:
// the captured request body was read nowhere at all — POST/PUT payloads in
// a HAR file were discarded entirely, so a scaffolded mock carried no
// information at all about what a working request actually looks like.
func TestImportHARDerivesValidationFromCapturedBody(t *testing.T) {
	r, store := newTestTrafficImportRouter(t)

	rec := doJSON(t, r, http.MethodPost, "/api/traffic-import/har", map[string]any{"content": sampleHARWithBody})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	defs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(defs) != 1 {
		t.Fatalf("expected exactly 1 scaffolded mock, got %d", len(defs))
	}
	d := defs[0]
	if d.Method != "POST" || d.PathPattern != "/orders" || d.Response.StatusCode != 201 {
		t.Fatalf("unexpected scaffolded mock: %+v", d)
	}
	if len(d.Validation) != 2 {
		t.Fatalf("expected 2 required-field rules derived from the captured body, got %+v", d.Validation)
	}
	fields := map[string]bool{}
	for _, rule := range d.Validation {
		if !rule.Required {
			t.Fatalf("expected every derived rule to be Required, got %+v", rule)
		}
		fields[rule.Field] = true
	}
	if !fields["body.customerId"] || !fields["body.amount"] {
		t.Fatalf("expected rules for body.customerId and body.amount, got %+v", d.Validation)
	}
}

// TestImportHARWithoutBodyHasNoValidation confirms a GET-only capture (no
// request body at all) doesn't fabricate any validation rules.
func TestImportHARWithoutBodyHasNoValidation(t *testing.T) {
	r, store := newTestTrafficImportRouter(t)

	har := `{"log":{"entries":[{"request":{"method":"GET","url":"https://api.example.com/health"},"response":{"status":200,"content":{"mimeType":"application/json","text":"{}"}}}]}}`
	rec := doJSON(t, r, http.MethodPost, "/api/traffic-import/har", map[string]any{"content": har})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	defs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(defs) != 1 || len(defs[0].Validation) != 0 {
		t.Fatalf("expected no validation rules for a bodyless GET capture, got %+v", defs)
	}
}

func TestImportHARInvalidContentReturnsBadRequest(t *testing.T) {
	r, _ := newTestTrafficImportRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/traffic-import/har", map[string]any{"content": "not a har file"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unparseable content, got %d", rec.Code)
	}
}

func TestImportPostmanExamplesCreatesMock(t *testing.T) {
	r, store := newTestTrafficImportRouter(t)

	postman := `{"item":[{"name":"Get widget","request":{"method":"GET","url":{"raw":"https://api.example.com/widgets/1"}},"response":[{"code":200,"body":"{\"id\":1}"}]}]}`
	rec := doJSON(t, r, http.MethodPost, "/api/traffic-import/postman-examples", map[string]any{"content": postman})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	defs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(defs) != 1 || defs[0].PathPattern != "/widgets/1" {
		t.Fatalf("unexpected scaffolded mock: %+v", defs)
	}
}

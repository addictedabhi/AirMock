package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/scheduledevent"
	"github.com/addictedabhi/airmock/internal/storage"
)

func newTestScheduledEventsRouter(t *testing.T) chi.Router {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := scheduledevent.NewStore(db)
	worker := scheduledevent.NewWorker(store, hitlog.NewStore(db))
	r := chi.NewRouter()
	r.Route("/api/scheduled-events", NewScheduledEventsHandler(store, worker).Routes)
	return r
}

func TestScheduledEventsHandler_createValidateListDelete(t *testing.T) {
	r := newTestScheduledEventsRouter(t)

	rec := doJSON(t, r, http.MethodPost, "/api/scheduled-events", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing fields, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, r, http.MethodPost, "/api/scheduled-events", map[string]any{
		"name": "heartbeat", "targetUrl": "http://example.com/hook", "intervalSecs": 30,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created scheduledevent.Event
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created: %v", err)
	}
	if created.Method != "POST" {
		t.Fatalf("expected default method POST, got %q", created.Method)
	}

	rec = doJSON(t, r, http.MethodGet, "/api/scheduled-events", nil)
	var list []scheduledevent.Event
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 event in list, got %d", len(list))
	}

	rec = doJSON(t, r, http.MethodDelete, "/api/scheduled-events/"+created.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	rec = doJSON(t, r, http.MethodGet, "/api/scheduled-events/"+created.ID, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", rec.Code)
	}
}

func TestScheduledEventsHandler_fireNow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer upstream.Close()

	r := newTestScheduledEventsRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/scheduled-events", map[string]any{
		"name": "pinger", "targetUrl": upstream.URL, "intervalSecs": 3600,
	})
	var created scheduledevent.Event
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created: %v", err)
	}

	rec = doJSON(t, r, http.MethodPost, "/api/scheduled-events/"+created.ID+"/fire-now", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var fired scheduledevent.Event
	if err := json.Unmarshal(rec.Body.Bytes(), &fired); err != nil {
		t.Fatalf("unmarshal fired: %v", err)
	}
	if fired.LastStatus != http.StatusAccepted {
		t.Fatalf("expected LastStatus 202, got %d", fired.LastStatus)
	}
}

func TestScheduledEventRejectsABodyTemplateThatDoesNotParse(t *testing.T) {
	r := newTestScheduledEventsRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/scheduled-events", map[string]any{
		"name": "bad", "targetUrl": "http://example.com/hook", "intervalSecs": 30, "bodyTemplate": "{{ if }",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unparseable bodyTemplate, got %d: %s", rec.Code, rec.Body.String())
	}
}

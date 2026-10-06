package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeScheduledEventsServer is a minimal in-memory stand-in for the real
// /api/scheduled-events admin endpoint — enough to exercise the CLI's
// list/export/apply HTTP calls without a full server.New(...) instance.
func fakeScheduledEventsServer(t *testing.T) (*httptest.Server, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	events := map[string]map[string]any{}
	nextID := 1

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/scheduled-events":
			list := make([]map[string]any, 0, len(events))
			for _, e := range events {
				list = append(list, e)
			}
			json.NewEncoder(w).Encode(list)
		case r.Method == http.MethodPost && r.URL.Path == "/api/scheduled-events":
			var e map[string]any
			json.NewDecoder(r.Body).Decode(&e)
			id := "id-" + itoa(nextID)
			nextID++
			e["id"] = id
			events[id] = e
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(e)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/scheduled-events/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/scheduled-events/")
			var e map[string]any
			json.NewDecoder(r.Body).Decode(&e)
			e["id"] = id
			events[id] = e
			json.NewEncoder(w).Encode(e)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	snapshot := func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		list := make([]map[string]any, 0, len(events))
		for _, e := range events {
			list = append(list, e)
		}
		return list
	}
	t.Cleanup(srv.Close)
	return srv, snapshot
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// TestApplyScheduledEventsFileCreatesThenUpdatesByName guards the exact
// behavior "mocks apply" already relies on, now shared via
// applyResourceByName: applying the same file twice must create once, then
// update the same event in place on the second run rather than duplicating
// it — the point of a mocks-as-code apply being safely re-runnable.
func TestApplyScheduledEventsFileCreatesThenUpdatesByName(t *testing.T) {
	srv, snapshot := fakeScheduledEventsServer(t)

	file := filepath.Join(t.TempDir(), "events.json")
	body := `{"scheduledEvents": [{"name": "Nightly ping", "enabled": true, "intervalSecs": 3600, "targetUrl": "https://example.com/webhook", "method": "POST"}]}`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := applyScheduledEventsFile(srv.URL, file); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if got := snapshot(); len(got) != 1 {
		t.Fatalf("expected exactly 1 event after the first apply, got %d: %+v", len(got), got)
	}

	updated := `{"scheduledEvents": [{"name": "nightly PING  ", "enabled": true, "intervalSecs": 60, "targetUrl": "https://example.com/webhook-v2", "method": "POST"}]}`
	if err := os.WriteFile(file, []byte(updated), 0o644); err != nil {
		t.Fatalf("write updated fixture: %v", err)
	}
	if err := applyScheduledEventsFile(srv.URL, file); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	got := snapshot()
	if len(got) != 1 {
		t.Fatalf("expected the second apply to update the same event by case/whitespace-insensitive name match, not create a second one — got %d: %+v", len(got), got)
	}
	if got[0]["intervalSecs"].(float64) != 60 || got[0]["targetUrl"] != "https://example.com/webhook-v2" {
		t.Fatalf("expected the event's fields to reflect the second file's values, got %+v", got[0])
	}
}

// TestApplyScheduledEventsFileMergesDuplicateNamesWithinTheSameFile guards
// against a real gap: existingByName is fetched once, before the apply
// loop, so two entries in the SAME file sharing a (normalized) name both
// looked "not found" and both got POSTed as separate creates — scheduled
// events have no server-side name-uniqueness check at all, so this
// silently created two duplicate rows with no way for a later "apply" run
// to ever reach the second one again. The fix records each create's own ID
// back into existingByName immediately, so a same-file duplicate becomes an
// update against the just-created row instead of a second create.
func TestApplyScheduledEventsFileMergesDuplicateNamesWithinTheSameFile(t *testing.T) {
	srv, snapshot := fakeScheduledEventsServer(t)

	file := filepath.Join(t.TempDir(), "events.json")
	body := `{"scheduledEvents": [
		{"name": "Nightly ping", "enabled": true, "intervalSecs": 3600, "targetUrl": "https://example.com/first", "method": "POST"},
		{"name": "nightly PING", "enabled": true, "intervalSecs": 60, "targetUrl": "https://example.com/second", "method": "POST"}
	]}`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := applyScheduledEventsFile(srv.URL, file); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := snapshot()
	if len(got) != 1 {
		t.Fatalf("expected the two same-name entries to merge into exactly 1 event, got %d: %+v", len(got), got)
	}
	if got[0]["targetUrl"] != "https://example.com/second" {
		t.Fatalf("expected the second (later) entry's values to win, got %+v", got[0])
	}
}

func TestApplyScheduledEventsFileRejectsUnknownWrapperKey(t *testing.T) {
	srv, _ := fakeScheduledEventsServer(t)
	file := filepath.Join(t.TempDir(), "events.json")
	if err := os.WriteFile(file, []byte(`{"mocks": [{"name": "wrong wrapper"}]}`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := applyScheduledEventsFile(srv.URL, file); err == nil {
		t.Fatal("expected an error for a file using the mocks wrapper key instead of scheduledEvents")
	}
}

func TestApplyScheduledEventsFileAcceptsBareArray(t *testing.T) {
	srv, snapshot := fakeScheduledEventsServer(t)
	file := filepath.Join(t.TempDir(), "events.json")
	body := `[{"name": "Bare array event", "enabled": true, "intervalSecs": 10, "targetUrl": "https://example.com/x", "method": "GET"}]`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := applyScheduledEventsFile(srv.URL, file); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := snapshot(); len(got) != 1 || got[0]["name"] != "Bare array event" {
		t.Fatalf("expected the bare-array event applied, got %+v", got)
	}
}

func TestWrapAsResourceProducesExpectedShape(t *testing.T) {
	out, err := wrapAsResource([]byte(`[{"name":"a"},{"name":"b"}]`), "scheduledEvents")
	if err != nil {
		t.Fatalf("wrapAsResource: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode wrapped output: %v", err)
	}
	list, ok := decoded["scheduledEvents"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("expected a scheduledEvents array with 2 entries, got %+v", decoded)
	}
}

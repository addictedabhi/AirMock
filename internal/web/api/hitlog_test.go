package api

import (
	"encoding/csv"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/storage"
)

func newTestHitLogRouter(t *testing.T) (chi.Router, *hitlog.Store) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	hitStore := hitlog.NewStore(db)
	mockStore := mock.NewStore(db)
	r := chi.NewRouter()
	r.Route("/api/hitlog", NewHitLogHandler(hitStore, mockStore, hitlog.NewBroadcaster()).Routes)
	return r, hitStore
}

func mustRecordEntry(t *testing.T, store *hitlog.Store, e *hitlog.Entry) {
	t.Helper()
	if err := store.Record(e); err != nil {
		t.Fatalf("Record: %v", err)
	}
}

func TestHitLogExportCSVIncludesEveryEntry(t *testing.T) {
	r, store := newTestHitLogRouter(t)
	mustRecordEntry(t, store, &hitlog.Entry{
		ProtocolType: "rest", Direction: "inbound", Method: "GET", Path: "/orders/1",
		ResponseStatus: 200, LatencyMs: 12, RequestHeaders: map[string]string{"X-Test": "a"},
	})
	mustRecordEntry(t, store, &hitlog.Entry{
		ProtocolType: "rest", Direction: "inbound", Method: "POST", Path: "/orders",
		ResponseStatus: 201, LatencyMs: 34,
	})

	req := httptest.NewRequest("GET", "/api/hitlog/export", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv" {
		t.Fatalf("expected text/csv content type, got %q", ct)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "airmock-hitlog-export.csv") {
		t.Fatalf("expected a CSV attachment filename, got %q", rec.Header().Get("Content-Disposition"))
	}

	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	if len(rows) != 3 { // header + 2 entries
		t.Fatalf("expected a header row plus 2 entry rows, got %d rows: %+v", len(rows), rows)
	}
	if rows[0][0] != "id" || rows[0][5] != "path" {
		t.Fatalf("expected the documented column header, got %+v", rows[0])
	}
	paths := []string{rows[1][5], rows[2][5]}
	if !(paths[0] == "/orders/1" || paths[1] == "/orders/1") {
		t.Fatalf("expected /orders/1 among the exported rows, got %+v", paths)
	}
}

func TestHitLogExportJSONFormat(t *testing.T) {
	r, store := newTestHitLogRouter(t)
	mustRecordEntry(t, store, &hitlog.Entry{ProtocolType: "tcp", Direction: "inbound", Path: "PING", ResponseStatus: 0})

	req := httptest.NewRequest("GET", "/api/hitlog/export?format=json", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json content type, got %q", ct)
	}
	var entries []hitlog.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode JSON export: %v", err)
	}
	if len(entries) != 1 || entries[0].ProtocolType != "tcp" {
		t.Fatalf("expected exactly the one recorded tcp entry, got %+v", entries)
	}
}

func TestHitLogExportRespectsFilters(t *testing.T) {
	r, store := newTestHitLogRouter(t)
	mustRecordEntry(t, store, &hitlog.Entry{ProtocolType: "rest", Direction: "inbound", Method: "GET", Path: "/keep"})
	mustRecordEntry(t, store, &hitlog.Entry{ProtocolType: "smtp", Direction: "inbound", Path: "MAIL FROM"})

	req := httptest.NewRequest("GET", "/api/hitlog/export?format=json&protocolType=rest", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var entries []hitlog.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode JSON export: %v", err)
	}
	if len(entries) != 1 || entries[0].ProtocolType != "rest" {
		t.Fatalf("expected the protocolType filter to exclude the smtp entry, got %+v", entries)
	}
}

// TestPromoteToMockRejectsAnEntryWithNoValidMethod guards against a real
// crash: promoteToMock builds a "rest" mock.Definition straight from a
// hit-log entry's own Method/Path, and unlike create/update/import it used
// to skip validateMockShape entirely. A non-REST inbound hit (TCP/SMTP/FTP
// entries never set Method at all) promoted this way would previously
// produce a REST mock with an empty/invalid Method — which panics chi's
// router the moment it's dispatched, and again on every future server
// restart. This must now be rejected with 400 instead of ever reaching
// mockStore.Create/engine.Dispatch.
func TestPromoteToMockRejectsAnEntryWithNoValidMethod(t *testing.T) {
	r, store := newTestHitLogRouter(t)
	mustRecordEntry(t, store, &hitlog.Entry{
		ID: "tcp-entry", ProtocolType: "tcp", Direction: "inbound", RequestBody: "ping", ResponseBody: "pong",
	})

	req := httptest.NewRequest("POST", "/api/hitlog/tcp-entry/promote-to-mock", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("expected 400 rejecting a non-REST entry with no valid Method, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHitLogExportCSVGuardsAgainstFormulaInjection guards against a real
// CSV-injection risk: RequestBody/ResponseBody/Path/etc. are entirely
// attacker-controlled (whatever a caller sent to the mocked endpoint), and
// a cell starting with =, +, -, or @ is evaluated as a formula when the
// exported CSV is opened in Excel/Google Sheets. Each such value must be
// prefixed with a quote so it's forced to plain text instead.
func TestHitLogExportCSVGuardsAgainstFormulaInjection(t *testing.T) {
	r, store := newTestHitLogRouter(t)
	mustRecordEntry(t, store, &hitlog.Entry{
		ProtocolType: "rest", Direction: "inbound", Method: "POST", Path: "/orders",
		RequestBody:  `=cmd|'/c calc'!A1`,
		ResponseBody: `+HYPERLINK("http://evil.example")`,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/hitlog/export", nil)
	r.ServeHTTP(rec, req)

	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected a header row plus 1 entry row, got %d: %+v", len(rows), rows)
	}
	requestBody := rows[1][11]  // requestBody column, per hitLogCSVHeader
	responseBody := rows[1][13] // responseBody column
	if requestBody[0] != '\'' {
		t.Fatalf("expected requestBody to be quote-prefixed to neutralize the formula, got %q", requestBody)
	}
	if responseBody[0] != '\'' {
		t.Fatalf("expected responseBody to be quote-prefixed to neutralize the formula, got %q", responseBody)
	}
}

// TestHitLogExportCapsAtHitLogExportLimitCap only ever asserted the route
// returns 200 for an oversized limit, against an EMPTY store — that would
// pass identically whether clampExportLimit's cap existed, was wrong, or
// was deleted entirely, since an unclamped `LIMIT 999999` against an empty
// table still returns 200 with an empty array either way. Now asserts the
// actual clamp value directly instead, the same way apiclient's
// TestClampLoadTestConfig tests clampLoadTestConfig rather than only
// checking that a load test request doesn't error.
func TestHitLogExportCapsAtHitLogExportLimitCap(t *testing.T) {
	if got := clampExportLimit(999999); got != hitLogExportLimitCap {
		t.Fatalf("expected an oversized limit clamped to %d, got %d", hitLogExportLimitCap, got)
	}
	if got := clampExportLimit(100); got != 100 {
		t.Fatalf("expected a limit under the cap to pass through unchanged, got %d", got)
	}
}

// TestHitLogExportRouteHandlesOversizedLimitRequest keeps a lighter
// route-level check (200, no error) alongside the direct clamp test above
// — this one guards wiring (the route actually calls clampExportLimit and
// doesn't error on a huge limit param), not the clamp value itself.
func TestHitLogExportRouteHandlesOversizedLimitRequest(t *testing.T) {
	r, _ := newTestHitLogRouter(t)
	req := httptest.NewRequest("GET", "/api/hitlog/export?format=json&limit=999999", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200 even for an oversized limit request (it should just be capped), got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHitLogListReturnsMatchingEntries guards list — previously untested at
// the handler level (only export's shared parseQueryOptions path had
// coverage) — including that its filter bar (mockId here) behaves the same
// way export's does.
func TestHitLogListReturnsMatchingEntries(t *testing.T) {
	r, store := newTestHitLogRouter(t)
	mustRecordEntry(t, store, &hitlog.Entry{MockID: "m1", ProtocolType: "rest", Direction: "inbound", Method: "GET", Path: "/a", ResponseStatus: 200})
	mustRecordEntry(t, store, &hitlog.Entry{MockID: "m2", ProtocolType: "rest", Direction: "inbound", Method: "GET", Path: "/b", ResponseStatus: 200})

	rec := doJSON(t, r, "GET", "/api/hitlog/", nil)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var all []hitlog.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected both entries with no filter, got %d", len(all))
	}

	recFiltered := doJSON(t, r, "GET", "/api/hitlog/?mockId=m1", nil)
	var filtered []hitlog.Entry
	if err := json.Unmarshal(recFiltered.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(filtered) != 1 || filtered[0].MockID != "m1" {
		t.Fatalf("expected only m1's entry with mockId filter, got %+v", filtered)
	}
}

func TestHitLogGetReturnsEntryOrNotFound(t *testing.T) {
	r, store := newTestHitLogRouter(t)
	entry := &hitlog.Entry{ProtocolType: "rest", Direction: "inbound", Method: "GET", Path: "/orders/1", ResponseStatus: 200}
	mustRecordEntry(t, store, entry)

	rec := doJSON(t, r, "GET", "/api/hitlog/"+entry.ID, nil)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got hitlog.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != entry.ID || got.Path != "/orders/1" {
		t.Fatalf("expected the recorded entry, got %+v", got)
	}

	recMissing := doJSON(t, r, "GET", "/api/hitlog/does-not-exist", nil)
	if recMissing.Code != 404 {
		t.Fatalf("expected 404 for a missing entry, got %d: %s", recMissing.Code, recMissing.Body.String())
	}
}

// Promoting a hit whose method+path (or generated name) is already taken
// used to surface the store's duplicate error as a 500; it is a conflict.
func TestPromoteToMockReturnsConflictWhenTheMockAlreadyExists(t *testing.T) {
	engine.Register(&fakeEngine{name: "http"})
	r, store := newTestHitLogRouter(t)
	for _, id := range []string{"cap-1", "cap-2"} {
		mustRecordEntry(t, store, &hitlog.Entry{
			ID: id, ProtocolType: "rest", Direction: "proxy-capture", Method: "GET", Path: "/conflict",
			ResponseStatus: 200, ResponseBody: "{}",
		})
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/api/hitlog/cap-1/promote-to-mock", nil))
	if rec.Code != 201 {
		t.Fatalf("first promote should create the mock, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/api/hitlog/cap-2/promote-to-mock", nil))
	if rec.Code != 409 {
		t.Fatalf("expected 409 when promoting onto an existing mock, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already exists") {
		t.Fatalf("expected an explanatory message, got %s", rec.Body.String())
	}
}

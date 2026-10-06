# Load Test History + Charts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist every load-test run (aggregate result + lightweight per-request samples) so past runs can be browsed later, and replace the current flat stat-tile output with three real charts (latency-over-time, latency histogram, status-code breakdown) built on Chart.js.

**Architecture:** Two new SQLite tables (`load_test_runs`, `load_test_run_samples`) persisted by `internal/apiclient.Store` right after each load test completes, with a retention sweep mirroring the existing `internal/hitlog.RetentionWorker` pattern. Per-request sample capture in `internal/apiclient/loadtest.go` becomes unconditional (cheap: 4 small fields) instead of gated behind the existing `Detailed` flag, which keeps its original meaning — capturing full response body/headers for live inspection only, never persisted. Three new Svelte components wrap Chart.js and render from the same `LoadTestResult` shape whether it just finished running or was reloaded from history.

**Tech Stack:** Go + SQLite (existing stack), Svelte 5 + Vite (existing stack), **Chart.js** (`chart.js` npm package, `chart.js/auto` import) — this frontend's first production dependency; it has zero npm dependencies today.

**Spec:** `docs/superpowers/specs/2026-10-01-load-test-history-charts-design.md`

## Global Constraints

- Load-test safety caps are unchanged: `MaxLoadTestConcurrency=50`, `MaxLoadTestRequests=2000`, `MaxLoadTestDurationSecs=60` (`internal/apiclient/loadtest.go`).
- History persistence must never fail or block the run itself — log and continue on any save/retention error (matches the existing "best-effort" convention used for mock-version-history and dynamic-value cleanup elsewhere in this codebase).
- No full response bodies/headers are ever persisted to history — only latency, status code, elapsed-ms, and error string per sample (confirmed with the user: "lightweight" capture only).
- Retention defaults: 30 days / 50 runs per item — mirrors `settings.DefaultHitLogMaxAgeDays`/`DefaultHitLogMaxRowsPerMock`'s existing shape, new constants `DefaultLoadTestRunMaxAgeDays`/`DefaultLoadTestRunMaxRowsPerItem` in `internal/settings/store.go`.
- Route naming follows the existing (non-hyphenated) convention already used by this feature: `/api/apiclient/loadtest`, `/api/apiclient/ws-loadtest` — new routes are `/api/apiclient/loadtest-runs...`, not `/api/apiclient/load-test-runs...` (the spec's wording used a hyphen; the actual existing routes don't, so this plan follows the code, not the prose).
- Out of scope (deferred to their own future specs, per the design doc): multi-step scenarios, ramp-up load shapes, pass/fail thresholds, comparing two runs side by side.

## Review Focus

- **A load test run from an unsaved/draft tab** (no real `itemId`) — the spec requires this still persists (with `item_id=''`), just invisible to any item's history list. A test must create one and confirm `ListLoadTestRuns` for a real item never returns it, while `GetLoadTestRun` by its own id still works.
- **Deleting a collection must not orphan its runs** — `load_test_runs`/`load_test_run_samples` have no FK/CASCADE (SQLite, same as this codebase's `dynamic_counters`/`dynamic_csv_sources`), so without an explicit cleanup in `DeleteCollection`, every run ever made against that collection's requests would sit in the database forever referencing a dead `collection_id`.
- **A non-`Detailed` run must still produce a chartable result** — `StatusBreakdownChart` reads only the aggregate `Statuses` counts, never samples, so it must render correctly even though `LatencyOverTimeChart`/`LatencyHistogram` still get real sample data too now (lightweight capture is unconditional) — the distinguishing behavior to actually test is that `Detailed=false` still omits `ResponseBody`/`ResponseHeaders` from every sample, not that samples are themselves empty.
- **A run stopped early via the existing "Stop" button** (client aborts the fetch, cancelling the request's context) still produces a valid, smaller `LoadTestResult` today (confirmed in `loadtest.go`: `shouldStop()` checks `ctx.Err()`) — this must still get persisted like any other completed run, not silently dropped because the context that triggered the save itself got cancelled.
- **The existing `TestRunLoadTest_detailedSamples` test asserts `plain.Samples != nil` must be false when `Detailed` is unset** — this is the one existing test this plan's Task 1 directly breaks by design (lightweight sampling becomes unconditional) and must be rewritten, not merely left failing.

---

## File Structure

- **Modify** `internal/apiclient/loadtest.go` — add `ElapsedMs` to `LoadTestSample`, add `Detailed` to `LoadTestResult`, make lightweight capture unconditional in `record()`, export `ClampLoadTestConfig`.
- **Create** `internal/storage/migrations/000049_load_test_runs.up.sql` / `.down.sql` — the two new tables.
- **Create** `internal/storage/migrations/000050_settings_loadtest_retention.up.sql` / `.down.sql` — two new `app_settings` columns.
- **Create** `internal/apiclient/loadtest_runs.go` — `LoadTestRun`/`LoadTestRunSummary` types + `Store.SaveLoadTestRun`/`ListLoadTestRuns`/`GetLoadTestRun`/`DeleteLoadTestRun`/`EnforceLoadTestRunRetention`.
- **Create** `internal/apiclient/loadtest_runs_test.go` — tests for the above.
- **Modify** `internal/apiclient/store.go` — `DeleteCollection` gains the cascade cleanup.
- **Create** `internal/apiclient/loadtest_run_retention.go` — `LoadTestRunRetentionWorker`, mirroring `internal/hitlog/retention.go`'s `RetentionWorker` shape exactly.
- **Create** `internal/apiclient/loadtest_run_retention_test.go`.
- **Modify** `internal/settings/store.go` — two new `Settings` fields + defaults + `Get`/`Save` SQL.
- **Modify** `internal/web/api/settings.go` — two new `settingsBody` fields + validation.
- **Modify** `internal/web/api/apiclient.go` — `loadTestRequest`/`wsLoadTestRequest` gain `ItemID`/`CollectionID`; `loadTest`/`wsLoadTest` persist after running; three new handlers (`listLoadTestRuns`, `getLoadTestRun`, `deleteLoadTestRun`) + routes.
- **Modify** `internal/web/api/apiclient_test.go` — tests for the three new handlers and the persist-on-run behavior.
- **Modify** `internal/server/server.go` — construct + start `LoadTestRunRetentionWorker`.
- **Modify** `ui/package.json` — add `chart.js` dependency.
- **Create** `ui/src/lib/charts/LatencyOverTimeChart.svelte`, `ui/src/lib/charts/LatencyHistogram.svelte`, `ui/src/lib/charts/StatusBreakdownChart.svelte`.
- **Modify** `ui/src/lib/api.js` — `runLoadTest`/`runWSLoadTest` gain a `context` param; add `listLoadTestRuns`/`getLoadTestRun`/`deleteLoadTestRun`.
- **Modify** `ui/src/lib/pages/Collections.svelte` — pass item/collection context into runs, render the three charts, add a History list, gate the existing samples table on `result.detailed`.
- **Modify** `ui/src/lib/pages/Settings.svelte` — two new retention fields.

---

### Task 1: Engine changes — unconditional lightweight sampling, `ElapsedMs`, `Detailed` flag, exported clamp

**Files:**
- Modify: `internal/apiclient/loadtest.go`
- Test: `internal/apiclient/loadtest_test.go`

**Interfaces:**
- Produces: `LoadTestSample.ElapsedMs int64` (json `elapsedMs`), `LoadTestResult.Detailed bool` (json `detailed`), `apiclient.ClampLoadTestConfig(cfg LoadTestConfig) LoadTestConfig` (exported, was `clampLoadTestConfig`).

- [ ] **Step 1: Write the failing tests**

Replace the existing `TestRunLoadTest_detailedSamples` (it currently asserts the opposite of the new behavior) and add two new tests, in `internal/apiclient/loadtest_test.go`:

```go
func TestRunLoadTest_lightweightSamplesAlwaysCaptured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Header", "yes")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"hello":"world"}`))
	}))
	defer srv.Close()

	plain := RunLoadTest(context.Background(), RequestSpec{Method: "GET", URL: srv.URL}, nil, LoadTestConfig{Concurrency: 2, TotalRequests: 10})
	if plain.Detailed {
		t.Fatal("expected Detailed=false when the config didn't request it")
	}
	if len(plain.Samples) != 10 {
		t.Fatalf("expected lightweight samples captured even without Detailed, got %d", len(plain.Samples))
	}
	for _, s := range plain.Samples {
		if s.ResponseBody != "" || s.ResponseHeaders != nil {
			t.Fatalf("expected no response body/headers without Detailed, got %+v", s)
		}
		if s.StatusCode != 200 {
			t.Fatalf("expected every lightweight sample to still have its status code, got %+v", s)
		}
	}

	const n = 300
	detailed := RunLoadTest(context.Background(), RequestSpec{Method: "GET", URL: srv.URL}, nil, LoadTestConfig{Concurrency: 4, TotalRequests: n, Detailed: true})
	if !detailed.Detailed {
		t.Fatal("expected Detailed=true when the config requested it")
	}
	if len(detailed.Samples) != n {
		t.Fatalf("expected every one of %d requests captured, got %d", n, len(detailed.Samples))
	}
	for _, s := range detailed.Samples {
		if s.ResponseBody != `{"hello":"world"}` {
			t.Fatalf("expected the actual response body captured per sample when Detailed, got %q", s.ResponseBody)
		}
		if got := s.ResponseHeaders["X-Test-Header"]; len(got) != 1 || got[0] != "yes" {
			t.Fatalf("expected the actual response headers captured per sample when Detailed, got %+v", s.ResponseHeaders)
		}
	}
}

// TestRunLoadTest_cancelledContextStillReturnsAValidResult guards the
// existing "Stop" button's behavior (aborting the fetch cancels this
// run's context, checked by loadTestLimits.shouldStop between iterations)
// — a run stopped before any iteration completes must still return a
// real, persistable *LoadTestResult (zero requests is a valid outcome,
// nil is not), since internal/web/api's saveLoadTestRun persists whatever
// RunLoadTest returns regardless of why the run ended early.
func TestRunLoadTest_cancelledContextStillReturnsAValidResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before RunLoadTest's first iteration
	result := RunLoadTest(ctx, RequestSpec{Method: "GET", URL: srv.URL}, nil, LoadTestConfig{Concurrency: 2, TotalRequests: 50})
	if result == nil {
		t.Fatal("expected a non-nil result even for an immediately-cancelled run")
	}
	if result.TotalRequests != 0 {
		t.Fatalf("expected 0 requests for a run cancelled before it started, got %d", result.TotalRequests)
	}
	if result.Samples != nil {
		t.Fatalf("expected no samples for a run with zero requests, got %d", len(result.Samples))
	}
}

func TestRunLoadTest_elapsedMsIsMonotonicWithIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// A single worker (Concurrency: 1) issues requests strictly in order,
	// so ElapsedMs must be non-decreasing alongside Index for this run —
	// the x-axis every chart plots samples against.
	result := RunLoadTest(context.Background(), RequestSpec{Method: "GET", URL: srv.URL}, nil, LoadTestConfig{Concurrency: 1, TotalRequests: 20})
	if len(result.Samples) != 20 {
		t.Fatalf("expected 20 samples, got %d", len(result.Samples))
	}
	for i := 1; i < len(result.Samples); i++ {
		if result.Samples[i].ElapsedMs < result.Samples[i-1].ElapsedMs {
			t.Fatalf("expected non-decreasing ElapsedMs with a single worker, got %d then %d at index %d",
				result.Samples[i-1].ElapsedMs, result.Samples[i].ElapsedMs, i)
		}
	}
}
```

Delete the old `TestRunLoadTest_detailedSamples` function entirely — it's superseded by `TestRunLoadTest_lightweightSamplesAlwaysCaptured` above.

Also update `TestClampLoadTestConfig` to call the renamed export:

```go
func TestClampLoadTestConfig(t *testing.T) {
	cfg := ClampLoadTestConfig(LoadTestConfig{Concurrency: 9999, TotalRequests: 999999, DurationSecs: 9999})
	if cfg.Concurrency != MaxLoadTestConcurrency {
		t.Errorf("concurrency not clamped: %d", cfg.Concurrency)
	}
	if cfg.TotalRequests != MaxLoadTestRequests {
		t.Errorf("totalRequests not clamped: %d", cfg.TotalRequests)
	}
	if cfg.DurationSecs != MaxLoadTestDurationSecs {
		t.Errorf("durationSecs not clamped: %d", cfg.DurationSecs)
	}

	def := ClampLoadTestConfig(LoadTestConfig{})
	if def.Concurrency != 1 || def.TotalRequests != 50 {
		t.Errorf("expected safe defaults, got %+v", def)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/apiclient/... -run 'TestRunLoadTest_lightweightSamplesAlwaysCaptured|TestRunLoadTest_cancelledContextStillReturnsAValidResult|TestRunLoadTest_elapsedMsIsMonotonicWithIndex|TestClampLoadTestConfig' -v`
Expected: FAIL — `ClampLoadTestConfig` undefined, `plain.Detailed`/`s.ElapsedMs` fields undefined (compile error).

- [ ] **Step 3: Implement**

In `internal/apiclient/loadtest.go`:

1. Add `ElapsedMs` to `LoadTestSample`:

```go
type LoadTestSample struct {
	Index           int                 `json:"index"`
	ElapsedMs       int64               `json:"elapsedMs"`
	LatencyMs       int64               `json:"latencyMs"`
	StatusCode      int                 `json:"statusCode"`
	Error           string              `json:"error,omitempty"`
	ResponseBody    string              `json:"responseBody,omitempty"`
	ResponseHeaders map[string][]string `json:"responseHeaders,omitempty"`
}
```

2. Add `Detailed` to `LoadTestResult` (insert after `Samples`):

```go
	// Detailed echoes whether this run's config requested full response
	// body/header capture — false still means every Samples entry has a
	// real latency/status/elapsedMs (lightweight capture is unconditional),
	// just no ResponseBody/ResponseHeaders. The frontend uses this to gate
	// its "inspect full response" table without needing to probe samples
	// for which fields happen to be populated.
	Detailed bool `json:"detailed"`
```

3. Rename `clampLoadTestConfig` to `ClampLoadTestConfig` (the function body is unchanged — only the name):

```go
// ClampLoadTestConfig applies the safety caps and fills in the "neither
// limit set" default, returning a config guaranteed safe to run as-is.
// Exported so internal/web/api can persist the EXACT config a run actually
// used (see apiclient.LoadTestRun.Config), not just whatever a caller sent.
func ClampLoadTestConfig(cfg LoadTestConfig) LoadTestConfig {
```

4. Update `runLoadTest`'s one call site accordingly, and add a `start` time to the accumulator so `record()` can compute elapsed time:

```go
type loadTestAccumulator struct {
	mu        sync.Mutex
	latencies []int64
	statuses  StatusCounts
	detailed  bool
	samples   []LoadTestSample
	sampleSeq int
	start     time.Time
}
```

```go
func runLoadTest(ctx context.Context, cfg LoadTestConfig, hit func() loadTestHit) *LoadTestResult {
	cfg = ClampLoadTestConfig(cfg)

	var deadline time.Time
	if cfg.DurationSecs > 0 {
		deadline = time.Now().Add(time.Duration(cfg.DurationSecs) * time.Second)
	}
	totalCap := int64(cfg.TotalRequests)

	limits := &loadTestLimits{ctx: ctx, deadline: deadline, totalCap: totalCap}
	start := time.Now()
	acc := &loadTestAccumulator{detailed: cfg.Detailed, start: start}

	var wg sync.WaitGroup
	for i := 0; i < cfg.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runLoadTestWorker(limits, hit, acc)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	result := summarizeLoadTest(acc, elapsed)
	result.Detailed = cfg.Detailed
	return result
}
```

5. Make `record()` always append a sample (lightweight fields unconditional; body/headers only when `acc.detailed`):

```go
// record must be called with acc.mu held. Lightweight fields (Index,
// ElapsedMs, LatencyMs, StatusCode, Error) are captured for EVERY request
// regardless of acc.detailed — cheap, and what every chart is drawn from
// (see internal/web/api's persist-after-run hook). ResponseBody/
// ResponseHeaders are only ever populated when acc.detailed is set — the
// one thing that flag still gates.
func (acc *loadTestAccumulator) record(h loadTestHit) {
	sample := LoadTestSample{
		Index: acc.sampleSeq, ElapsedMs: time.Since(acc.start).Milliseconds(),
		LatencyMs: h.latencyMs, StatusCode: h.statusCode, Error: h.errMsg,
	}
	if acc.detailed {
		sample.ResponseBody = h.responseBody
		sample.ResponseHeaders = h.responseHeaders
	}
	acc.samples = append(acc.samples, sample)
	acc.sampleSeq++
	if h.errMsg != "" {
		acc.statuses.CountError++
		return
	}
	acc.latencies = append(acc.latencies, h.latencyMs)
	switch {
	case h.statusCode >= 500:
		acc.statuses.Count5xx++
	case h.statusCode >= 400:
		acc.statuses.Count4xx++
	case h.statusCode >= 300:
		acc.statuses.Count3xx++
	default:
		acc.statuses.Count2xx++
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/apiclient/... -v`
Expected: PASS — every test in the package, including the ones just added/rewritten.

- [ ] **Step 5: Commit**

```bash
git add internal/apiclient/loadtest.go internal/apiclient/loadtest_test.go
git commit -m "Capture lightweight load-test samples on every run, not just Detailed ones"
```

---

### Task 2: Database migrations for run history

**Files:**
- Create: `internal/storage/migrations/000049_load_test_runs.up.sql`
- Create: `internal/storage/migrations/000049_load_test_runs.down.sql`

**Interfaces:**
- Produces: tables `load_test_runs`, `load_test_run_samples` (no Go code in this task — Task 3 is the first to read/write them).

- [ ] **Step 1: Write the migration files**

`internal/storage/migrations/000049_load_test_runs.up.sql`:

```sql
CREATE TABLE load_test_runs (
    id TEXT PRIMARY KEY,
    item_id TEXT NOT NULL DEFAULT '',
    collection_id TEXT NOT NULL DEFAULT '',
    method TEXT NOT NULL,
    url TEXT NOT NULL,
    config_json TEXT NOT NULL,
    result_json TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL
);

CREATE INDEX idx_load_test_runs_item_id ON load_test_runs (item_id);
CREATE INDEX idx_load_test_runs_collection_id ON load_test_runs (collection_id);

CREATE TABLE load_test_run_samples (
    run_id TEXT NOT NULL,
    idx INTEGER NOT NULL,
    elapsed_ms INTEGER NOT NULL,
    latency_ms INTEGER NOT NULL,
    status_code INTEGER NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, idx)
);
```

`internal/storage/migrations/000049_load_test_runs.down.sql`:

```sql
-- Additive-only migrations (see 000004_async.down.sql) — no-op.
```

- [ ] **Step 2: Verify the migration applies cleanly**

Run: `go test ./internal/storage/... -v`
Expected: PASS — this package's existing tests open a fresh DB (running every migration, including the new one) on every test run; a syntax error in the new SQL fails them immediately.

- [ ] **Step 3: Commit**

```bash
git add internal/storage/migrations/000049_load_test_runs.up.sql internal/storage/migrations/000049_load_test_runs.down.sql
git commit -m "Add load_test_runs/load_test_run_samples tables"
```

---

### Task 3: `apiclient.Store` CRUD for load-test run history

**Files:**
- Create: `internal/apiclient/loadtest_runs.go`
- Create: `internal/apiclient/loadtest_runs_test.go`
- Modify: `internal/apiclient/store.go` (`DeleteCollection` cascade)

**Interfaces:**
- Consumes: `LoadTestConfig`, `LoadTestResult`, `LoadTestSample`, `StatusCounts` (Task 1, `internal/apiclient/loadtest.go`); `Store.db *sql.DB`, `formatTime`/`parseTime`, `ErrNotFound` (existing, `internal/apiclient/store.go`).
- Produces: `LoadTestRun` struct, `LoadTestRunSummary` struct, `Store.SaveLoadTestRun(run *LoadTestRun) error`, `Store.ListLoadTestRuns(itemID string) ([]*LoadTestRunSummary, error)`, `Store.GetLoadTestRun(id string) (*LoadTestRun, error)`, `Store.DeleteLoadTestRun(id string) error`, `Store.EnforceLoadTestRunRetention(maxAgeDays, maxRowsPerItem int) error` — all used by Task 4 (retention worker) and Task 6 (HTTP handlers).

- [ ] **Step 1: Write the failing tests**

`internal/apiclient/loadtest_runs_test.go`:

```go
package apiclient

import "testing"

func sampleLoadTestResult() *LoadTestResult {
	return &LoadTestResult{
		TotalRequests:  3,
		Statuses:       StatusCounts{Count2xx: 2, Count5xx: 1},
		MinMs:          10, MaxMs: 30, AvgMs: 20, P50Ms: 20, P90Ms: 30, P95Ms: 30, P99Ms: 30,
		DurationMs:     100,
		RequestsPerSec: 30,
		Samples: []LoadTestSample{
			{Index: 0, ElapsedMs: 0, LatencyMs: 10, StatusCode: 200},
			{Index: 1, ElapsedMs: 40, LatencyMs: 20, StatusCode: 200},
			{Index: 2, ElapsedMs: 90, LatencyMs: 30, StatusCode: 500},
		},
	}
}

func TestSaveAndGetLoadTestRunRoundTrip(t *testing.T) {
	s := newTestStore(t)
	run := &LoadTestRun{
		ItemID: "item-1", CollectionID: "coll-1", Method: "GET", URL: "http://example.com/x",
		Config: LoadTestConfig{Concurrency: 4, TotalRequests: 3},
		Result: sampleLoadTestResult(),
	}
	if err := s.SaveLoadTestRun(run); err != nil {
		t.Fatalf("SaveLoadTestRun: %v", err)
	}
	if run.ID == "" {
		t.Fatal("expected an ID to be assigned")
	}

	got, err := s.GetLoadTestRun(run.ID)
	if err != nil {
		t.Fatalf("GetLoadTestRun: %v", err)
	}
	if got.ItemID != "item-1" || got.CollectionID != "coll-1" || got.Method != "GET" || got.URL != "http://example.com/x" {
		t.Fatalf("unexpected run metadata: %+v", got)
	}
	if got.Config.Concurrency != 4 || got.Config.TotalRequests != 3 {
		t.Fatalf("unexpected config: %+v", got.Config)
	}
	if got.Result.TotalRequests != 3 || got.Result.Statuses.Count5xx != 1 {
		t.Fatalf("unexpected result aggregate: %+v", got.Result)
	}
	if len(got.Result.Samples) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(got.Result.Samples))
	}
	if got.Result.Samples[2].ElapsedMs != 90 || got.Result.Samples[2].StatusCode != 500 {
		t.Fatalf("unexpected sample[2]: %+v", got.Result.Samples[2])
	}
}

func TestListLoadTestRunsScopedByItemAndOrderedNewestFirst(t *testing.T) {
	s := newTestStore(t)
	for _, itemID := range []string{"item-1", "item-1", "item-2"} {
		if err := s.SaveLoadTestRun(&LoadTestRun{ItemID: itemID, Method: "GET", URL: "http://x", Config: LoadTestConfig{}, Result: sampleLoadTestResult()}); err != nil {
			t.Fatalf("SaveLoadTestRun: %v", err)
		}
	}
	list, err := s.ListLoadTestRuns("item-1")
	if err != nil {
		t.Fatalf("ListLoadTestRuns: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 runs for item-1, got %d", len(list))
	}
	if list[0].TotalRequests != 3 || list[0].ErrorRate <= 0 {
		t.Fatalf("expected a populated summary (1 of 3 requests was a 5xx), got %+v", list[0])
	}
}

// TestLoadTestRunWithNoItemIDIsInvisibleToItemHistory guards the
// unsaved/draft-tab case the spec explicitly allows: still persisted
// (fetchable by its own id), just never listed under any real item.
func TestLoadTestRunWithNoItemIDIsInvisibleToItemHistory(t *testing.T) {
	s := newTestStore(t)
	run := &LoadTestRun{Method: "GET", URL: "http://x", Config: LoadTestConfig{}, Result: sampleLoadTestResult()}
	if err := s.SaveLoadTestRun(run); err != nil {
		t.Fatalf("SaveLoadTestRun: %v", err)
	}
	list, err := s.ListLoadTestRuns("item-1")
	if err != nil {
		t.Fatalf("ListLoadTestRuns: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected a draft run (no itemId) to never appear in a real item's history, got %+v", list)
	}
	if _, err := s.GetLoadTestRun(run.ID); err != nil {
		t.Fatalf("expected the draft run to still be directly fetchable by id: %v", err)
	}
}

func TestDeleteLoadTestRunRemovesItsSamples(t *testing.T) {
	s := newTestStore(t)
	run := &LoadTestRun{ItemID: "item-1", Method: "GET", URL: "http://x", Config: LoadTestConfig{}, Result: sampleLoadTestResult()}
	if err := s.SaveLoadTestRun(run); err != nil {
		t.Fatalf("SaveLoadTestRun: %v", err)
	}
	if err := s.DeleteLoadTestRun(run.ID); err != nil {
		t.Fatalf("DeleteLoadTestRun: %v", err)
	}
	if _, err := s.GetLoadTestRun(run.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
	var sampleCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM load_test_run_samples WHERE run_id = ?`, run.ID).Scan(&sampleCount); err != nil {
		t.Fatalf("count samples: %v", err)
	}
	if sampleCount != 0 {
		t.Fatalf("expected the run's samples to be deleted too, got %d left", sampleCount)
	}
}

func TestDeleteLoadTestRunNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.DeleteLoadTestRun("nope"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestSaveLoadTestRunWithZeroSamples guards the other half of the
// cancelled-run case covered at the engine level by
// TestRunLoadTest_cancelledContextStillReturnsAValidResult: a run stopped
// before anything completed has a real *LoadTestResult with Samples==nil
// and TotalRequests==0 — SaveLoadTestRun must persist that cleanly (the
// for-range over a nil Samples slice is a zero-iteration no-op, not an
// error), not reject or panic on it.
func TestSaveLoadTestRunWithZeroSamples(t *testing.T) {
	s := newTestStore(t)
	run := &LoadTestRun{
		ItemID: "item-1", Method: "GET", URL: "http://x",
		Config: LoadTestConfig{Concurrency: 2, TotalRequests: 50},
		Result: &LoadTestResult{TotalRequests: 0},
	}
	if err := s.SaveLoadTestRun(run); err != nil {
		t.Fatalf("SaveLoadTestRun: %v", err)
	}
	got, err := s.GetLoadTestRun(run.ID)
	if err != nil {
		t.Fatalf("GetLoadTestRun: %v", err)
	}
	if got.Result.TotalRequests != 0 || len(got.Result.Samples) != 0 {
		t.Fatalf("expected a zero-request, zero-sample run to round-trip cleanly, got %+v", got.Result)
	}
}

// TestDeleteCollectionCleansUpItsLoadTestRuns guards a real gap: collection_id
// has no FK/CASCADE, so without DeleteCollection's own explicit cleanup, a
// deleted collection's load-test-run history would sit orphaned forever.
func TestDeleteCollectionCleansUpItsLoadTestRuns(t *testing.T) {
	s := newTestStore(t)
	coll, err := s.CreateCollection(&Collection{Name: "c1"})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	run := &LoadTestRun{CollectionID: coll.ID, ItemID: "item-1", Method: "GET", URL: "http://x", Config: LoadTestConfig{}, Result: sampleLoadTestResult()}
	if err := s.SaveLoadTestRun(run); err != nil {
		t.Fatalf("SaveLoadTestRun: %v", err)
	}

	if err := s.DeleteCollection(coll.ID); err != nil {
		t.Fatalf("DeleteCollection: %v", err)
	}
	if _, err := s.GetLoadTestRun(run.ID); err != ErrNotFound {
		t.Fatalf("expected the collection's load test run to be cleaned up, got %v", err)
	}
}

func TestEnforceLoadTestRunRetentionByAgeAndCount(t *testing.T) {
	s := newTestStore(t)
	old := &LoadTestRun{ItemID: "item-1", Method: "GET", URL: "http://x", Config: LoadTestConfig{}, Result: sampleLoadTestResult()}
	if err := s.SaveLoadTestRun(old); err != nil {
		t.Fatalf("SaveLoadTestRun: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE load_test_runs SET created_at = ? WHERE id = ?`, formatTime(timeNowMinus(40)), old.ID); err != nil {
		t.Fatalf("backdate run: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := s.SaveLoadTestRun(&LoadTestRun{ItemID: "item-1", Method: "GET", URL: "http://x", Config: LoadTestConfig{}, Result: sampleLoadTestResult()}); err != nil {
			t.Fatalf("SaveLoadTestRun: %v", err)
		}
	}

	if err := s.EnforceLoadTestRunRetention(30, 3); err != nil {
		t.Fatalf("EnforceLoadTestRunRetention: %v", err)
	}

	if _, err := s.GetLoadTestRun(old.ID); err != ErrNotFound {
		t.Fatalf("expected the 40-day-old run to be purged by the age cutoff, got %v", err)
	}
	list, err := s.ListLoadTestRuns("item-1")
	if err != nil {
		t.Fatalf("ListLoadTestRuns: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected exactly 3 runs left (maxRowsPerItem), got %d", len(list))
	}
}
```

Add this small test-only time helper at the bottom of the same file (mirrors how other tests in this codebase backdate a row to test retention):

```go
func timeNowMinus(days int) time.Time {
	return time.Now().UTC().AddDate(0, 0, -days)
}
```

Add `"time"` to this test file's imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/apiclient/... -run 'LoadTestRun' -v`
Expected: FAIL with compile errors — `LoadTestRun`/`LoadTestRunSummary`/`SaveLoadTestRun`/etc. undefined.

- [ ] **Step 3: Implement the store methods**

Create `internal/apiclient/loadtest_runs.go`:

```go
package apiclient

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// LoadTestRun is one persisted load-test run: its metadata plus the full
// aggregate result (with Samples attached — see GetLoadTestRun, which is
// the only accessor that populates them; ListLoadTestRuns returns the
// lighter LoadTestRunSummary instead).
type LoadTestRun struct {
	ID string `json:"id"`
	// ItemID/CollectionID identify which saved request this run was
	// against — both '' for an unsaved/draft tab, which still gets
	// persisted (fetchable by ID) but never appears in any item's history
	// list (see ListLoadTestRuns).
	ItemID       string         `json:"itemId,omitempty"`
	CollectionID string         `json:"collectionId,omitempty"`
	Method       string         `json:"method"`
	URL          string         `json:"url"`
	Config       LoadTestConfig `json:"config"`
	Result       *LoadTestResult `json:"result"`
	CreatedAt    time.Time      `json:"createdAt"`
}

// LoadTestRunSummary is what a history LIST needs — enough for one row
// (timestamp, req/s, p95, error rate) without paying for every sample.
type LoadTestRunSummary struct {
	ID             string    `json:"id"`
	ItemID         string    `json:"itemId,omitempty"`
	Method         string    `json:"method"`
	URL            string    `json:"url"`
	CreatedAt      time.Time `json:"createdAt"`
	TotalRequests  int       `json:"totalRequests"`
	RequestsPerSec float64   `json:"requestsPerSec"`
	P95Ms          int64     `json:"p95Ms"`
	// ErrorRate is the percentage (0-100) of requests that were a 4xx, 5xx,
	// or network error.
	ErrorRate float64 `json:"errorRate"`
}

func errorRatePercent(s StatusCounts) float64 {
	total := s.Count2xx + s.Count3xx + s.Count4xx + s.Count5xx + s.CountError
	if total == 0 {
		return 0
	}
	return float64(s.Count4xx+s.Count5xx+s.CountError) / float64(total) * 100
}

// SaveLoadTestRun persists run, assigning an ID/CreatedAt if not already
// set. Samples are stored in their own table (load_test_run_samples), so
// result_json never duplicates them — this method strips Samples from the
// JSON it writes for the aggregate row, independent of whatever run.Result
// itself still holds in memory.
func (s *Store) SaveLoadTestRun(run *LoadTestRun) error {
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	configJSON, err := json.Marshal(run.Config)
	if err != nil {
		return fmt.Errorf("marshal load test config: %w", err)
	}
	resultForJSON := *run.Result
	resultForJSON.Samples = nil
	resultJSON, err := json.Marshal(resultForJSON)
	if err != nil {
		return fmt.Errorf("marshal load test result: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO load_test_runs (id, item_id, collection_id, method, url, config_json, result_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.ItemID, run.CollectionID, run.Method, run.URL, string(configJSON), string(resultJSON), formatTime(run.CreatedAt),
	); err != nil {
		return fmt.Errorf("insert load test run: %w", err)
	}
	for _, sample := range run.Result.Samples {
		if _, err := tx.Exec(
			`INSERT INTO load_test_run_samples (run_id, idx, elapsed_ms, latency_ms, status_code, error) VALUES (?, ?, ?, ?, ?, ?)`,
			run.ID, sample.Index, sample.ElapsedMs, sample.LatencyMs, sample.StatusCode, sample.Error,
		); err != nil {
			return fmt.Errorf("insert load test run sample: %w", err)
		}
	}
	return tx.Commit()
}

// ListLoadTestRuns returns summaries for every run against itemID, newest
// first.
func (s *Store) ListLoadTestRuns(itemID string) ([]*LoadTestRunSummary, error) {
	rows, err := s.db.Query(
		`SELECT id, item_id, method, url, result_json, created_at FROM load_test_runs WHERE item_id = ? ORDER BY created_at DESC`,
		itemID,
	)
	if err != nil {
		return nil, fmt.Errorf("list load test runs: %w", err)
	}
	defer rows.Close()

	out := []*LoadTestRunSummary{}
	for rows.Next() {
		var id, iid, method, url, resultJSON, createdAt string
		if err := rows.Scan(&id, &iid, &method, &url, &resultJSON, &createdAt); err != nil {
			return nil, fmt.Errorf("scan load test run: %w", err)
		}
		var result LoadTestResult
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			return nil, fmt.Errorf("unmarshal load test result: %w", err)
		}
		created, err := parseTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		out = append(out, &LoadTestRunSummary{
			ID: id, ItemID: iid, Method: method, URL: url, CreatedAt: created,
			TotalRequests: result.TotalRequests, RequestsPerSec: result.RequestsPerSec, P95Ms: result.P95Ms,
			ErrorRate: errorRatePercent(result.Statuses),
		})
	}
	return out, rows.Err()
}

// GetLoadTestRun returns ErrNotFound if id doesn't exist.
func (s *Store) GetLoadTestRun(id string) (*LoadTestRun, error) {
	row := s.db.QueryRow(
		`SELECT id, item_id, collection_id, method, url, config_json, result_json, created_at FROM load_test_runs WHERE id = ?`,
		id,
	)
	var run LoadTestRun
	var configJSON, resultJSON, createdAt string
	if err := row.Scan(&run.ID, &run.ItemID, &run.CollectionID, &run.Method, &run.URL, &configJSON, &resultJSON, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan load test run: %w", err)
	}
	if err := json.Unmarshal([]byte(configJSON), &run.Config); err != nil {
		return nil, fmt.Errorf("unmarshal load test config: %w", err)
	}
	var result LoadTestResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return nil, fmt.Errorf("unmarshal load test result: %w", err)
	}
	var err error
	if run.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}

	sampleRows, err := s.db.Query(
		`SELECT idx, elapsed_ms, latency_ms, status_code, error FROM load_test_run_samples WHERE run_id = ? ORDER BY idx ASC`,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("list load test run samples: %w", err)
	}
	defer sampleRows.Close()
	var samples []LoadTestSample
	for sampleRows.Next() {
		var smp LoadTestSample
		if err := sampleRows.Scan(&smp.Index, &smp.ElapsedMs, &smp.LatencyMs, &smp.StatusCode, &smp.Error); err != nil {
			return nil, fmt.Errorf("scan load test run sample: %w", err)
		}
		samples = append(samples, smp)
	}
	if err := sampleRows.Err(); err != nil {
		return nil, err
	}
	result.Samples = samples
	run.Result = &result
	return &run, nil
}

// DeleteLoadTestRun returns ErrNotFound if id doesn't exist.
func (s *Store) DeleteLoadTestRun(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM load_test_run_samples WHERE run_id = ?`, id); err != nil {
		return fmt.Errorf("delete load test run samples: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM load_test_runs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete load test run: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// EnforceLoadTestRunRetention deletes every run older than maxAgeDays,
// then trims each remaining item_id down to its most recent
// maxRowsPerItem runs — mirrors internal/hitlog.Store.DeleteOlderThan +
// EnforceMaxRowsPerMock's exact two-part shape. A '' item_id (a draft-tab
// run) is excluded from the per-item trim — there's no meaningful "most
// recent N" grouping for runs that don't share a real item, so only the
// age cutoff above ever reaps those.
func (s *Store) EnforceLoadTestRunRetention(maxAgeDays, maxRowsPerItem int) error {
	cutoff := formatTime(time.Now().UTC().Add(-time.Duration(maxAgeDays) * 24 * time.Hour))
	if _, err := s.db.Exec(
		`DELETE FROM load_test_run_samples WHERE run_id IN (SELECT id FROM load_test_runs WHERE created_at < ?)`,
		cutoff,
	); err != nil {
		return fmt.Errorf("delete old load test run samples: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM load_test_runs WHERE created_at < ?`, cutoff); err != nil {
		return fmt.Errorf("delete old load test runs: %w", err)
	}

	rows, err := s.db.Query(`SELECT DISTINCT item_id FROM load_test_runs WHERE item_id != ''`)
	if err != nil {
		return fmt.Errorf("list load test run item ids: %w", err)
	}
	var itemIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan item id: %w", err)
		}
		itemIDs = append(itemIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, itemID := range itemIDs {
		if _, err := s.db.Exec(
			`DELETE FROM load_test_run_samples WHERE run_id IN (
			   SELECT id FROM load_test_runs WHERE item_id = ? AND id NOT IN (
			     SELECT id FROM load_test_runs WHERE item_id = ? ORDER BY created_at DESC LIMIT ?
			   )
			 )`,
			itemID, itemID, maxRowsPerItem,
		); err != nil {
			return fmt.Errorf("enforce max rows for item %s (samples): %w", itemID, err)
		}
		if _, err := s.db.Exec(
			`DELETE FROM load_test_runs WHERE item_id = ? AND id NOT IN (
			   SELECT id FROM load_test_runs WHERE item_id = ? ORDER BY created_at DESC LIMIT ?
			 )`,
			itemID, itemID, maxRowsPerItem,
		); err != nil {
			return fmt.Errorf("enforce max rows for item %s: %w", itemID, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Wire the `DeleteCollection` cascade**

In `internal/apiclient/store.go`, modify `DeleteCollection` (currently a single `DELETE FROM collections WHERE id=?`):

```go
func (s *Store) DeleteCollection(id string) error {
	// Same reasoning as mock.Store.Delete's own dynamic-value cleanup:
	// collection_id has no FK/CASCADE, so a deleted collection's load-test
	// run history would otherwise sit orphaned forever.
	if _, err := s.db.Exec(`DELETE FROM load_test_run_samples WHERE run_id IN (SELECT id FROM load_test_runs WHERE collection_id = ?)`, id); err != nil {
		return fmt.Errorf("delete collection's load test run samples: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM load_test_runs WHERE collection_id = ?`, id); err != nil {
		return fmt.Errorf("delete collection's load test runs: %w", err)
	}
	res, err := s.db.Exec(`DELETE FROM collections WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete collection: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/apiclient/... -v`
Expected: PASS — every test in the package.

- [ ] **Step 6: Commit**

```bash
git add internal/apiclient/loadtest_runs.go internal/apiclient/loadtest_runs_test.go internal/apiclient/store.go
git commit -m "Add load-test run history storage, with cascade cleanup on collection delete"
```

---

### Task 4: Settings — two new retention fields

**Files:**
- Create: `internal/storage/migrations/000050_settings_loadtest_retention.up.sql`
- Create: `internal/storage/migrations/000050_settings_loadtest_retention.down.sql`
- Modify: `internal/settings/store.go`
- Modify: `internal/settings/store_test.go` (existing file — confirm its name via `ls internal/settings/*_test.go` before editing; if it doesn't exist yet, create it following this task's test code)

**Interfaces:**
- Produces: `settings.DefaultLoadTestRunMaxAgeDays = 30`, `settings.DefaultLoadTestRunMaxRowsPerItem = 50`, `Settings.LoadTestRunMaxAgeDays int`, `Settings.LoadTestRunMaxRowsPerItem int`.

- [ ] **Step 1: Write the migration**

`internal/storage/migrations/000050_settings_loadtest_retention.up.sql`:

```sql
ALTER TABLE app_settings ADD COLUMN load_test_run_max_age_days INTEGER NOT NULL DEFAULT 0;
ALTER TABLE app_settings ADD COLUMN load_test_run_max_rows_per_item INTEGER NOT NULL DEFAULT 0;
```

`internal/storage/migrations/000050_settings_loadtest_retention.down.sql`:

```sql
-- Additive-only migrations (see 000004_async.down.sql) — no-op.
```

(0 is a pre-existing-row backfill value, not a real setting — `Store.Get` below falls back to the real defaults whenever it reads a non-positive value, the same `<= 0` convention already used for `MaxVersionsPerMock`/`MaxCapturedBodyBytes`.)

- [ ] **Step 2: Write the failing test**

Add to the existing `internal/settings/store_test.go` (it already has a `newTestStore(t *testing.T) *Store` helper at line 10 — opens a temp DB via `storage.Open`, wraps in `settings.NewStore`; reuse it as-is):

```go
func TestLoadTestRunRetentionDefaultsWhenNeverSet(t *testing.T) {
	s := newTestStore(t)
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LoadTestRunMaxAgeDays != DefaultLoadTestRunMaxAgeDays {
		t.Errorf("expected default max age %d, got %d", DefaultLoadTestRunMaxAgeDays, got.LoadTestRunMaxAgeDays)
	}
	if got.LoadTestRunMaxRowsPerItem != DefaultLoadTestRunMaxRowsPerItem {
		t.Errorf("expected default max rows %d, got %d", DefaultLoadTestRunMaxRowsPerItem, got.LoadTestRunMaxRowsPerItem)
	}
}

func TestLoadTestRunRetentionRoundTrip(t *testing.T) {
	s := newTestStore(t)
	saved, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	saved.LoadTestRunMaxAgeDays = 7
	saved.LoadTestRunMaxRowsPerItem = 10
	if err := s.Save(saved); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get (after save): %v", err)
	}
	if got.LoadTestRunMaxAgeDays != 7 || got.LoadTestRunMaxRowsPerItem != 10 {
		t.Fatalf("expected the saved values to round-trip, got %+v", got)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/settings/... -run LoadTestRunRetention -v`
Expected: FAIL — `DefaultLoadTestRunMaxAgeDays`/`LoadTestRunMaxAgeDays` undefined (compile error).

- [ ] **Step 4: Implement**

In `internal/settings/store.go`, add two constants next to the existing ones:

```go
	// DefaultLoadTestRunMaxAgeDays/DefaultLoadTestRunMaxRowsPerItem mirror
	// DefaultHitLogMaxAgeDays/DefaultHitLogMaxRowsPerMock's own shape,
	// scoped to load-test run history instead of hit-log rows.
	DefaultLoadTestRunMaxAgeDays     = 30
	DefaultLoadTestRunMaxRowsPerItem = 50
```

Add two fields to `Settings` (after `InactivityLockMinutes`, before `UpdatedAt`):

```go
	// LoadTestRunMaxAgeDays/LoadTestRunMaxRowsPerItem bound how much
	// load-test run history (internal/apiclient.LoadTestRun) is kept per
	// item before internal/apiclient.LoadTestRunRetentionWorker purges it —
	// same two-knob shape as the hit-log retention fields above, scoped to
	// a different table.
	LoadTestRunMaxAgeDays     int
	LoadTestRunMaxRowsPerItem int
```

Update `Get`'s SQL/scan/defaults:

```go
func (s *Store) Get() (*Settings, error) {
	row := s.db.QueryRow(
		`SELECT hit_log_max_age_days, hit_log_max_rows_per_mock, max_versions_per_mock, redacted_headers_json,
		        default_response_delay_ms, default_failure_rate_percent, max_captured_body_bytes, session_timeout_minutes,
		        inactivity_lock_minutes, load_test_run_max_age_days, load_test_run_max_rows_per_item, updated_at
		 FROM app_settings WHERE id='default'`)

	var maxAgeDays, maxRows, maxVersions, defaultDelayMs, maxCapturedBodyBytes, sessionTimeoutMinutes, inactivityLockMinutes int
	var loadTestRunMaxAgeDays, loadTestRunMaxRowsPerItem int
	var defaultFailureRate float64
	var redactedHeadersJSON sql.NullString
	var updatedAt string
	err := row.Scan(&maxAgeDays, &maxRows, &maxVersions, &redactedHeadersJSON,
		&defaultDelayMs, &defaultFailureRate, &maxCapturedBodyBytes, &sessionTimeoutMinutes, &inactivityLockMinutes,
		&loadTestRunMaxAgeDays, &loadTestRunMaxRowsPerItem, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return &Settings{
			HitLogMaxAgeDays:          DefaultHitLogMaxAgeDays,
			HitLogMaxRowsPerMock:      DefaultHitLogMaxRowsPerMock,
			MaxVersionsPerMock:        DefaultMaxVersionsPerMock,
			RedactedHeaders:           DefaultRedactedHeaders,
			MaxCapturedBodyBytes:      DefaultMaxCapturedBodyBytes,
			SessionTimeoutMinutes:     DefaultSessionTimeoutMinutes,
			LoadTestRunMaxAgeDays:     DefaultLoadTestRunMaxAgeDays,
			LoadTestRunMaxRowsPerItem: DefaultLoadTestRunMaxRowsPerItem,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan app settings: %w", err)
	}

	settingsRow := &Settings{
		HitLogMaxAgeDays:          maxAgeDays,
		HitLogMaxRowsPerMock:      maxRows,
		MaxVersionsPerMock:        maxVersions,
		RedactedHeaders:           DefaultRedactedHeaders,
		DefaultResponseDelayMs:    defaultDelayMs,
		DefaultFailureRatePercent: defaultFailureRate,
		MaxCapturedBodyBytes:      maxCapturedBodyBytes,
		SessionTimeoutMinutes:     sessionTimeoutMinutes,
		InactivityLockMinutes:     inactivityLockMinutes,
		LoadTestRunMaxAgeDays:     loadTestRunMaxAgeDays,
		LoadTestRunMaxRowsPerItem: loadTestRunMaxRowsPerItem,
	}
	if settingsRow.SessionTimeoutMinutes <= 0 {
		settingsRow.SessionTimeoutMinutes = DefaultSessionTimeoutMinutes
	}
	if settingsRow.MaxVersionsPerMock <= 0 {
		settingsRow.MaxVersionsPerMock = DefaultMaxVersionsPerMock
	}
	if settingsRow.MaxCapturedBodyBytes <= 0 {
		settingsRow.MaxCapturedBodyBytes = DefaultMaxCapturedBodyBytes
	}
	if settingsRow.LoadTestRunMaxAgeDays <= 0 {
		// Same reasoning as MaxVersionsPerMock above: a pre-existing row
		// predating these columns (backfilled to 0) is indistinguishable
		// from an explicit "0 days", but 0 would purge every run
		// immediately, defeating the whole feature.
		settingsRow.LoadTestRunMaxAgeDays = DefaultLoadTestRunMaxAgeDays
	}
	if settingsRow.LoadTestRunMaxRowsPerItem <= 0 {
		settingsRow.LoadTestRunMaxRowsPerItem = DefaultLoadTestRunMaxRowsPerItem
	}
	if redactedHeadersJSON.Valid && redactedHeadersJSON.String != "" {
		if err := json.Unmarshal([]byte(redactedHeadersJSON.String), &settingsRow.RedactedHeaders); err != nil {
			return nil, fmt.Errorf("unmarshal redacted headers: %w", err)
		}
	}
	if settingsRow.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return settingsRow, nil
}
```

Update `Save`'s SQL:

```go
func (s *Store) Save(settingsRow *Settings) error {
	settingsRow.UpdatedAt = time.Now().UTC()
	redactedHeadersJSON, err := json.Marshal(settingsRow.RedactedHeaders)
	if err != nil {
		return fmt.Errorf("marshal redacted headers: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO app_settings (id, hit_log_max_age_days, hit_log_max_rows_per_mock, max_versions_per_mock, redacted_headers_json,
		                           default_response_delay_ms, default_failure_rate_percent, max_captured_body_bytes, session_timeout_minutes,
		                           inactivity_lock_minutes, load_test_run_max_age_days, load_test_run_max_rows_per_item, updated_at)
		 VALUES ('default', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   hit_log_max_age_days=excluded.hit_log_max_age_days,
		   hit_log_max_rows_per_mock=excluded.hit_log_max_rows_per_mock,
		   max_versions_per_mock=excluded.max_versions_per_mock,
		   redacted_headers_json=excluded.redacted_headers_json,
		   default_response_delay_ms=excluded.default_response_delay_ms,
		   default_failure_rate_percent=excluded.default_failure_rate_percent,
		   max_captured_body_bytes=excluded.max_captured_body_bytes,
		   session_timeout_minutes=excluded.session_timeout_minutes,
		   inactivity_lock_minutes=excluded.inactivity_lock_minutes,
		   load_test_run_max_age_days=excluded.load_test_run_max_age_days,
		   load_test_run_max_rows_per_item=excluded.load_test_run_max_rows_per_item,
		   updated_at=excluded.updated_at`,
		settingsRow.HitLogMaxAgeDays, settingsRow.HitLogMaxRowsPerMock, settingsRow.MaxVersionsPerMock, string(redactedHeadersJSON),
		settingsRow.DefaultResponseDelayMs, settingsRow.DefaultFailureRatePercent, settingsRow.MaxCapturedBodyBytes, settingsRow.SessionTimeoutMinutes,
		settingsRow.InactivityLockMinutes, settingsRow.LoadTestRunMaxAgeDays, settingsRow.LoadTestRunMaxRowsPerItem, formatTime(settingsRow.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("save app settings: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/settings/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/storage/migrations/000050_settings_loadtest_retention.up.sql internal/storage/migrations/000050_settings_loadtest_retention.down.sql internal/settings/store.go internal/settings/store_test.go
git commit -m "Add load-test run retention settings"
```

---

### Task 5: `LoadTestRunRetentionWorker`

**Files:**
- Create: `internal/apiclient/loadtest_run_retention.go`
- Create: `internal/apiclient/loadtest_run_retention_test.go`

**Interfaces:**
- Consumes: `Store.EnforceLoadTestRunRetention` (Task 3), `settings.Settings.LoadTestRunMaxAgeDays`/`LoadTestRunMaxRowsPerItem` (Task 4).
- Produces: `apiclient.LoadTestRunSettingsProvider` interface, `apiclient.NewLoadTestRunRetentionWorker(store *Store, settingsProvider LoadTestRunSettingsProvider) *LoadTestRunRetentionWorker`, `(w *LoadTestRunRetentionWorker) Start(ctx context.Context)` — consumed by Task 7 (`internal/server/server.go`).

- [ ] **Step 1: Write the failing test**

`internal/apiclient/loadtest_run_retention_test.go`:

```go
package apiclient

import (
	"context"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/settings"
)

type fakeLoadTestRunSettingsProvider struct{ s *settings.Settings }

func (f *fakeLoadTestRunSettingsProvider) Get() (*settings.Settings, error) { return f.s, nil }

func TestLoadTestRunRetentionWorkerPurgesOnStart(t *testing.T) {
	s := newTestStore(t)
	run := &LoadTestRun{ItemID: "item-1", Method: "GET", URL: "http://x", Config: LoadTestConfig{}, Result: sampleLoadTestResult()}
	if err := s.SaveLoadTestRun(run); err != nil {
		t.Fatalf("SaveLoadTestRun: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE load_test_runs SET created_at = ? WHERE id = ?`, formatTime(timeNowMinus(999)), run.ID); err != nil {
		t.Fatalf("backdate run: %v", err)
	}

	w := NewLoadTestRunRetentionWorker(s, &fakeLoadTestRunSettingsProvider{s: &settings.Settings{LoadTestRunMaxAgeDays: 1, LoadTestRunMaxRowsPerItem: 50}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx) // purges once, synchronously, before returning

	if _, err := s.GetLoadTestRun(run.ID); err != ErrNotFound {
		t.Fatalf("expected the old run to be purged by Start's immediate pass, got %v", err)
	}
}

func TestLoadTestRunRetentionWorkerFallsBackToDefaultsOnSettingsError(t *testing.T) {
	s := newTestStore(t)
	run := &LoadTestRun{ItemID: "item-1", Method: "GET", URL: "http://x", Config: LoadTestConfig{}, Result: sampleLoadTestResult()}
	if err := s.SaveLoadTestRun(run); err != nil {
		t.Fatalf("SaveLoadTestRun: %v", err)
	}

	w := NewLoadTestRunRetentionWorker(s, nil) // nil provider — must not panic, must use defaults
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	if _, err := s.GetLoadTestRun(run.ID); err != nil {
		t.Fatalf("expected a fresh run to survive the default 30-day/50-row retention, got %v", err)
	}
	_ = time.Second // keep time imported for readability of future assertions
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/apiclient/... -run LoadTestRunRetentionWorker -v`
Expected: FAIL — `NewLoadTestRunRetentionWorker` undefined.

- [ ] **Step 3: Implement**

`internal/apiclient/loadtest_run_retention.go`:

```go
package apiclient

import (
	"context"
	"log"
	"time"

	"github.com/addictedabhi/airmock/internal/settings"
)

const defaultLoadTestRunPurgeInterval = 1 * time.Hour

// LoadTestRunSettingsProvider is the narrow slice of internal/settings.Store
// this worker needs — read fresh on every purge cycle (not snapshotted
// once) so a policy change on the Settings page takes effect on the next
// tick without a restart. Mirrors internal/hitlog.SettingsProvider exactly.
type LoadTestRunSettingsProvider interface {
	Get() (*settings.Settings, error)
}

// LoadTestRunRetentionWorker periodically purges old/excess
// load_test_runs rows, mirroring internal/hitlog.RetentionWorker's shape
// for a different table.
type LoadTestRunRetentionWorker struct {
	store            *Store
	settingsProvider LoadTestRunSettingsProvider
	interval         time.Duration
}

func NewLoadTestRunRetentionWorker(store *Store, settingsProvider LoadTestRunSettingsProvider) *LoadTestRunRetentionWorker {
	return &LoadTestRunRetentionWorker{
		store:            store,
		settingsProvider: settingsProvider,
		interval:         defaultLoadTestRunPurgeInterval,
	}
}

// Start runs an immediate purge, then repeats on w.interval until ctx is
// cancelled.
func (w *LoadTestRunRetentionWorker) Start(ctx context.Context) {
	w.purge()
	go func() {
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.purge()
			}
		}
	}()
}

func (w *LoadTestRunRetentionWorker) purge() {
	maxAgeDays, maxRowsPerItem := settings.DefaultLoadTestRunMaxAgeDays, settings.DefaultLoadTestRunMaxRowsPerItem
	if w.settingsProvider != nil {
		if s, err := w.settingsProvider.Get(); err != nil {
			log.Printf("airmock: load test run retention: failed to load settings, using defaults: %v", err)
		} else {
			maxAgeDays, maxRowsPerItem = s.LoadTestRunMaxAgeDays, s.LoadTestRunMaxRowsPerItem
		}
	}
	if err := w.store.EnforceLoadTestRunRetention(maxAgeDays, maxRowsPerItem); err != nil {
		log.Printf("airmock: load test run retention failed: %v", err)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/apiclient/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/apiclient/loadtest_run_retention.go internal/apiclient/loadtest_run_retention_test.go
git commit -m "Add LoadTestRunRetentionWorker"
```

---

### Task 6: HTTP handlers — persist on run, list/get/delete history, settings wiring

**Files:**
- Modify: `internal/web/api/apiclient.go`
- Modify: `internal/web/api/apiclient_test.go`
- Modify: `internal/web/api/settings.go`
- Modify: `internal/server/server.go`

**Interfaces:**
- Consumes: `apiclient.ClampLoadTestConfig`, `apiclient.LoadTestRun`, `apiclient.LoadTestRunSummary`, `Store.SaveLoadTestRun`/`ListLoadTestRuns`/`GetLoadTestRun`/`DeleteLoadTestRun` (Task 3), `apiclient.NewLoadTestRunRetentionWorker` (Task 5), `settings.Settings.LoadTestRunMaxAgeDays`/`LoadTestRunMaxRowsPerItem` (Task 4).
- Produces: routes `GET/POST /api/apiclient/loadtest-runs`, `GET/DELETE /api/apiclient/loadtest-runs/{id}` — consumed by Task 8 (`ui/src/lib/api.js`).

- [ ] **Step 1: Write the failing tests**

Add to `internal/web/api/apiclient_test.go`. This file's existing helper is `newTestAPIClientRouter(t *testing.T) chi.Router` (single return value, confirmed at `internal/web/api/apiclient_test.go:23` — it constructs its own internal `*apiclient.Store` that isn't exposed to callers), so both new tests verify persistence purely through the public HTTP routes rather than reaching into a store directly:

```go
func TestLoadTestPersistsRunHistory(t *testing.T) {
	r := newTestAPIClientRouter(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rec := doJSON(t, r, "POST", "/api/apiclient/loadtest", map[string]any{
		"spec":          map[string]any{"method": "GET", "url": srv.URL},
		"concurrency":   2,
		"totalRequests": 5,
		"itemId":        "item-1",
		"collectionId":  "coll-1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, r, "GET", "/api/apiclient/loadtest-runs?itemId=item-1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 listing history, got %d: %s", rec.Code, rec.Body.String())
	}
	var list []apiclient.LoadTestRunSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 || list[0].TotalRequests != 5 {
		t.Fatalf("expected the run to be persisted under item-1, got %+v", list)
	}
}

func TestLoadTestRunHistoryRoutesRoundTrip(t *testing.T) {
	r := newTestAPIClientRouter(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rec := doJSON(t, r, "POST", "/api/apiclient/loadtest", map[string]any{
		"spec":          map[string]any{"method": "GET", "url": srv.URL},
		"concurrency":   1,
		"totalRequests": 1,
		"itemId":        "item-1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 running the load test, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, r, "GET", "/api/apiclient/loadtest-runs?itemId=item-1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 listing history, got %d: %s", rec.Code, rec.Body.String())
	}
	var list []apiclient.LoadTestRunSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 run in history, got %d", len(list))
	}
	runID := list[0].ID

	rec = doJSON(t, r, "GET", "/api/apiclient/loadtest-runs/"+runID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 getting one run, got %d: %s", rec.Code, rec.Body.String())
	}
	var full apiclient.LoadTestRun
	if err := json.Unmarshal(rec.Body.Bytes(), &full); err != nil {
		t.Fatalf("decode full run: %v", err)
	}
	if full.Result == nil || full.Result.TotalRequests != 1 {
		t.Fatalf("expected the full run's result, got %+v", full)
	}

	rec = doJSON(t, r, "DELETE", "/api/apiclient/loadtest-runs/"+runID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 deleting the run, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, r, "GET", "/api/apiclient/loadtest-runs/"+runID, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d: %s", rec.Code, rec.Body.String())
	}
}
```

Confirm `"github.com/addictedabhi/airmock/internal/apiclient"` is already imported in this test file (it is, per `TestLoadTestAggregatesResults`'s existing use of `apiclient.LoadTestResult`) — no new import needed.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/web/api/... -run 'TestLoadTestPersistsRunHistory|TestLoadTestRunHistoryRoutesRoundTrip' -v`
Expected: FAIL — new routes 404 (`/loadtest-runs` doesn't exist yet).

- [ ] **Step 3: Implement**

In `internal/web/api/apiclient.go`, update `loadTestRequest`/`wsLoadTestRequest` and the two handlers:

```go
type loadTestRequest struct {
	Spec          apiclient.RequestSpec `json:"spec"`
	Variables     map[string]string     `json:"variables,omitempty"`
	Concurrency   int                   `json:"concurrency"`
	TotalRequests int                   `json:"totalRequests,omitempty"`
	DurationSecs  int                   `json:"durationSecs,omitempty"`
	Detailed      bool                  `json:"detailed,omitempty"`
	// ItemID/CollectionID identify which saved request this run is for —
	// '' for an unsaved/draft tab, still persisted (see saveLoadTestRun)
	// but invisible to any item's history list.
	ItemID       string `json:"itemId,omitempty"`
	CollectionID string `json:"collectionId,omitempty"`
}
```

```go
func (h *APIClientHandler) loadTest(w http.ResponseWriter, r *http.Request) {
	var req loadTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := h.resolveClientCert(&req.Spec); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg := apiclient.LoadTestConfig{
		Concurrency:   req.Concurrency,
		TotalRequests: req.TotalRequests,
		DurationSecs:  req.DurationSecs,
		Detailed:      req.Detailed,
	}
	result := apiclient.RunLoadTest(r.Context(), req.Spec, req.Variables, cfg)
	h.saveLoadTestRun(req.ItemID, req.CollectionID, req.Spec.Method, req.Spec.URL, cfg, result)
	writeJSON(w, http.StatusOK, result)
}
```

```go
type wsLoadTestRequest struct {
	Spec          apiclient.WSRequestSpec `json:"spec"`
	Variables     map[string]string       `json:"variables,omitempty"`
	Concurrency   int                     `json:"concurrency"`
	TotalRequests int                     `json:"totalRequests,omitempty"`
	DurationSecs  int                     `json:"durationSecs,omitempty"`
	Detailed      bool                    `json:"detailed,omitempty"`
	ItemID        string                  `json:"itemId,omitempty"`
	CollectionID  string                  `json:"collectionId,omitempty"`
}

func (h *APIClientHandler) wsLoadTest(w http.ResponseWriter, r *http.Request) {
	var req wsLoadTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg := apiclient.LoadTestConfig{
		Concurrency:   req.Concurrency,
		TotalRequests: req.TotalRequests,
		DurationSecs:  req.DurationSecs,
		Detailed:      req.Detailed,
	}
	result := apiclient.RunWSLoadTest(r.Context(), req.Spec, req.Variables, cfg)
	h.saveLoadTestRun(req.ItemID, req.CollectionID, "WS", req.Spec.URL, cfg, result)
	writeJSON(w, http.StatusOK, result)
}

// saveLoadTestRun persists a just-completed run's history — best-effort,
// matching this codebase's existing "cleanup/logging side effect must
// never fail the real operation" convention (see e.g. mock.Store.Delete's
// own best-effort dynamic-value cleanup): a save failure is logged, never
// returned to the caller, whose run already succeeded and whose result is
// already about to be written back regardless.
func (h *APIClientHandler) saveLoadTestRun(itemID, collectionID, method, url string, cfg apiclient.LoadTestConfig, result *apiclient.LoadTestResult) {
	run := &apiclient.LoadTestRun{
		ItemID: itemID, CollectionID: collectionID, Method: method, URL: url,
		Config: apiclient.ClampLoadTestConfig(cfg),
		Result: result,
	}
	if err := h.store.SaveLoadTestRun(run); err != nil {
		log.Printf("airmock: failed to save load test run history: %v", err)
	}
}
```

Add the three new handlers (place near `loadTest`/`wsLoadTest`):

```go
func (h *APIClientHandler) listLoadTestRuns(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.ListLoadTestRuns(r.URL.Query().Get("itemId"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *APIClientHandler) getLoadTestRun(w http.ResponseWriter, r *http.Request) {
	run, err := h.store.GetLoadTestRun(chi.URLParam(r, "id"))
	if errors.Is(err, apiclient.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *APIClientHandler) deleteLoadTestRun(w http.ResponseWriter, r *http.Request) {
	if err := h.store.DeleteLoadTestRun(chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, apiclient.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

Register the routes in `Routes` (near the existing `r.Post("/loadtest", h.loadTest)` / `r.Post("/ws-loadtest", h.wsLoadTest)` lines):

```go
	r.Route("/loadtest-runs", func(r chi.Router) {
		r.Get("/", h.listLoadTestRuns)
		r.Get("/{id}", h.getLoadTestRun)
		r.Delete("/{id}", h.deleteLoadTestRun)
	})
```

In `internal/web/api/settings.go`, add the two new fields to `settingsBody`, `settingsBodyFrom`, `get`'s response (automatic via `settingsBodyFrom`), and `put`'s validation + construction:

```go
type settingsBody struct {
	HitLogMaxAgeDays          int      `json:"hitLogMaxAgeDays"`
	HitLogMaxRowsPerMock      int      `json:"hitLogMaxRowsPerMock"`
	MaxVersionsPerMock        int      `json:"maxVersionsPerMock"`
	RedactedHeaders           []string `json:"redactedHeaders"`
	DefaultResponseDelayMs    int      `json:"defaultResponseDelayMs"`
	DefaultFailureRatePercent float64  `json:"defaultFailureRatePercent"`
	MaxCapturedBodyBytes      int      `json:"maxCapturedBodyBytes"`
	SessionTimeoutMinutes     int      `json:"sessionTimeoutMinutes"`
	InactivityLockMinutes     int      `json:"inactivityLockMinutes"`
	LoadTestRunMaxAgeDays     int      `json:"loadTestRunMaxAgeDays"`
	LoadTestRunMaxRowsPerItem int      `json:"loadTestRunMaxRowsPerItem"`
}
```

```go
func settingsBodyFrom(s *settings.Settings) settingsBody {
	return settingsBody{
		HitLogMaxAgeDays:          s.HitLogMaxAgeDays,
		HitLogMaxRowsPerMock:      s.HitLogMaxRowsPerMock,
		MaxVersionsPerMock:        s.MaxVersionsPerMock,
		RedactedHeaders:           s.RedactedHeaders,
		DefaultResponseDelayMs:    s.DefaultResponseDelayMs,
		DefaultFailureRatePercent: s.DefaultFailureRatePercent,
		MaxCapturedBodyBytes:      s.MaxCapturedBodyBytes,
		SessionTimeoutMinutes:     s.SessionTimeoutMinutes,
		InactivityLockMinutes:     s.InactivityLockMinutes,
		LoadTestRunMaxAgeDays:     s.LoadTestRunMaxAgeDays,
		LoadTestRunMaxRowsPerItem: s.LoadTestRunMaxRowsPerItem,
	}
}
```

In `put`, extend the positive-number validation and the constructed `*settings.Settings`:

```go
	if body.HitLogMaxAgeDays <= 0 || body.HitLogMaxRowsPerMock <= 0 || body.MaxVersionsPerMock <= 0 || body.MaxCapturedBodyBytes <= 0 || body.SessionTimeoutMinutes <= 0 ||
		body.LoadTestRunMaxAgeDays <= 0 || body.LoadTestRunMaxRowsPerItem <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("hitLogMaxAgeDays, hitLogMaxRowsPerMock, maxVersionsPerMock, maxCapturedBodyBytes, sessionTimeoutMinutes, loadTestRunMaxAgeDays, and loadTestRunMaxRowsPerItem must all be positive"))
		return
	}
```

```go
	s := &settings.Settings{
		HitLogMaxAgeDays:          body.HitLogMaxAgeDays,
		HitLogMaxRowsPerMock:      body.HitLogMaxRowsPerMock,
		MaxVersionsPerMock:        body.MaxVersionsPerMock,
		RedactedHeaders:           body.RedactedHeaders,
		DefaultResponseDelayMs:    body.DefaultResponseDelayMs,
		DefaultFailureRatePercent: body.DefaultFailureRatePercent,
		MaxCapturedBodyBytes:      body.MaxCapturedBodyBytes,
		SessionTimeoutMinutes:     body.SessionTimeoutMinutes,
		InactivityLockMinutes:     body.InactivityLockMinutes,
		LoadTestRunMaxAgeDays:     body.LoadTestRunMaxAgeDays,
		LoadTestRunMaxRowsPerItem: body.LoadTestRunMaxRowsPerItem,
	}
```

In `internal/server/server.go`, add a new field to the `Server` struct (next to `retentionWorker *hitlog.RetentionWorker`, line 50):

```go
	loadTestRunRetentionWorker *apiclient.LoadTestRunRetentionWorker
```

Construct it in `New` (next to `retentionWorker := hitlog.NewRetentionWorker(hitLogStore, settingsStore)`, line 64):

```go
	loadTestRunRetentionWorker := apiclient.NewLoadTestRunRetentionWorker(apiClientStore, settingsStore)
```

Add it to the `&Server{...}` struct literal (next to `retentionWorker: retentionWorker,`, around line 213):

```go
		loadTestRunRetentionWorker: loadTestRunRetentionWorker,
```

Start it in `Run` (next to `s.retentionWorker.Start(ctx)`, around line 235):

```go
	s.loadTestRunRetentionWorker.Start(ctx)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/web/api/... ./internal/server/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web/api/apiclient.go internal/web/api/apiclient_test.go internal/web/api/settings.go internal/server/server.go
git commit -m "Persist load-test runs and expose history endpoints + retention settings"
```

---

### Task 7: Frontend API client — history calls + itemId/collectionId context

**Files:**
- Modify: `ui/src/lib/api.js`

**Interfaces:**
- Consumes: routes from Task 6.
- Produces: `api.runLoadTest(spec, variables, concurrency, totalRequests, durationSecs, detailed, signal, context)`, `api.runWSLoadTest(...)` (same new trailing `context` param), `api.listLoadTestRuns(itemId)`, `api.getLoadTestRun(id)`, `api.deleteLoadTestRun(id)` — consumed by Task 9 (`Collections.svelte`).

- [ ] **Step 1: Implement** (no separate test file for this module exists in the repo today — confirmed during brainstorming; verified instead by Task 9's manual pass)

In `ui/src/lib/api.js`, change `runLoadTest`/`runWSLoadTest` to accept an optional trailing `context` object (mirrors `executeRequest`'s existing `context = {}` convention immediately above them):

```js
  runLoadTest: (spec, variables, concurrency, totalRequests, durationSecs, detailed, signal, context = {}) =>
    request('POST', '/api/apiclient/loadtest', { spec, variables, concurrency, totalRequests, durationSecs, detailed, ...context }, signal),
  runWSLoadTest: (spec, variables, concurrency, totalRequests, durationSecs, detailed, signal, context = {}) =>
    request('POST', '/api/apiclient/ws-loadtest', { spec, variables, concurrency, totalRequests, durationSecs, detailed, ...context }, signal),
  listLoadTestRuns: (itemId) => request('GET', `/api/apiclient/loadtest-runs?itemId=${encodeURIComponent(itemId)}`),
  getLoadTestRun: (id) => request('GET', `/api/apiclient/loadtest-runs/${id}`),
  deleteLoadTestRun: (id) => request('DELETE', `/api/apiclient/loadtest-runs/${id}`),
```

- [ ] **Step 2: Commit**

```bash
git add ui/src/lib/api.js
git commit -m "Add load-test run history API client methods"
```

---

### Task 8: Chart components

**Files:**
- Modify: `ui/package.json`
- Create: `ui/src/lib/charts/LatencyOverTimeChart.svelte`
- Create: `ui/src/lib/charts/LatencyHistogram.svelte`
- Create: `ui/src/lib/charts/StatusBreakdownChart.svelte`

**Interfaces:**
- Produces: three Svelte components, each `export let` props as shown below — consumed by Task 9 (`Collections.svelte`).

- [ ] **Step 1: Add the dependency**

Run: `cd ui && npm install chart.js@^4.4.0`

Verify `ui/package.json` now has a `"dependencies"` section (this frontend's first) containing `"chart.js": "^4.4.0"`.

- [ ] **Step 2: Build `LatencyOverTimeChart.svelte`**

```svelte
<script>
  import { onMount, onDestroy } from 'svelte';
  import Chart from 'chart.js/auto';

  // samples: LoadTestSample[] — { index, elapsedMs, latencyMs, statusCode, error }
  export let samples = [];

  let canvasEl;
  let chart;

  function pointColor(s) {
    if (s.error) return '#ef4444';
    if (s.statusCode >= 500) return '#ef4444';
    if (s.statusCode >= 400) return '#f59e0b';
    if (s.statusCode >= 300) return '#3b82f6';
    return '#22c55e';
  }

  function buildData() {
    return {
      datasets: [
        {
          label: 'Latency (ms)',
          data: samples.map((s) => ({ x: s.elapsedMs, y: s.latencyMs })),
          pointBackgroundColor: samples.map(pointColor),
          pointBorderColor: samples.map(pointColor),
          pointRadius: 2,
          showLine: false,
        },
      ],
    };
  }

  onMount(() => {
    chart = new Chart(canvasEl, {
      type: 'scatter',
      data: buildData(),
      options: {
        responsive: true,
        maintainAspectRatio: false,
        scales: {
          x: { title: { display: true, text: 'Elapsed (ms)' } },
          y: { title: { display: true, text: 'Latency (ms)' }, beginAtZero: true },
        },
        plugins: { legend: { display: false } },
      },
    });
  });
  onDestroy(() => chart?.destroy());

  $: if (chart) {
    chart.data = buildData();
    chart.update();
  }
</script>

<div class="chart-wrap">
  <canvas bind:this={canvasEl}></canvas>
</div>

<style>
  .chart-wrap { position: relative; height: 220px; max-width: 100%; }
  canvas { width: 100% !important; height: 100% !important; }
</style>
```

- [ ] **Step 3: Build `LatencyHistogram.svelte`**

```svelte
<script>
  import { onMount, onDestroy } from 'svelte';
  import Chart from 'chart.js/auto';

  // samples: LoadTestSample[] — only latencyMs is used here
  export let samples = [];
  const BUCKET_COUNT = 10;

  let canvasEl;
  let chart;

  function buildBuckets() {
    if (samples.length === 0) return { labels: [], counts: [] };
    const latencies = samples.map((s) => s.latencyMs);
    const min = Math.min(...latencies);
    const max = Math.max(...latencies);
    const width = Math.max(1, Math.ceil((max - min + 1) / BUCKET_COUNT));
    const counts = new Array(BUCKET_COUNT).fill(0);
    for (const ms of latencies) {
      const idx = Math.min(BUCKET_COUNT - 1, Math.floor((ms - min) / width));
      counts[idx]++;
    }
    const labels = counts.map((_, i) => `${min + i * width}-${min + (i + 1) * width}`);
    return { labels, counts };
  }

  function buildData() {
    const { labels, counts } = buildBuckets();
    return {
      labels,
      datasets: [{ label: 'Requests', data: counts, backgroundColor: '#3b82f6' }],
    };
  }

  onMount(() => {
    chart = new Chart(canvasEl, {
      type: 'bar',
      data: buildData(),
      options: {
        responsive: true,
        maintainAspectRatio: false,
        scales: {
          x: { title: { display: true, text: 'Latency (ms)' } },
          y: { title: { display: true, text: 'Requests' }, beginAtZero: true, ticks: { precision: 0 } },
        },
        plugins: { legend: { display: false } },
      },
    });
  });
  onDestroy(() => chart?.destroy());

  $: if (chart) {
    chart.data = buildData();
    chart.update();
  }
</script>

<div class="chart-wrap">
  <canvas bind:this={canvasEl}></canvas>
</div>

<style>
  .chart-wrap { position: relative; height: 220px; max-width: 100%; }
  canvas { width: 100% !important; height: 100% !important; }
</style>
```

- [ ] **Step 4: Build `StatusBreakdownChart.svelte`**

```svelte
<script>
  import { onMount, onDestroy } from 'svelte';
  import Chart from 'chart.js/auto';

  // statuses: StatusCounts — { count2xx, count3xx, count4xx, count5xx, countError }
  export let statuses;

  let canvasEl;
  let chart;

  const LABELS = ['2xx', '3xx', '4xx', '5xx', 'Network error'];
  const COLORS = ['#22c55e', '#3b82f6', '#f59e0b', '#ef4444', '#991b1b'];

  function buildData() {
    return {
      labels: LABELS,
      datasets: [
        {
          data: [statuses.count2xx, statuses.count3xx, statuses.count4xx, statuses.count5xx, statuses.countError],
          backgroundColor: COLORS,
        },
      ],
    };
  }

  onMount(() => {
    chart = new Chart(canvasEl, {
      type: 'doughnut',
      data: buildData(),
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: { legend: { position: 'right' } },
      },
    });
  });
  onDestroy(() => chart?.destroy());

  $: if (chart) {
    chart.data = buildData();
    chart.update();
  }
</script>

<div class="chart-wrap">
  <canvas bind:this={canvasEl}></canvas>
</div>

<style>
  .chart-wrap { position: relative; height: 220px; max-width: 100%; }
  canvas { width: 100% !important; height: 100% !important; }
</style>
```

- [ ] **Step 5: Verify the frontend builds**

Run: `cd ui && npm run build`
Expected: succeeds with no errors (these three components aren't imported anywhere yet, so this just confirms they're valid Svelte + that `chart.js/auto` resolves).

- [ ] **Step 6: Commit**

```bash
git add ui/package.json ui/package-lock.json ui/src/lib/charts/LatencyOverTimeChart.svelte ui/src/lib/charts/LatencyHistogram.svelte ui/src/lib/charts/StatusBreakdownChart.svelte
git commit -m "Add Chart.js and the three load-test result chart components"
```

---

### Task 9: Wire history + charts into the Collections load-test panel

**Files:**
- Modify: `ui/src/lib/pages/Collections.svelte`
- Modify: `ui/src/lib/pages/Settings.svelte`

**Interfaces:**
- Consumes: `api.runLoadTest`/`runWSLoadTest` (Task 7, new `context` param), `api.listLoadTestRuns`/`getLoadTestRun` (Task 7), `LatencyOverTimeChart`/`LatencyHistogram`/`StatusBreakdownChart` (Task 8).

- [ ] **Step 1: Pass item/collection context into every run**

In `runLoadTest` (`ui/src/lib/pages/Collections.svelte`), capture the context alongside the other tab-snapshotted values and pass it through:

```js
  async function runLoadTest() {
    const isWS = activeItem?.type === 'wsrequest';
    const spec = isWS ? activeItem?.wsRequest : activeItem?.request;
    if (!spec) return;
    const tabId = activeTabId;
    const concurrency = ltConcurrency;
    const totalRequests = ltTotalRequests;
    const durationSecs = ltDurationSecs;
    const detailed = ltDetailed;
    const vars = effectiveVariables;
    // '' for a draft tab (activeTab.itemId is unset) — still runs and still
    // gets persisted (see api.js's runLoadTest), just invisible to any
    // item's own history list.
    const context = { itemId: activeTab?.itemId ?? '', collectionId: activeTab?.collectionId ?? '' };

    const controller = new AbortController();
    loadTestAbortControllersByTabId = { ...loadTestAbortControllersByTabId, [tabId]: controller };
    loadTestingByTabId = { ...loadTestingByTabId, [tabId]: true };
    updateTabDisplayState(tabId, { loadTestResult: null });
    try {
      const runner = isWS ? api.runWSLoadTest : api.runLoadTest;
      const result = await runner(spec, vars, concurrency, durationSecs > 0 ? 0 : totalRequests, durationSecs, detailed, controller.signal, context);
      updateTabDisplayState(tabId, { loadTestResult: result });
      if (context.itemId) await loadLoadTestHistory(context.itemId);
    } catch (e) {
      if (e.name === 'AbortError') {
        showToast('Load test stopped', 'ok');
      } else {
        showToast(e.message, 'err');
      }
    } finally {
      loadTestingByTabId = { ...loadTestingByTabId, [tabId]: false };
      const { [tabId]: _discard, ...rest } = loadTestAbortControllersByTabId;
      loadTestAbortControllersByTabId = rest;
    }
  }
```

- [ ] **Step 2: Add history state + loading/selection functions**

Add near the other load-test state declarations (next to `let loadTestResult = null;`):

```js
  let loadTestHistory = []; // LoadTestRunSummary[] for the active item
  let showLoadTestHistory = false;
  let loadingLoadTestHistory = false;

  async function loadLoadTestHistory(itemId) {
    if (!itemId) {
      loadTestHistory = [];
      return;
    }
    loadingLoadTestHistory = true;
    try {
      loadTestHistory = (await api.listLoadTestRuns(itemId)) ?? [];
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      loadingLoadTestHistory = false;
    }
  }

  async function toggleLoadTestHistory() {
    showLoadTestHistory = !showLoadTestHistory;
    if (showLoadTestHistory && activeTab?.itemId) await loadLoadTestHistory(activeTab.itemId);
  }

  async function viewLoadTestRun(runId) {
    try {
      const run = await api.getLoadTestRun(runId);
      updateTabDisplayState(activeTabId, { loadTestResult: run.result });
    } catch (e) {
      showToast(e.message, 'err');
    }
  }
```

- [ ] **Step 3: Add the History button and list to the template**

In the `.loadtest-card` block, add a "History" button next to the existing Run/Stop/Clear buttons, and the history list + charts below the existing stat tiles:

```svelte
              <button class="btn btn-primary" on:click={runLoadTest} disabled={loadTesting}>{loadTesting ? 'Running…' : 'Run'}</button>
              {#if loadTesting}<button class="btn btn-ghost btn-stop" on:click={forceStopLoadTest}>Stop</button>{/if}
              {#if loadTestResult && !loadTesting}<button class="btn btn-ghost" on:click={clearLoadTestResult}>Clear</button>{/if}
              {#if activeTab?.itemId}
                <button class="btn btn-ghost" on:click={toggleLoadTestHistory}>{showLoadTestHistory ? 'Hide history' : 'History'}</button>
              {/if}
            </div>
            <label class="toggle-label">
              <input type="checkbox" bind:checked={ltDetailed} />
              Detailed output (capture every request/response, not just the summary)
            </label>

            {#if showLoadTestHistory}
              <div class="lt-history">
                {#if loadingLoadTestHistory}
                  <p class="sub"><span class="loader-spin"></span>&nbsp; Loading history…</p>
                {:else if loadTestHistory.length === 0}
                  <p class="sub">No past runs for this request yet.</p>
                {:else}
                  <table class="lt-history-table">
                    <thead><tr><th>When</th><th>Requests</th><th>Req/s</th><th>p95</th><th>Error rate</th><th></th></tr></thead>
                    <tbody>
                      {#each loadTestHistory as run (run.id)}
                        <tr>
                          <td>{new Date(run.createdAt).toLocaleString()}</td>
                          <td>{run.totalRequests}</td>
                          <td>{run.requestsPerSec.toFixed(1)}</td>
                          <td>{run.p95Ms}ms</td>
                          <td>{run.errorRate.toFixed(1)}%</td>
                          <td><button class="btn btn-ghost small" on:click={() => viewLoadTestRun(run.id)}>View</button></td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                {/if}
              </div>
            {/if}

            {#if loadTesting}
              <p class="sub"><span class="loader-spin"></span>&nbsp; Running…</p>
            {:else if loadTestResult}
              <div class="lt-summary">
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.totalRequests}</span><span class="lt-stat-label">requests</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.requestsPerSec.toFixed(1)}</span><span class="lt-stat-label">req/s</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.durationMs}ms</span><span class="lt-stat-label">duration</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.minMs}ms</span><span class="lt-stat-label">min</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.avgMs}ms</span><span class="lt-stat-label">avg</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.p50Ms}ms</span><span class="lt-stat-label">p50</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.p90Ms}ms</span><span class="lt-stat-label">p90</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.p95Ms}ms</span><span class="lt-stat-label">p95</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.p99Ms}ms</span><span class="lt-stat-label">p99</span></div>
                <div class="lt-stat"><span class="lt-stat-val">{loadTestResult.maxMs}ms</span><span class="lt-stat-label">max</span></div>
              </div>
              <div class="lt-statuses">
                {#if loadTestResult.statuses.count2xx}<span class="chip badge-ok">{loadTestResult.statuses.count2xx} 2xx</span>{/if}
                {#if loadTestResult.statuses.count3xx}<span class="chip badge-info">{loadTestResult.statuses.count3xx} 3xx</span>{/if}
                {#if loadTestResult.statuses.count4xx}<span class="chip badge-warn">{loadTestResult.statuses.count4xx} 4xx</span>{/if}
                {#if loadTestResult.statuses.count5xx}<span class="chip badge-err">{loadTestResult.statuses.count5xx} 5xx</span>{/if}
                {#if loadTestResult.statuses.countError}<span class="chip badge-err">{loadTestResult.statuses.countError} network error</span>{/if}
              </div>
              <div class="lt-charts">
                <StatusBreakdownChart statuses={loadTestResult.statuses} />
                {#if loadTestResult.samples?.length}
                  <LatencyOverTimeChart samples={loadTestResult.samples} />
                  <LatencyHistogram samples={loadTestResult.samples} />
                {/if}
              </div>
              {#if loadTestResult.detailed}
                {@render loadTestSamplesTable(loadTestResult)}
              {/if}
            {/if}
```

Note the last change: `{@render loadTestSamplesTable(loadTestResult)}` is now gated on `loadTestResult.detailed` (it used to be gated only inside the snippet on `result.samples?.length`, which is no longer a useful signal now that samples are always present) — this preserves the existing behavior of only showing the full-response-inspection table for genuinely `Detailed` runs.

- [ ] **Step 4: Add the new imports and CSS**

Near the top `<script>` imports:

```js
  import LatencyOverTimeChart from '../charts/LatencyOverTimeChart.svelte';
  import LatencyHistogram from '../charts/LatencyHistogram.svelte';
  import StatusBreakdownChart from '../charts/StatusBreakdownChart.svelte';
```

Add to this file's `<style>` block:

```css
  .lt-charts { display: flex; flex-wrap: wrap; gap: 16px; margin: 12px 0; }
  .lt-charts > :global(.chart-wrap) { flex: 1 1 280px; min-width: 0; }
  .lt-history { margin: 10px 0; }
  .lt-history-table { width: 100%; border-collapse: collapse; font-size: 13px; }
  .lt-history-table th, .lt-history-table td { text-align: left; padding: 4px 8px; border-bottom: 1px solid var(--border); }
```

- [ ] **Step 5: Add the two new Settings page fields**

In `ui/src/lib/pages/Settings.svelte`, add to `DEFAULTS` and the matching `let` declarations (near `hitLogMaxAgeDays`):

```js
    loadTestRunMaxAgeDays: 30,
```
```js
    loadTestRunMaxRowsPerItem: 50,
```
```js
  let loadTestRunMaxAgeDays = 30;
  let loadTestRunMaxRowsPerItem = 50;
```

Add to the load-from-server assignment block (near `hitLogMaxAgeDays = s.hitLogMaxAgeDays;`, both places it appears — after initial load and after save):

```js
      loadTestRunMaxAgeDays = s.loadTestRunMaxAgeDays;
      loadTestRunMaxRowsPerItem = s.loadTestRunMaxRowsPerItem;
```

Add to the reset-to-defaults function (near `hitLogMaxAgeDays = DEFAULTS.hitLogMaxAgeDays;`):

```js
    loadTestRunMaxAgeDays = DEFAULTS.loadTestRunMaxAgeDays;
    loadTestRunMaxRowsPerItem = DEFAULTS.loadTestRunMaxRowsPerItem;
```

Add to the save payload (near `hitLogMaxAgeDays: Number(hitLogMaxAgeDays),`):

```js
        loadTestRunMaxAgeDays: Number(loadTestRunMaxAgeDays),
        loadTestRunMaxRowsPerItem: Number(loadTestRunMaxRowsPerItem),
```

Add the UI fields to the existing "Hit log retention" card (rename nothing — just extend it with a second `field-row`, right after the existing one):

```svelte
    <div class="field-row">
      <label>
        <span class="label-text">Load test history: max age (days)<InfoTooltip text="Controls how long past load-test runs (and their charts) are kept before being automatically purged. Applies on the next retention pass (runs hourly) — no restart needed." /></span>
        <input type="number" min="1" bind:value={loadTestRunMaxAgeDays} />
      </label>
      <label>
        Load test history: max runs per request
        <input type="number" min="1" bind:value={loadTestRunMaxRowsPerItem} />
      </label>
    </div>
```

- [ ] **Step 6: Build and manually verify**

Run: `cd ui && npm run build`
Expected: succeeds with no errors.

Manual/browser pass (no component-test harness exists in this repo today — confirmed during brainstorming):
1. Start a scratch instance: `HOME=/tmp/airmock-lt-test /path/to/built/binary serve --headless --admin-port 18600 --gateway-port 18601 --gateway-tls-port 18602`, point a `vite` dev server's proxy at port 18600, open Collections.
2. Create a request against a real mock (or `https://httpbin.org/get` if no local target is handy), open its Load Test panel, run it with `Detailed` OFF: confirm the stat tiles, `StatusBreakdownChart`, `LatencyOverTimeChart`, and `LatencyHistogram` all render; confirm the old per-request samples table does NOT appear.
3. Re-run the same request with `Detailed` ON: confirm the samples table NOW appears (with real response bodies/headers on expand), alongside the same three charts.
4. Click "History": confirm both runs appear, newest first, with correct req/s and p95. Click "View" on the older one: confirm the stat tiles and charts reload to match that run, not the most recent one.
5. Delete the collection containing this request (or query `load_test_runs`/`load_test_run_samples` directly via sqlite3 against the instance's DB file) and confirm no rows remain referencing it.
6. Revert the scratch instance's vite proxy config and stop both processes, per this repo's established manual-testing cleanup convention.

- [ ] **Step 7: Commit**

```bash
git add ui/src/lib/pages/Collections.svelte ui/src/lib/pages/Settings.svelte
git commit -m "Wire load-test run history and charts into the Collections load-test panel"
```

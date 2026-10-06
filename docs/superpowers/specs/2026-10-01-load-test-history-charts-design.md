# Load Test History + Charts — Design

## Context

AirMock already has a built-in load-test feature (`internal/apiclient/loadtest.go`): from the Collections page, a saved request (HTTP or WS) can be fired repeatedly across N concurrent workers, governed by a request-count or duration cap, with a hard safety ceiling (50 workers / 2000 requests / 60s). The result today is a flat row of stat tiles — total requests, req/s, duration, min/avg/p50/p90/p95/p99/max latency, and a 2xx/3xx/4xx/5xx/error breakdown — plus an optional raw per-request samples table when `Detailed` is on. Nothing is persisted: the moment you navigate away or run again, the previous result is gone.

The user asked to "plan more features for load test, add visual analysis of output." Brainstorming surfaced four candidate features beyond charts — run history, multi-step scenarios, ramp-up load shapes, and pass/fail thresholds — all of which the user wants eventually. Given the scope (several of these rework the same core run engine; others are independent), this was decomposed into three sub-projects, each to get its own design → spec → plan cycle:

1. **Run history + visual charts** (this spec) — foundational; delivers the "visual analysis" ask directly, and the other two benefit from runs being persisted rather than thrown away.
2. **Multi-step scenarios + ramp-up load shapes** — a bigger rework of the same core `runLoadTest` execution loop; deferred to its own spec.
3. **Pass/fail thresholds** — a small, mostly self-contained evaluation layer; deferred to its own spec.

This document covers **only** sub-project 1. Sub-projects 2 and 3 are intentionally out of scope here and will each get their own brainstorming pass before being spec'd.

## Design

### Data model

Two new tables (migration `000049`, additive-only per this codebase's existing migration convention — see e.g. `000004_async.down.sql`'s no-op pattern):

```sql
CREATE TABLE load_test_runs (
    id TEXT PRIMARY KEY,
    item_id TEXT NOT NULL DEFAULT '',       -- '' if run from an unsaved/draft request — still saved, just not listed under any item's history
    collection_id TEXT NOT NULL DEFAULT '',
    method TEXT NOT NULL,
    url TEXT NOT NULL,
    config_json TEXT NOT NULL,              -- the LoadTestConfig actually used, POST-clamping (see clampLoadTestConfig)
    result_json TEXT NOT NULL,              -- the aggregate LoadTestResult; Samples field always omitted here (stored separately below)
    created_at TIMESTAMP NOT NULL
);

CREATE TABLE load_test_run_samples (
    run_id TEXT NOT NULL REFERENCES load_test_runs(id),
    idx INTEGER NOT NULL,
    elapsed_ms INTEGER NOT NULL,  -- NEW field, ms since the run started — the x-axis every chart needs
    latency_ms INTEGER NOT NULL,
    status_code INTEGER NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, idx)
);
```

**Key decoupling**: today, per-request samples are only captured in memory at all when `LoadTestConfig.Detailed` is true, and `Detailed` also governs capturing the full response body/headers for the existing live "inspect what came back" samples table. This spec splits that in two:

- **Lightweight per-request capture** (the 4 sample columns above) happens on **every** run, regardless of `Detailed` — cheap (4 small values × up to 2000 requests), and what every chart is drawn from. This requires adding an `ElapsedMs` field to the in-memory `loadTestHit`/`LoadTestSample` structs (`internal/apiclient/loadtest.go`), computed as `time.Since(runStart).Milliseconds()` when each hit completes.
- `Detailed` keeps its exact current meaning — *additionally* capturing full response body/headers, for the live in-browser samples table only. These are never persisted to history (confirmed with the user: history keeps lightweight data only, not full response replay).

A run with zero samples (shouldn't happen under the above, but defensively: a 0-request run, or a future config that disables lightweight capture) still has its aggregate `result_json`, so the status-breakdown chart (built from the aggregate's `StatusCounts`, not from samples) always renders; only the latency-over-time and histogram charts need samples.

### Retention

Mirrors the existing pattern in `internal/hitlog/retention.go` (`RetentionWorker`, Settings-driven max-age-days + max-rows-per-key, ticker-driven sweep) rather than inventing a new style:

- Two new fields on the existing `settings.Settings` struct: `LoadTestRunMaxAgeDays` (default 30) and `LoadTestRunMaxRowsPerItem` (default 50) — exposed on the Settings page alongside the existing hit-log retention controls.
- New `apiclient.Store.EnforceLoadTestRunRetention(maxAgeDays, maxRowsPerItem int) error`, mirroring `hitlog.Store.EnforceMaxRowsPerMock`'s SQL shape (delete-outside-window-per-key).
- New `LoadTestRunRetentionWorker` (same `Start(ctx)` + `time.NewTicker` shape as `hitlog.RetentionWorker`), wired up in `internal/server/server.go` next to the existing hit-log retention worker.

**Cleanup on delete**: `collection_id`/`item_id` have no FK/CASCADE (SQLite, same convention as this codebase's `dynamic_counters`/`dynamic_csv_sources` tables). Without an explicit sweep, deleting a collection or a request item would orphan its load-test-run rows exactly the way deleting a mock orphaned its counter/CSV rows before that was fixed. `DeleteCollection` and whatever deletes a single item both get a matching `DELETE FROM load_test_runs WHERE collection_id = ?` / `... WHERE item_id = ?` (cascading to that run's own `load_test_run_samples` first), mirroring `mock.Store.Delete`'s own cleanup.

### API surface

Extends the existing blocking `loadTest`/`wsLoadTest` handlers (`internal/web/api/apiclient.go`) to persist after every run — no change to the response shape the caller already gets back, just a save as a side effect:

```
POST   /api/apiclient/load-test                    existing; loadTestRequest gains optional ItemID/CollectionID; now persists
POST   /api/apiclient/ws-load-test                  existing; same
GET    /api/apiclient/load-test-runs?itemId=<id>    list of run summaries for one item (id, createdAt, totalRequests, requestsPerSec, p95Ms, errorRate) — no samples, just enough for a history list row
GET    /api/apiclient/load-test-runs/{id}           one run's full aggregate result + its lightweight samples — what charts render from
DELETE /api/apiclient/load-test-runs/{id}           manual delete, for clearing clutter
```

New `apiclient.Store` methods: `SaveLoadTestRun`, `ListLoadTestRuns(itemID)`, `GetLoadTestRun(id)`, `DeleteLoadTestRun(id)`.

### Frontend

- `ui/src/lib/pages/Collections.svelte`'s existing load-test panel gains a "History" toggle next to the current Run/Stop/Clear buttons, listing past runs for the active item (timestamp, req/s, p95, error rate). Clicking a past run loads it into the *same* results view the live run already renders — "just ran" and "viewing history" become the same code path, not two parallel ones.
- **Chart.js** is added as this frontend's first production npm dependency (confirmed with the user — the alternative, hand-rolled SVG, was considered and rejected in favor of a library). New `ui/src/lib/charts/`:
  - `LatencyOverTimeChart.svelte` — line chart, x = `elapsedMs`, y = `latencyMs`, point color by status bucket. Needs samples; hidden if a run has none.
  - `LatencyHistogram.svelte` — bar chart, bucketed client-side from the lightweight samples. Needs samples; hidden if a run has none.
  - `StatusBreakdownChart.svelte` — doughnut/stacked-bar built from the aggregate `StatusCounts` alone — renders even for a run with zero samples.
  
  All three sit alongside the existing stat-tile row, not instead of it — the tiles remain the at-a-glance numbers; charts add the trend/shape view.
- `ui/src/lib/api.js` gains `listLoadTestRuns(itemId)`, `getLoadTestRun(id)`, `deleteLoadTestRun(id)`.

### Error handling

A persistence failure (DB write error after a run completes) must never fail the run itself or hide its result from the caller — log and continue, the same "best-effort, not fatal" convention already used for mock-version-history cleanup and the dynamic-value orphan-row cleanup elsewhere in this codebase. The retention sweep is similarly best-effort per the existing `hitlog.RetentionWorker` pattern.

### Testing

- Backend: round-trip save/list/get/delete for `load_test_runs`/`load_test_run_samples`; retention sweep enforcement (mirroring `hitlog`'s own retention tests); confirm a run saves a chartable aggregate (non-empty `StatusCounts`) even when it has zero samples; confirm `ElapsedMs` is populated and monotonically consistent with `Index` ordering within one run.
- Frontend: this repo has no component-test harness today (confirmed during brainstorming) — verified via the established manual/browser pass instead: run a load test, confirm it appears in history, confirm all three charts render with real data, confirm selecting a past run reloads it correctly, confirm a non-`Detailed` run still shows the status-breakdown chart with the other two correctly hidden.

## Out of scope (deferred to their own specs)

- Multi-step scenario load testing (a sequence of different requests under concurrency, not one repeated request).
- Ramp-up / load-shape concurrency scheduling.
- Pass/fail threshold rules.
- Comparing two runs side by side (a natural follow-on once history exists, but not requested as part of this sub-project).

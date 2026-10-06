package apiclient

import (
	"testing"
	"time"
)

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

// TestSaveLoadTestRunSetsRunIDOnTheSharedResultBeforeMarshaling guards the
// download-link feature: RunID must be set on run.Result in place (so a
// caller holding the same *LoadTestResult pointer sees it immediately,
// without re-fetching) AND must already be baked into the persisted
// result_json, so a later GetLoadTestRun round-trips the same non-empty
// RunID rather than requiring a separate migration/backfill.
func TestSaveLoadTestRunSetsRunIDOnTheSharedResultBeforeMarshaling(t *testing.T) {
	s := newTestStore(t)
	result := sampleLoadTestResult()
	run := &LoadTestRun{
		ItemID: "item-1", Method: "GET", URL: "http://example.com/x",
		Config: LoadTestConfig{Concurrency: 4, TotalRequests: 3},
		Result: result,
	}
	if err := s.SaveLoadTestRun(run); err != nil {
		t.Fatalf("SaveLoadTestRun: %v", err)
	}
	if result.RunID != run.ID {
		t.Fatalf("expected the caller's *LoadTestResult.RunID to be set to %q in place, got %q", run.ID, result.RunID)
	}

	got, err := s.GetLoadTestRun(run.ID)
	if err != nil {
		t.Fatalf("GetLoadTestRun: %v", err)
	}
	if got.Result.RunID != run.ID {
		t.Fatalf("expected RunID to round-trip through persistence, got %q want %q", got.Result.RunID, run.ID)
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

func timeNowMinus(days int) time.Time {
	return time.Now().UTC().AddDate(0, 0, -days)
}

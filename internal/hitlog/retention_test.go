package hitlog

import (
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/settings"
)

type fakeSettingsProvider struct {
	s   *settings.Settings
	err error
}

func (f *fakeSettingsProvider) Get() (*settings.Settings, error) { return f.s, f.err }

func recordAt(t *testing.T, s *Store, mockID string, createdAt time.Time) *Entry {
	t.Helper()
	e := &Entry{MockID: mockID, ProtocolType: "rest", Direction: DirectionInbound, ResponseStatus: 200, CreatedAt: createdAt}
	if err := s.Record(e); err != nil {
		t.Fatalf("Record: %v", err)
	}
	return e
}

func TestQueryFiltersByMockIDAndDateRange(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	recordAt(t, s, "m1", now.Add(-3*time.Hour))
	recordAt(t, s, "m1", now.Add(-1*time.Hour))
	recordAt(t, s, "m2", now.Add(-1*time.Hour)) // different mock, same time window

	// N hits generated across two mocks and two time points; filter by
	// mock_id + a date range that only covers the more recent one.
	results, err := s.Query(QueryOptions{
		MockID: "m1",
		Since:  now.Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 entry for m1 within the last 2h, got %d: %+v", len(results), results)
	}

	allM1, err := s.Query(QueryOptions{MockID: "m1"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(allM1) != 2 {
		t.Fatalf("expected 2 entries for m1 with no date filter, got %d", len(allM1))
	}
}

func TestRetentionWorkerPurgesOldRows(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	old := recordAt(t, s, "m1", now.Add(-48*time.Hour))
	recent := recordAt(t, s, "m1", now.Add(-1*time.Hour))

	n, err := s.DeleteOlderThan(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("DeleteOlderThan: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row purged, got %d", n)
	}

	if _, err := s.Get(old.ID); err != ErrNotFound {
		t.Fatalf("expected the old entry to be purged, got err=%v", err)
	}
	if _, err := s.Get(recent.ID); err != nil {
		t.Fatalf("expected the recent entry to survive retention, got err=%v", err)
	}
}

func TestRetentionWorkerHonorsConfiguredMaxAge(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	old := recordAt(t, s, "m1", now.Add(-48*time.Hour))
	recent := recordAt(t, s, "m1", now.Add(-1*time.Hour))

	// A 1-day max age (well inside the default 30-day policy) should purge
	// the 48h-old row but not the 1h-old one — proving purge() actually
	// reads the configured policy rather than the hardcoded default.
	provider := &fakeSettingsProvider{s: &settings.Settings{HitLogMaxAgeDays: 1, HitLogMaxRowsPerMock: 10_000}}
	w := NewRetentionWorker(s, provider)
	w.purge()

	if _, err := s.Get(old.ID); err != ErrNotFound {
		t.Fatalf("expected the 48h-old entry to be purged under a 1-day policy, got err=%v", err)
	}
	if _, err := s.Get(recent.ID); err != nil {
		t.Fatalf("expected the 1h-old entry to survive a 1-day policy, got err=%v", err)
	}
}

func TestRetentionWorkerFallsBackToDefaultsWhenSettingsUnavailable(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	// 10 days old: would survive the 30-day default, so this also proves
	// the fallback isn't an overly aggressive "purge everything" failure mode.
	within := recordAt(t, s, "m1", now.Add(-10*24*time.Hour))

	provider := &fakeSettingsProvider{err: ErrNotFound}
	w := NewRetentionWorker(s, provider)
	w.purge()

	if _, err := s.Get(within.ID); err != nil {
		t.Fatalf("expected the entry to survive under the default 30-day policy, got err=%v", err)
	}
}

func TestEnforceMaxRowsPerMockTrimsExcessOldestFirst(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	var ids []string
	for i := 0; i < 5; i++ {
		e := recordAt(t, s, "m1", now.Add(-time.Duration(5-i)*time.Minute))
		ids = append(ids, e.ID)
	}

	if err := s.EnforceMaxRowsPerMock(3); err != nil {
		t.Fatalf("EnforceMaxRowsPerMock: %v", err)
	}

	remaining, err := s.Query(QueryOptions{MockID: "m1", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(remaining) != 3 {
		t.Fatalf("expected 3 rows to remain, got %d", len(remaining))
	}
	// The two oldest (ids[0], ids[1]) must be gone; the three most recent survive.
	for _, id := range ids[:2] {
		if _, err := s.Get(id); err != ErrNotFound {
			t.Fatalf("expected oldest entry %s to be trimmed, got err=%v", id, err)
		}
	}
}

// TestEnforceMaxRowsPerMockAlsoCapsScheduledEventRows guards against a real
// gap: scheduledevent.Worker.Fire used to leave MockID empty entirely
// (recording only into Path, purely for display), so scheduled-event rows
// had NO count-based cap at all — only the age-based DeleteOlderThan.
// IntervalSecs can be as low as 1 second, so a single fast-firing event
// could accumulate millions of rows before the age cutoff ever purged
// anything. Fixed by MockID now holding the event's own stable ID (see
// worker.go's Fire and internal/hitlog.ScheduledEventMetricsSnapshot's own
// fix for the identical root cause), which this test confirms also flows
// through to EnforceMaxRowsPerMock's existing mock_id-keyed cap for free.
func TestEnforceMaxRowsPerMockAlsoCapsScheduledEventRows(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	var ids []string
	for i := 0; i < 5; i++ {
		e := &Entry{
			MockID: "event-1", ProtocolType: "http", Direction: DirectionScheduledEvent,
			Path: "Nightly ping", ResponseStatus: 200, CreatedAt: now.Add(-time.Duration(5-i) * time.Minute),
		}
		if err := s.Record(e); err != nil {
			t.Fatalf("Record: %v", err)
		}
		ids = append(ids, e.ID)
	}

	if err := s.EnforceMaxRowsPerMock(3); err != nil {
		t.Fatalf("EnforceMaxRowsPerMock: %v", err)
	}

	remaining, err := s.Query(QueryOptions{MockID: "event-1", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(remaining) != 3 {
		t.Fatalf("expected the scheduled event's rows to be capped at 3 like any other mock_id, got %d", len(remaining))
	}
	for _, id := range ids[:2] {
		if _, err := s.Get(id); err != ErrNotFound {
			t.Fatalf("expected oldest scheduled-event entry %s to be trimmed, got err=%v", id, err)
		}
	}
}

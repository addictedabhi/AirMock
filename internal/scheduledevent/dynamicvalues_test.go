package scheduledevent

import "testing"

const testCSV = "name,email\nAlice,alice@example.com\nBob,bob@example.com\n"

func TestEventCounterIncrementsByDefaultStepOfOne(t *testing.T) {
	s := newTestStore(t)
	for i, want := range []int64{1, 2, 3} {
		got, err := s.Counter("event-1", "hits", 1)
		if err != nil {
			t.Fatalf("Counter call %d: %v", i, err)
		}
		if got != want {
			t.Fatalf("Counter call %d = %d, want %d", i, got, want)
		}
	}
}

func TestEventNextCSVRowRoundRobinCyclesAndWraps(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.SetCSVSource("event-1", "round_robin", testCSV); err != nil {
		t.Fatalf("SetCSVSource: %v", err)
	}
	want := []string{"Alice", "Bob", "Alice"}
	for i, wantName := range want {
		row, ok, err := s.NextCSVRow("event-1")
		if err != nil {
			t.Fatalf("NextCSVRow call %d: %v", i, err)
		}
		if !ok || row["name"] != wantName {
			t.Fatalf("NextCSVRow call %d: got %+v, want name %q", i, row, wantName)
		}
	}
}

// TestDeleteEventCleansUpItsCounterAndCSVRows guards a real gap: owner_id
// has no FK/CASCADE tying dynamic_counters/dynamic_csv_sources rows to the
// scheduled_events table, so without Store.Delete's own explicit cleanup,
// a deleted event's counters/CSV attachment would sit orphaned in the
// database forever, keyed by a UUID nothing can ever reach again.
func TestDeleteEventCleansUpItsCounterAndCSVRows(t *testing.T) {
	s := newTestStore(t)
	e, err := s.Create(&Event{Name: "cleanup-target", Enabled: true, IntervalSecs: 30, TargetURL: "http://example.com/hook"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Counter(e.ID, "hits", 1); err != nil {
		t.Fatalf("Counter: %v", err)
	}
	if _, err := s.SetCSVSource(e.ID, "round_robin", testCSV); err != nil {
		t.Fatalf("SetCSVSource: %v", err)
	}

	if err := s.Delete(e.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var counterRows, csvRows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dynamic_counters WHERE owner_id = ?`, e.ID).Scan(&counterRows); err != nil {
		t.Fatalf("count dynamic_counters: %v", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dynamic_csv_sources WHERE owner_id = ?`, e.ID).Scan(&csvRows); err != nil {
		t.Fatalf("count dynamic_csv_sources: %v", err)
	}
	if counterRows != 0 || csvRows != 0 {
		t.Fatalf("expected the deleted event's counter/csv rows to be cleaned up, got %d counter rows and %d csv rows", counterRows, csvRows)
	}
}

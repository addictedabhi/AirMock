package scheduledevent

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/storage"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func TestCreateGetListUpdateDelete(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(&Event{Name: "heartbeat", Enabled: true, IntervalSecs: 30, TargetURL: "http://example.com/hook"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected an ID to be assigned")
	}
	if created.Method != "POST" {
		t.Fatalf("expected default method POST, got %q", created.Method)
	}
	if created.NextFireAt.Before(created.CreatedAt) || created.NextFireAt.After(created.CreatedAt.Add(31*time.Second)) {
		t.Fatalf("expected NextFireAt ~30s after creation, got %s vs %s", created.NextFireAt, created.CreatedAt)
	}

	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "heartbeat" {
		t.Fatalf("expected name 'heartbeat', got %q", got.Name)
	}

	list, err := s.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("List: %v (len=%d)", err, len(list))
	}

	created.Name = "renamed"
	created.IntervalSecs = 60
	updated, err := s.Update(created)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "renamed" {
		t.Fatalf("expected updated name 'renamed', got %q", updated.Name)
	}

	if err := s.Delete(created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(created.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestClaimDue(t *testing.T) {
	s := newTestStore(t)

	notDue, err := s.Create(&Event{Name: "far-future", Enabled: true, IntervalSecs: 3600, TargetURL: "http://example.com/a"})
	if err != nil {
		t.Fatalf("Create notDue: %v", err)
	}
	due, err := s.Create(&Event{Name: "due-now", Enabled: true, IntervalSecs: 1, TargetURL: "http://example.com/b"})
	if err != nil {
		t.Fatalf("Create due: %v", err)
	}
	disabled, err := s.Create(&Event{Name: "disabled", Enabled: false, IntervalSecs: 1, TargetURL: "http://example.com/c"})
	if err != nil {
		t.Fatalf("Create disabled: %v", err)
	}

	// Force due/disabled into the past so ClaimDue actually sees them as due.
	forceNextFireInPast(t, s, due.ID)
	forceNextFireInPast(t, s, disabled.ID)

	claimed, err := s.ClaimDue(10)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != due.ID {
		t.Fatalf("expected exactly the enabled due event to be claimed, got %+v", claimed)
	}

	// Claiming reschedules it into the future — a second immediate claim
	// must not re-claim it.
	again, err := s.ClaimDue(10)
	if err != nil {
		t.Fatalf("ClaimDue second call: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected no events claimed on the second call, got %+v", again)
	}

	// notDue and the disabled one were never claimed.
	stillPending, err := s.Get(notDue.ID)
	if err != nil {
		t.Fatalf("Get notDue: %v", err)
	}
	if !stillPending.NextFireAt.After(time.Now()) {
		t.Fatalf("expected notDue's next fire to still be in the future")
	}
}

func forceNextFireInPast(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE scheduled_events SET next_fire_at=? WHERE id=?`, formatTime(time.Now().UTC().Add(-time.Minute)), id); err != nil {
		t.Fatalf("force next_fire_at: %v", err)
	}
}

func TestRecordFireResult(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(&Event{Name: "e", Enabled: true, IntervalSecs: 10, TargetURL: "http://example.com"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.RecordFireResult(created.ID, 204, nil); err != nil {
		t.Fatalf("RecordFireResult: %v", err)
	}
	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastStatus != 204 || got.LastFiredAt == nil || got.LastError != "" {
		t.Fatalf("unexpected fire result: %+v", got)
	}
}

package settings

import (
	"path/filepath"
	"testing"

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

func TestGetReturnsDefaultsWhenNoRowExists(t *testing.T) {
	s := newTestStore(t)

	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.HitLogMaxAgeDays != DefaultHitLogMaxAgeDays || got.HitLogMaxRowsPerMock != DefaultHitLogMaxRowsPerMock {
		t.Fatalf("expected defaults, got %+v", got)
	}
	if got.MaxVersionsPerMock != DefaultMaxVersionsPerMock {
		t.Fatalf("expected the default max versions per mock, got %d", got.MaxVersionsPerMock)
	}
	if len(got.RedactedHeaders) != len(DefaultRedactedHeaders) {
		t.Fatalf("expected default redacted headers, got %+v", got.RedactedHeaders)
	}
}

func TestMaxVersionsPerMockRoundTrip(t *testing.T) {
	s := newTestStore(t)

	if err := s.Save(&Settings{HitLogMaxAgeDays: 30, HitLogMaxRowsPerMock: 10_000, MaxVersionsPerMock: 5}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MaxVersionsPerMock != 5 {
		t.Fatalf("expected the configured max versions per mock to round-trip, got %d", got.MaxVersionsPerMock)
	}
}

// TestMaxVersionsPerMockZeroFallsBackToDefault guards a real data-migration
// concern: an existing app_settings row saved before this column existed
// gets backfilled to 0 by the additive migration, and 0 would defeat the
// whole feature (nothing left to ever restore) if honored literally — it's
// treated as "not configured yet," not a real user choice.
func TestMaxVersionsPerMockZeroFallsBackToDefault(t *testing.T) {
	s := newTestStore(t)

	if err := s.Save(&Settings{HitLogMaxAgeDays: 30, HitLogMaxRowsPerMock: 10_000, MaxVersionsPerMock: 0}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MaxVersionsPerMock != DefaultMaxVersionsPerMock {
		t.Fatalf("expected 0 to fall back to the default, got %d", got.MaxVersionsPerMock)
	}
}

func TestRedactedHeadersRoundTrip(t *testing.T) {
	s := newTestStore(t)

	custom := []string{"Authorization", "X-Custom-Secret"}
	if err := s.Save(&Settings{HitLogMaxAgeDays: 30, HitLogMaxRowsPerMock: 10_000, RedactedHeaders: custom}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.RedactedHeaders) != 2 || got.RedactedHeaders[0] != "Authorization" || got.RedactedHeaders[1] != "X-Custom-Secret" {
		t.Fatalf("expected the custom redacted headers to round-trip, got %+v", got.RedactedHeaders)
	}

	// An explicitly empty list is a real, respected choice — not silently
	// replaced with defaults — since a user might genuinely want to turn
	// redaction off for local debugging.
	if err := s.Save(&Settings{HitLogMaxAgeDays: 30, HitLogMaxRowsPerMock: 10_000, RedactedHeaders: []string{}}); err != nil {
		t.Fatalf("Save (empty): %v", err)
	}
	got2, err := s.Get()
	if err != nil {
		t.Fatalf("Get (empty): %v", err)
	}
	if len(got2.RedactedHeaders) != 0 {
		t.Fatalf("expected an explicitly empty redacted-headers list to be respected, got %+v", got2.RedactedHeaders)
	}
}

func TestSaveAndGetRoundTrip(t *testing.T) {
	s := newTestStore(t)

	if err := s.Save(&Settings{HitLogMaxAgeDays: 7, HitLogMaxRowsPerMock: 500}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.HitLogMaxAgeDays != 7 || got.HitLogMaxRowsPerMock != 500 {
		t.Fatalf("unexpected settings after save: %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("expected UpdatedAt to be set")
	}

	// Saving again should update the same row, not insert a second one.
	if err := s.Save(&Settings{HitLogMaxAgeDays: 14, HitLogMaxRowsPerMock: 1000}); err != nil {
		t.Fatalf("Save (second): %v", err)
	}
	got2, err := s.Get()
	if err != nil {
		t.Fatalf("Get (second): %v", err)
	}
	if got2.HitLogMaxAgeDays != 14 || got2.HitLogMaxRowsPerMock != 1000 {
		t.Fatalf("expected the second save to overwrite the first, got %+v", got2)
	}
}

func TestGetReturnsDefaultMaxCapturedBodyBytesWhenNoRowExists(t *testing.T) {
	s := newTestStore(t)

	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MaxCapturedBodyBytes != DefaultMaxCapturedBodyBytes {
		t.Fatalf("expected the default max captured body bytes, got %d", got.MaxCapturedBodyBytes)
	}
}

// TestMaxCapturedBodyBytesZeroFallsBackToDefault mirrors
// TestMaxVersionsPerMockZeroFallsBackToDefault's own reasoning: a
// pre-existing row from before this column existed is backfilled to 0 by
// the additive migration, indistinguishable from an explicit "capture
// nothing" — treated as "not configured yet" rather than honored literally.
func TestMaxCapturedBodyBytesZeroFallsBackToDefault(t *testing.T) {
	s := newTestStore(t)

	if err := s.Save(&Settings{HitLogMaxAgeDays: 30, HitLogMaxRowsPerMock: 10_000, MaxVersionsPerMock: 20, MaxCapturedBodyBytes: 0}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MaxCapturedBodyBytes != DefaultMaxCapturedBodyBytes {
		t.Fatalf("expected 0 to fall back to the default, got %d", got.MaxCapturedBodyBytes)
	}
}

func TestSessionTimeoutMinutesRoundTrip(t *testing.T) {
	s := newTestStore(t)

	if err := s.Save(&Settings{HitLogMaxAgeDays: 30, HitLogMaxRowsPerMock: 10_000, MaxVersionsPerMock: 20, MaxCapturedBodyBytes: 65536, SessionTimeoutMinutes: 5}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SessionTimeoutMinutes != 5 {
		t.Fatalf("expected 5, got %d", got.SessionTimeoutMinutes)
	}
}

// TestSessionTimeoutMinutesZeroFallsBackToDefault mirrors
// TestMaxCapturedBodyBytesZeroFallsBackToDefault's own reasoning: 0 would
// mean every session expires instantly, not a meaningful value to honor.
func TestSessionTimeoutMinutesZeroFallsBackToDefault(t *testing.T) {
	s := newTestStore(t)

	if err := s.Save(&Settings{HitLogMaxAgeDays: 30, HitLogMaxRowsPerMock: 10_000, MaxVersionsPerMock: 20, MaxCapturedBodyBytes: 65536, SessionTimeoutMinutes: 0}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SessionTimeoutMinutes != DefaultSessionTimeoutMinutes {
		t.Fatalf("expected 0 to fall back to the default, got %d", got.SessionTimeoutMinutes)
	}
}

func TestInactivityLockMinutesRoundTrip(t *testing.T) {
	s := newTestStore(t)

	if err := s.Save(&Settings{HitLogMaxAgeDays: 30, HitLogMaxRowsPerMock: 10_000, MaxVersionsPerMock: 20, MaxCapturedBodyBytes: 65536, SessionTimeoutMinutes: 60, InactivityLockMinutes: 5}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.InactivityLockMinutes != 5 {
		t.Fatalf("expected 5, got %d", got.InactivityLockMinutes)
	}
}

// TestInactivityLockMinutesZeroMeansOff is the opposite of
// TestSessionTimeoutMinutesZeroFallsBackToDefault: unlike session timeout,
// 0 here is a real, intentional "off" value — a fresh instance that never
// touches this setting should NOT have it silently fall back to some
// non-zero default, since auto-lock is a purely opt-in feature.
func TestInactivityLockMinutesZeroMeansOff(t *testing.T) {
	s := newTestStore(t)

	if err := s.Save(&Settings{HitLogMaxAgeDays: 30, HitLogMaxRowsPerMock: 10_000, MaxVersionsPerMock: 20, MaxCapturedBodyBytes: 65536, SessionTimeoutMinutes: 60, InactivityLockMinutes: 0}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.InactivityLockMinutes != 0 {
		t.Fatalf("expected 0 (off) to be honored literally, got %d", got.InactivityLockMinutes)
	}
}

func TestGetReturnsInactivityLockOffWhenNoRowExists(t *testing.T) {
	s := newTestStore(t)
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.InactivityLockMinutes != 0 {
		t.Fatalf("expected a fresh instance to default to auto-lock off, got %d", got.InactivityLockMinutes)
	}
}

// TestDefaultResponseDelayAndFailureRateRoundTrip guards the two fields
// that pre-fill a brand-new mock's own response delay and fault error
// rate — unlike MaxVersionsPerMock/MaxCapturedBodyBytes above, 0 is their
// genuinely valid default (no delay, no injected failures) rather than a
// "not configured yet" sentinel, so no fallback-on-zero logic applies here.
func TestDefaultResponseDelayAndFailureRateRoundTrip(t *testing.T) {
	s := newTestStore(t)

	if err := s.Save(&Settings{
		HitLogMaxAgeDays: 30, HitLogMaxRowsPerMock: 10_000, MaxVersionsPerMock: 20, MaxCapturedBodyBytes: 4096,
		DefaultResponseDelayMs: 250, DefaultFailureRatePercent: 12.5,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DefaultResponseDelayMs != 250 || got.DefaultFailureRatePercent != 12.5 {
		t.Fatalf("expected both defaults to round-trip, got %+v", got)
	}
}

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

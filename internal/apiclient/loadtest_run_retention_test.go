package apiclient

import (
	"context"
	"errors"
	"testing"

	"github.com/addictedabhi/airmock/internal/settings"
)

type fakeLoadTestRunSettingsProvider struct{ s *settings.Settings }

func (f *fakeLoadTestRunSettingsProvider) Get() (*settings.Settings, error) { return f.s, nil }

type erroringLoadTestRunSettingsProvider struct{ err error }

func (e *erroringLoadTestRunSettingsProvider) Get() (*settings.Settings, error) { return nil, e.err }

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

	// Create a provider that returns an error — tests fallback to defaults (30-day/50-row)
	provider := &erroringLoadTestRunSettingsProvider{err: errors.New("boom")}
	w := NewLoadTestRunRetentionWorker(s, provider)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	if _, err := s.GetLoadTestRun(run.ID); err != nil {
		t.Fatalf("expected a fresh run to survive the default 30-day/50-row retention, got %v", err)
	}
}

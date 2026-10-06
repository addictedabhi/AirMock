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

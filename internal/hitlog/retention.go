package hitlog

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/addictedabhi/airmock/internal/settings"
)

const defaultPurgeInterval = 1 * time.Hour

// SettingsProvider is the narrow slice of internal/settings.Store this
// worker needs — read fresh on every purge cycle (rather than snapshotting
// once at construction) so a policy change made via the Settings page takes
// effect on the next tick without restarting the process.
type SettingsProvider interface {
	Get() (*settings.Settings, error)
}

// RetentionWorker periodically purges old/excess hit_logs rows so the
// database file doesn't grow unbounded from a mock left running for weeks.
type RetentionWorker struct {
	store            *Store
	settingsProvider SettingsProvider
	interval         time.Duration
}

func NewRetentionWorker(store *Store, settingsProvider SettingsProvider) *RetentionWorker {
	return &RetentionWorker{
		store:            store,
		settingsProvider: settingsProvider,
		interval:         defaultPurgeInterval,
	}
}

// Start runs an immediate purge, then repeats on w.interval until ctx is
// cancelled.
func (w *RetentionWorker) Start(ctx context.Context) {
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

func (w *RetentionWorker) purge() {
	maxAgeDays, maxRowsPerMock := settings.DefaultHitLogMaxAgeDays, settings.DefaultHitLogMaxRowsPerMock
	if w.settingsProvider != nil {
		if s, err := w.settingsProvider.Get(); err != nil {
			log.Printf("airmock: hit log retention: failed to load settings, using defaults: %v", err)
		} else {
			maxAgeDays, maxRowsPerMock = s.HitLogMaxAgeDays, s.HitLogMaxRowsPerMock
		}
	}
	maxAge := time.Duration(maxAgeDays) * 24 * time.Hour

	if n, err := w.store.DeleteOlderThan(time.Now().UTC().Add(-maxAge)); err != nil {
		log.Printf("airmock: hit log retention (age) failed: %v", err)
	} else if n > 0 {
		log.Printf("airmock: hit log retention purged %d row(s) older than %s", n, maxAge)
	}
	if err := w.store.EnforceMaxRowsPerMock(maxRowsPerMock); err != nil {
		log.Printf("airmock: hit log retention (max rows) failed: %v", err)
	}
}

// DeleteOlderThan removes every row created before cutoff, returning how
// many were deleted.
func (s *Store) DeleteOlderThan(cutoff time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM hit_logs WHERE created_at < ?`, formatTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("delete old hit logs: %w", err)
	}
	return res.RowsAffected()
}

// EnforceMaxRowsPerMock trims each mock_id's history to its most recent
// maxRows entries. This also covers callback (internal/mock.CallbackWorker)
// and scheduled-event (internal/scheduledevent.Worker) rows, both of which
// set MockID to their own stable ID — a scheduled event's Fire, in
// particular, used to leave mock_id NULL entirely, so a fast-firing event
// (IntervalSecs as low as 1) had no count cap at all, only the 30-day-
// default age cutoff, and could accumulate millions of rows between
// purges; that's fixed simply by MockID now being set. Only entries with
// genuinely no mock_id at all (plain outbound API-client calls, which
// aren't tied to any single mock/event) are left to age out via
// DeleteOlderThan only.
func (s *Store) EnforceMaxRowsPerMock(maxRows int) error {
	rows, err := s.db.Query(`SELECT DISTINCT mock_id FROM hit_logs WHERE mock_id IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("list mock ids: %w", err)
	}
	var mockIDs []string
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan mock id: %w", err)
		}
		if id.Valid {
			mockIDs = append(mockIDs, id.String)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, mockID := range mockIDs {
		_, err := s.db.Exec(
			`DELETE FROM hit_logs WHERE mock_id = ? AND id NOT IN (
			   SELECT id FROM hit_logs WHERE mock_id = ? ORDER BY created_at DESC LIMIT ?
			 )`,
			mockID, mockID, maxRows,
		)
		if err != nil {
			return fmt.Errorf("enforce max rows for mock %s: %w", mockID, err)
		}
	}
	return nil
}

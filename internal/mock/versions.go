package mock

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/addictedabhi/airmock/internal/settings"
)

// defaultMaxVersionsPerMock is the fallback used when no SettingsProvider is
// configured (or its own Get call fails) — matches
// settings.DefaultMaxVersionsPerMock, kept as a separate literal so this
// package doesn't need to import settings just to read one constant back.
const defaultMaxVersionsPerMock = 20

// SettingsProvider is the narrow slice of internal/settings.Store this
// package needs — read fresh on every version snapshot (rather than cached
// once at construction) so a change made via the Settings page takes effect
// on the very next edit, without restarting the process.
type SettingsProvider interface {
	Get() (*settings.Settings, error)
}

// maxVersionsPerMock bounds mock_versions' growth per mock — old edits
// beyond the most recent N aren't useful for "undo my last change" (the
// feature's actual purpose), so pruning them keeps the table from growing
// unbounded on a mock that's edited constantly (e.g. iterated on for
// hours). Globally configurable via the Settings page; s.settingsProvider
// being nil (no provider wired up, e.g. in a test) falls back to
// defaultMaxVersionsPerMock.
func (s *Store) maxVersionsPerMock() int {
	if s.settingsProvider == nil {
		return defaultMaxVersionsPerMock
	}
	cfg, err := s.settingsProvider.Get()
	if err != nil || cfg.MaxVersionsPerMock <= 0 {
		return defaultMaxVersionsPerMock
	}
	return cfg.MaxVersionsPerMock
}

// EnforceMaxVersionsForAllMocks immediately prunes EVERY mock's version
// history down to maxVersions, most-recent-first — called right after the
// Settings page saves a new (lower) MaxVersionsPerMock so a mock that
// already has more history than the new limit gets trimmed right away,
// rather than waiting for its next edit to naturally hit snapshotVersion's
// own pruning. Mirrors internal/hitlog.Store.EnforceMaxRowsPerMock's own
// "list distinct ids, then prune each" shape.
func (s *Store) EnforceMaxVersionsForAllMocks(maxVersions int) error {
	rows, err := s.db.Query(`SELECT DISTINCT mock_id FROM mock_versions`)
	if err != nil {
		return fmt.Errorf("list mock ids with version history: %w", err)
	}
	var mockIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan mock id: %w", err)
		}
		mockIDs = append(mockIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, mockID := range mockIDs {
		_, err := s.db.Exec(
			`DELETE FROM mock_versions WHERE mock_id = ? AND id NOT IN (
			   SELECT id FROM mock_versions WHERE mock_id = ? ORDER BY created_at DESC LIMIT ?
			 )`,
			mockID, mockID, maxVersions,
		)
		if err != nil {
			return fmt.Errorf("enforce max versions for mock %s: %w", mockID, err)
		}
	}
	return nil
}

// Version is one past state of a mock, snapshotted immediately before an
// Update overwrote it.
type Version struct {
	ID         string     `json:"id"`
	MockID     string     `json:"mockId"`
	Definition Definition `json:"definition"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// snapshotVersion records id's CURRENT row (before the caller's pending
// update overwrites it) into mock_versions, then prunes anything beyond
// maxVersionsPerMock. A mock with no existing row (Update called on an id
// that doesn't exist) has nothing to snapshot — Update's own "0 rows
// affected" check surfaces that as ErrNotFound right after this returns, so
// this just quietly does nothing rather than erroring first.
func (s *Store) snapshotVersion(id string) error {
	existing, err := s.Get(id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load current mock state for version snapshot: %w", err)
	}

	b, err := json.Marshal(existing)
	if err != nil {
		return fmt.Errorf("marshal version snapshot: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO mock_versions (id, mock_id, definition_json, created_at) VALUES (?, ?, ?, ?)`,
		uuid.NewString(), id, string(b), formatTime(time.Now().UTC()),
	)
	if err != nil {
		return fmt.Errorf("insert version snapshot: %w", err)
	}

	_, err = s.db.Exec(
		`DELETE FROM mock_versions WHERE mock_id = ? AND id NOT IN (
			SELECT id FROM mock_versions WHERE mock_id = ? ORDER BY created_at DESC LIMIT ?
		)`, id, id, s.maxVersionsPerMock(),
	)
	if err != nil {
		return fmt.Errorf("prune old version snapshots: %w", err)
	}
	return nil
}

// ListVersions returns mockID's snapshotted history, most recent first.
func (s *Store) ListVersions(mockID string) ([]*Version, error) {
	rows, err := s.db.Query(
		`SELECT id, mock_id, definition_json, created_at FROM mock_versions WHERE mock_id = ? ORDER BY created_at DESC`,
		mockID,
	)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer rows.Close()

	out := []*Version{}
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func scanVersion(row scanner) (*Version, error) {
	var v Version
	var defJSON, createdAt string
	if err := row.Scan(&v.ID, &v.MockID, &defJSON, &createdAt); err != nil {
		return nil, fmt.Errorf("scan version: %w", err)
	}
	if err := json.Unmarshal([]byte(defJSON), &v.Definition); err != nil {
		return nil, fmt.Errorf("unmarshal version definition: %w", err)
	}
	var err error
	if v.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse version created_at: %w", err)
	}
	return &v, nil
}

// RestoreVersion overwrites mockID's current state with versionID's
// snapshotted definition, via the normal Update path — which itself
// snapshots whatever was current immediately before, so a restore is just
// as undoable as any other edit rather than needing special-cased handling.
func (s *Store) RestoreVersion(mockID, versionID string) (*Definition, error) {
	var defJSON string
	err := s.db.QueryRow(
		`SELECT definition_json FROM mock_versions WHERE id = ? AND mock_id = ?`, versionID, mockID,
	).Scan(&defJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load version: %w", err)
	}

	var def Definition
	if err := json.Unmarshal([]byte(defJSON), &def); err != nil {
		return nil, fmt.Errorf("unmarshal version: %w", err)
	}
	def.ID = mockID
	return s.Update(&def)
}

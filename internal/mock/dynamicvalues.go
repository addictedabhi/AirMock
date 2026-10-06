package mock

import (
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"math/rand"
	"strings"
)

// DynamicValueSource is what a response template's counter()/csv() functions
// (see template.go's funcMap) read and write through — implemented by
// *Store below, and independently by internal/scheduledevent.Store (same
// method signatures, satisfied structurally: a scheduled event's own
// counters/CSV rows are scoped by its own id exactly the way a mock's are
// scoped by its id, without scheduledevent needing to import this package
// for anything beyond RenderOptions itself).
type DynamicValueSource interface {
	// Counter atomically adds step to the named counter scoped to ownerID
	// (creating it at value=step if this is the first call for that pair)
	// and returns the value AFTER applying step. A negative step decrements.
	Counter(ownerID, name string, step int64) (int64, error)
	// NextCSVRow returns the currently-selected row (by column name) of the
	// CSV attached to ownerID, per whichever selection mode it was attached
	// with. ok is false if no CSV is attached.
	NextCSVRow(ownerID string) (row map[string]string, ok bool, err error)
}

// ErrEmptyCSV guards against attaching a CSV with a header row but no data
// rows — nothing for csv() to ever return, so rejected at attach time
// rather than failing every render later.
var ErrEmptyCSV = errors.New("mock: csv must have at least one data row")

// ErrInvalidCSVMode guards SetCSVSource's mode argument against anything
// other than the two selection modes NextCSVRow actually implements.
var ErrInvalidCSVMode = errors.New("mock: csv mode must be \"round_robin\" or \"random\"")

// Counter implements DynamicValueSource. The INSERT...ON CONFLICT...
// RETURNING is one atomic statement — this project's sqlite is already
// opened with a single connection (internal/storage/sqlite.go), which
// serializes every write, so no additional locking is needed here.
func (s *Store) Counter(ownerID, name string, step int64) (int64, error) {
	var value int64
	err := s.db.QueryRow(
		`INSERT INTO dynamic_counters (owner_id, name, value, updated_at)
		 VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(owner_id, name) DO UPDATE SET
		   value = dynamic_counters.value + excluded.value,
		   updated_at = CURRENT_TIMESTAMP
		 RETURNING value`,
		ownerID, name, step,
	).Scan(&value)
	if err != nil {
		return 0, fmt.Errorf("counter: %w", err)
	}
	return value, nil
}

// NextCSVRow implements DynamicValueSource. next_index is unconditionally
// incremented on every call (one atomic UPDATE...RETURNING) regardless of
// mode — round-robin uses it (modulo the row count) to pick the row, random
// mode ignores it and rolls its own index; incrementing it anyway avoids a
// mode-dependent branch in SQL, and a drifting-but-unused counter in random
// mode costs nothing.
func (s *Store) NextCSVRow(ownerID string) (map[string]string, bool, error) {
	var mode, content string
	var claimedIndex int64
	err := s.db.QueryRow(
		`UPDATE dynamic_csv_sources SET next_index = next_index + 1, updated_at = CURRENT_TIMESTAMP
		 WHERE owner_id = ?
		 RETURNING mode, content, next_index - 1`,
		ownerID,
	).Scan(&mode, &content, &claimedIndex)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("next csv row: %w", err)
	}

	header, rows, err := parseCSVContent(content)
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}

	var rowIdx int
	if mode == "random" {
		rowIdx = rand.Intn(len(rows))
	} else {
		rowIdx = int(((claimedIndex % int64(len(rows))) + int64(len(rows))) % int64(len(rows)))
	}
	row := rows[rowIdx]
	out := make(map[string]string, len(header))
	for i, col := range header {
		if i < len(row) {
			out[col] = row[i]
		}
	}
	return out, true, nil
}

// CSVSourceMeta is what the UI needs to show an attached CSV without
// re-uploading it — never the raw content itself.
type CSVSourceMeta struct {
	Mode     string   `json:"mode"`
	RowCount int      `json:"rowCount"`
	Columns  []string `json:"columns"`
}

// SetCSVSource attaches (or replaces) ownerID's CSV data source. Resets
// next_index to 0 on replace so a new upload's round-robin starts from the
// top rather than continuing wherever the previous CSV's cursor was.
func (s *Store) SetCSVSource(ownerID, mode, content string) (*CSVSourceMeta, error) {
	if mode != "round_robin" && mode != "random" {
		return nil, ErrInvalidCSVMode
	}
	header, rows, err := parseCSVContent(content)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrEmptyCSV
	}
	_, err = s.db.Exec(
		`INSERT INTO dynamic_csv_sources (owner_id, mode, content, next_index, updated_at)
		 VALUES (?, ?, ?, 0, CURRENT_TIMESTAMP)
		 ON CONFLICT(owner_id) DO UPDATE SET
		   mode = excluded.mode, content = excluded.content, next_index = 0, updated_at = CURRENT_TIMESTAMP`,
		ownerID, mode, content,
	)
	if err != nil {
		return nil, fmt.Errorf("set csv source: %w", err)
	}
	return &CSVSourceMeta{Mode: mode, RowCount: len(rows), Columns: header}, nil
}

// GetCSVSourceMeta returns ErrNotFound if ownerID has no CSV attached.
func (s *Store) GetCSVSourceMeta(ownerID string) (*CSVSourceMeta, error) {
	var mode, content string
	err := s.db.QueryRow(`SELECT mode, content FROM dynamic_csv_sources WHERE owner_id = ?`, ownerID).Scan(&mode, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get csv source: %w", err)
	}
	header, rows, err := parseCSVContent(content)
	if err != nil {
		return nil, err
	}
	return &CSVSourceMeta{Mode: mode, RowCount: len(rows), Columns: header}, nil
}

// DeleteCSVSource returns ErrNotFound if ownerID had nothing attached.
func (s *Store) DeleteCSVSource(ownerID string) error {
	res, err := s.db.Exec(`DELETE FROM dynamic_csv_sources WHERE owner_id = ?`, ownerID)
	if err != nil {
		return fmt.Errorf("delete csv source: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// parseCSVContent splits raw CSV text into its header row and data rows.
// Empty content parses as zero rows (not an error) so SetCSVSource's own
// ErrEmptyCSV check is the one place that rejects it, with a clearer message
// than a generic parse failure would give.
func parseCSVContent(content string) (header []string, rows [][]string, err error) {
	if strings.TrimSpace(content) == "" {
		return nil, nil, nil
	}
	r := csv.NewReader(strings.NewReader(content))
	r.FieldsPerRecord = -1
	all, err := r.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("parse csv: %w", err)
	}
	if len(all) == 0 {
		return nil, nil, nil
	}
	return all[0], all[1:], nil
}

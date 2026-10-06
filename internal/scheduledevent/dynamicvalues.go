package scheduledevent

import (
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"math/rand"
	"strings"
)

// Counter and NextCSVRow give a scheduled event's own BodyTemplate access
// to counter()/csv() template functions (see internal/mock/template.go's
// funcMap and internal/mock.DynamicValueSource) scoped by the event's own
// ID — sharing the same dynamic_counters/dynamic_csv_sources tables a mock
// would use (owner_id is just whichever UUID), but implemented here rather
// than by importing internal/mock, so this package keeps its existing zero
// dependency on the mock package (Worker already passes its own *Store
// wherever a mock.DynamicValueSource is expected, satisfied structurally —
// see mock.RenderOptions's doc comment). Deliberately duplicates
// internal/mock/dynamicvalues.go's logic rather than sharing it: the
// alternative is a new shared package neither side already depends on, for
// what is otherwise about 40 lines.

// ErrEmptyCSV mirrors mock.ErrEmptyCSV for this package's own SetCSVSource.
var ErrEmptyCSV = errors.New("scheduledevent: csv must have at least one data row")

// ErrInvalidCSVMode mirrors mock.ErrInvalidCSVMode for this package's own SetCSVSource.
var ErrInvalidCSVMode = errors.New("scheduledevent: csv mode must be \"round_robin\" or \"random\"")

// Counter implements mock.DynamicValueSource structurally.
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

// NextCSVRow implements mock.DynamicValueSource structurally.
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

// CSVSourceMeta mirrors mock.CSVSourceMeta for this package's own API handler.
type CSVSourceMeta struct {
	Mode     string   `json:"mode"`
	RowCount int      `json:"rowCount"`
	Columns  []string `json:"columns"`
}

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

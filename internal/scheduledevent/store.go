package scheduledevent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("scheduledevent: not found")

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Create schedules e's first fire at now+IntervalSecs — a newly-created
// event doesn't fire immediately on creation, the same way a cron job's
// first tick is a full interval away, not instant.
func (s *Store) Create(e *Event) (*Event, error) {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.Method == "" {
		e.Method = "POST"
	}
	now := time.Now().UTC()
	e.CreatedAt, e.UpdatedAt = now, now
	e.NextFireAt = now.Add(time.Duration(e.IntervalSecs) * time.Second)

	headersJSON, err := json.Marshal(e.Headers)
	if err != nil {
		return nil, fmt.Errorf("marshal headers: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO scheduled_events (id, name, enabled, interval_secs, target_url, method, headers_json, body_template, next_fire_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Name, boolToInt(e.Enabled), e.IntervalSecs, e.TargetURL, e.Method, string(headersJSON), e.BodyTemplate,
		formatTime(e.NextFireAt), formatTime(e.CreatedAt), formatTime(e.UpdatedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("insert scheduled event: %w", err)
	}
	return e, nil
}

// Update overwrites e's config. Changing IntervalSecs restarts the
// countdown from now — an edit is treated the same as "reschedule this,"
// simplest to reason about rather than trying to preserve a partially
// elapsed interval across an edit.
func (s *Store) Update(e *Event) (*Event, error) {
	e.UpdatedAt = time.Now().UTC()
	e.NextFireAt = e.UpdatedAt.Add(time.Duration(e.IntervalSecs) * time.Second)

	headersJSON, err := json.Marshal(e.Headers)
	if err != nil {
		return nil, fmt.Errorf("marshal headers: %w", err)
	}
	res, err := s.db.Exec(
		`UPDATE scheduled_events SET name=?, enabled=?, interval_secs=?, target_url=?, method=?, headers_json=?, body_template=?, next_fire_at=?, updated_at=?
		 WHERE id=?`,
		e.Name, boolToInt(e.Enabled), e.IntervalSecs, e.TargetURL, e.Method, string(headersJSON), e.BodyTemplate,
		formatTime(e.NextFireAt), formatTime(e.UpdatedAt), e.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update scheduled event: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return e, nil
}

func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM scheduled_events WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete scheduled event: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	// Best-effort, same reasoning as mock.Store.Delete: owner_id has no FK/
	// CASCADE tying it to this row, so a deleted event's own counters/CSV
	// attachment (see dynamicvalues.go) would otherwise sit orphaned forever.
	s.db.Exec(`DELETE FROM dynamic_counters WHERE owner_id=?`, id)
	s.db.Exec(`DELETE FROM dynamic_csv_sources WHERE owner_id=?`, id)
	return nil
}

const selectColumns = `id, name, enabled, interval_secs, target_url, method, headers_json, body_template,
	last_fired_at, last_status, last_error, next_fire_at, created_at, updated_at`

func (s *Store) Get(id string) (*Event, error) {
	row := s.db.QueryRow(`SELECT `+selectColumns+` FROM scheduled_events WHERE id=?`, id)
	return scanEvent(row)
}

func (s *Store) List() ([]*Event, error) {
	rows, err := s.db.Query(`SELECT ` + selectColumns + ` FROM scheduled_events ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list scheduled events: %w", err)
	}
	defer rows.Close()

	out := []*Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ClaimDue finds up to limit enabled events whose NextFireAt has passed and
// immediately reschedules each one to now+IntervalSecs before returning them
// — the fire itself happens outside this call (a real HTTP request/email
// send shouldn't happen while holding the DB's one connection), so
// rescheduling has to happen up front rather than after a possibly-slow
// delivery, or a slow target would repeatedly get re-claimed on every poll
// tick in the meantime.
func (s *Store) ClaimDue(limit int) ([]*Event, error) {
	rows, err := s.db.Query(
		`SELECT `+selectColumns+` FROM scheduled_events WHERE enabled = 1 AND next_fire_at <= ? ORDER BY next_fire_at ASC LIMIT ?`,
		formatTime(time.Now().UTC()), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query due scheduled events: %w", err)
	}
	var due []*Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		due = append(due, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	now := time.Now().UTC()
	for _, e := range due {
		next := now.Add(time.Duration(e.IntervalSecs) * time.Second)
		if _, err := s.db.Exec(`UPDATE scheduled_events SET next_fire_at=? WHERE id=?`, formatTime(next), e.ID); err != nil {
			return nil, fmt.Errorf("reschedule %s: %w", e.ID, err)
		}
		e.NextFireAt = next
	}
	return due, nil
}

// RecordFireResult persists the outcome of the most recent delivery attempt
// — surfaced in the UI as "last fired / last status" so a misconfigured
// target (wrong URL, target down) is visible without digging through logs.
func (s *Store) RecordFireResult(id string, status int, fireErr error) error {
	errMsg := ""
	if fireErr != nil {
		errMsg = fireErr.Error()
	}
	_, err := s.db.Exec(
		`UPDATE scheduled_events SET last_fired_at=?, last_status=?, last_error=? WHERE id=?`,
		formatTime(time.Now().UTC()), status, errMsg, id,
	)
	return err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanEvent(row scanner) (*Event, error) {
	var e Event
	var enabledInt int
	var headersJSON string
	var lastFiredAt, createdAt, updatedAt, nextFireAt sql.NullString
	var lastStatus sql.NullInt64
	var lastError sql.NullString

	err := row.Scan(&e.ID, &e.Name, &enabledInt, &e.IntervalSecs, &e.TargetURL, &e.Method, &headersJSON, &e.BodyTemplate,
		&lastFiredAt, &lastStatus, &lastError, &nextFireAt, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan scheduled event: %w", err)
	}
	e.Enabled = enabledInt != 0
	e.LastStatus = int(lastStatus.Int64)
	e.LastError = lastError.String

	if headersJSON != "" && headersJSON != "null" {
		if err := json.Unmarshal([]byte(headersJSON), &e.Headers); err != nil {
			return nil, fmt.Errorf("unmarshal headers: %w", err)
		}
	}
	if lastFiredAt.Valid && lastFiredAt.String != "" {
		t, err := parseTime(lastFiredAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse last_fired_at: %w", err)
		}
		e.LastFiredAt = &t
	}
	if e.NextFireAt, err = parseTime(nextFireAt.String); err != nil {
		return nil, fmt.Errorf("parse next_fire_at: %w", err)
	}
	if e.CreatedAt, err = parseTime(createdAt.String); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	if e.UpdatedAt, err = parseTime(updatedAt.String); err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return &e, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func formatTime(t time.Time) string         { return t.Format(time.RFC3339Nano) }
func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

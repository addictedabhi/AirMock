package hitlog

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("hitlog: not found")

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Record persists one captured exchange. Fire-and-forget from the caller's
// perspective (mock-serving code logs an error but never fails the actual
// response over a logging problem) — enforced by callers, not this method.
func (s *Store) Record(e *Entry) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	reqHeaders, err := json.Marshal(e.RequestHeaders)
	if err != nil {
		return fmt.Errorf("marshal request headers: %w", err)
	}
	respHeaders, err := json.Marshal(e.ResponseHeaders)
	if err != nil {
		return fmt.Errorf("marshal response headers: %w", err)
	}

	_, err = s.db.Exec(
		`INSERT INTO hit_logs (id, mock_id, protocol_type, direction, method, path, target_url, request_headers_json, request_body, response_status, response_headers_json, response_body, latency_ms, created_at, collection_id, collection_name, request_name)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, nullableString(e.MockID), e.ProtocolType, e.Direction, e.Method, e.Path, e.TargetURL,
		string(reqHeaders), e.RequestBody, e.ResponseStatus, string(respHeaders), e.ResponseBody,
		e.LatencyMs, formatTime(e.CreatedAt), e.CollectionID, e.CollectionName, e.RequestName,
	)
	if err != nil {
		return fmt.Errorf("insert hit log: %w", err)
	}
	return nil
}

func (s *Store) Get(id string) (*Entry, error) {
	row := s.db.QueryRow(`SELECT `+selectHitLogColumns+` FROM hit_logs WHERE id=?`, id)
	return scanEntry(row)
}

// List returns the most recent entries, optionally filtered to one mock —
// a thin convenience wrapper over Query for the common case.
func (s *Store) List(mockID string, limit int) ([]*Entry, error) {
	return s.Query(QueryOptions{MockID: mockID, Limit: limit})
}

// QueryOptions filters a hit-log query: an empty MockID means "any mock"
// (including proxy-capture/outbound-client-call entries, which have none),
// and a zero Since/Until means "no bound on that side." ProtocolType,
// Direction, and Method are exact matches; StatusClass is one of
// "2xx"/"3xx"/"4xx"/"5xx" (matched by leading digit) or "none" (no response
// recorded at all, e.g. a timed-out or errored call); Search does a
// case-insensitive substring match across path/target URL/request body/
// response body. These all used to be applied only client-side over
// whatever page happened to already be loaded, so a filter could silently
// miss matches sitting beyond that page — pushing them into the query
// itself means every filter combination actually sees the whole table.
type QueryOptions struct {
	MockID       string
	Since        time.Time
	Until        time.Time
	ProtocolType string
	Direction    string
	Method       string
	StatusClass  string
	Search       string
	Limit        int
	Offset       int
}

const selectHitLogColumns = `id, mock_id, protocol_type, direction, method, path, target_url,
	request_headers_json, request_body, response_status, response_headers_json, response_body, latency_ms, created_at,
	collection_id, collection_name, request_name`

// buildFilterWhere renders QueryOptions' filter fields (everything except
// Limit/Offset, which only make sense for a paginated read) into a shared
// " WHERE ..." clause and its args — used by both Query (a page of matching
// rows) and DeleteWhere (every matching row, no page), so the two can never
// drift into matching different sets of rows for the same filter.
func buildFilterWhere(opts QueryOptions) (string, []any) {
	query := ` WHERE 1=1`
	var args []any
	if opts.MockID != "" {
		query += ` AND mock_id = ?`
		args = append(args, opts.MockID)
	}
	if !opts.Since.IsZero() {
		query += ` AND created_at >= ?`
		args = append(args, formatTime(opts.Since))
	}
	if !opts.Until.IsZero() {
		query += ` AND created_at <= ?`
		args = append(args, formatTime(opts.Until))
	}
	if opts.ProtocolType != "" {
		query += ` AND protocol_type = ?`
		args = append(args, opts.ProtocolType)
	}
	if opts.Direction != "" {
		query += ` AND direction = ?`
		args = append(args, opts.Direction)
	}
	if opts.Method != "" {
		query += ` AND method = ?`
		args = append(args, opts.Method)
	}
	if lo, hi, ok := statusClassRange(opts.StatusClass); ok {
		query += ` AND response_status BETWEEN ? AND ?`
		args = append(args, lo, hi)
	} else if opts.StatusClass == "none" {
		query += ` AND (response_status IS NULL OR response_status = 0)`
	}
	if opts.Search != "" {
		query += ` AND (path LIKE ? ESCAPE '\' OR target_url LIKE ? ESCAPE '\' OR request_body LIKE ? ESCAPE '\' OR response_body LIKE ? ESCAPE '\')`
		needle := "%" + likeEscape(opts.Search) + "%"
		args = append(args, needle, needle, needle, needle)
	}
	return query, args
}

// DeleteWhere removes every hit-log row matching opts' filter fields
// (Limit/Offset are ignored — this always deletes the full matching set,
// not just a page of it) and returns how many rows were removed. Backs the
// Log History page's "delete everything matching the current filters"
// action.
func (s *Store) DeleteWhere(opts QueryOptions) (int64, error) {
	where, args := buildFilterWhere(opts)
	res, err := s.db.Exec(`DELETE FROM hit_logs`+where, args...)
	if err != nil {
		return 0, fmt.Errorf("delete hit logs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete hit logs: %w", err)
	}
	return n, nil
}

// Delete removes a single hit-log entry by id.
func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM hit_logs WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete hit log: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete hit log: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Query is the filtered/paginated read path: mock_id, a created_at date
// range, and limit/offset — what a log viewer's filter bar needs.
func (s *Store) Query(opts QueryOptions) ([]*Entry, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}

	where, args := buildFilterWhere(opts)
	query := `SELECT ` + selectHitLogColumns + ` FROM hit_logs` + where + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, opts.Offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query hit logs: %w", err)
	}
	defer rows.Close()

	out := []*Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MockMetrics is one mock's aggregate hit-log stats, computed directly in
// SQL (GROUP BY) rather than loaded row-by-row into Go — the /metrics
// endpoint's whole point is a cheap-to-scrape summary, so it shouldn't cost
// more than the dashboard's own capped "most recent 500" sample does.
type MockMetrics struct {
	MockID       string
	ProtocolType string
	Total        int64
	Count2xx     int64
	Count3xx     int64
	Count4xx     int64
	Count5xx     int64
	LatencySumMs int64
}

// MetricsSnapshot aggregates every inbound mock hit (proxy-capture/outbound-
// client-call/callback rows are a different kind of traffic than "a mock
// answered a request," so they're excluded here the same way the Dashboard's
// per-protocol breakdown treats them as a separate direction) by mock, for
// the /metrics endpoint to render as Prometheus counters.
func (s *Store) MetricsSnapshot() ([]MockMetrics, error) {
	rows, err := s.db.Query(`
		SELECT mock_id, protocol_type, COUNT(*),
			SUM(CASE WHEN response_status BETWEEN 200 AND 299 THEN 1 ELSE 0 END),
			SUM(CASE WHEN response_status BETWEEN 300 AND 399 THEN 1 ELSE 0 END),
			SUM(CASE WHEN response_status BETWEEN 400 AND 499 THEN 1 ELSE 0 END),
			SUM(CASE WHEN response_status >= 500 THEN 1 ELSE 0 END),
			SUM(latency_ms)
		FROM hit_logs
		WHERE direction = ? AND mock_id IS NOT NULL
		GROUP BY mock_id, protocol_type`, DirectionInbound)
	if err != nil {
		return nil, fmt.Errorf("aggregate hit log metrics: %w", err)
	}
	defer rows.Close()

	out := []MockMetrics{}
	for rows.Next() {
		var m MockMetrics
		if err := rows.Scan(&m.MockID, &m.ProtocolType, &m.Total, &m.Count2xx, &m.Count3xx, &m.Count4xx, &m.Count5xx, &m.LatencySumMs); err != nil {
			return nil, fmt.Errorf("scan hit log metrics: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DirectionMetrics is MockMetrics' counterpart for traffic that isn't a
// normal inbound mock hit: Label is a mock_id for callback/proxy-capture
// rows (both are always tied to the mock that triggered them) but a
// scheduled event's own name for scheduled-event rows, since those have no
// mock_id at all (a scheduled event fires on its own timer, not in
// response to a request against a mock).
type DirectionMetrics struct {
	Label        string
	ProtocolType string
	Total        int64
	Count2xx     int64
	Count3xx     int64
	Count4xx     int64
	Count5xx     int64
	// CountNoResponse is a delivery that never got a response at all (DNS/
	// connect/timeout failure, or the request couldn't even be built) —
	// response_status is 0 in that case, distinct from every real HTTP
	// status class, mirroring how apiclient.StatusCounts.CountError works.
	CountNoResponse int64
	LatencySumMs    int64
}

// CallbackMetricsSnapshot aggregates async-mock callback deliveries
// (hitlog.DirectionCallback rows, recorded by mock.CallbackWorker) by the
// mock that scheduled them, for the /metrics endpoint — previously
// entirely invisible there despite DirectionCallback existing since phase
// 1.12, because nothing recorded callback deliveries into hit_logs at all.
func (s *Store) CallbackMetricsSnapshot() ([]DirectionMetrics, error) {
	return s.directionMetricsByMockID(DirectionCallback)
}

// ProxyCaptureMetricsSnapshot aggregates proxy-mode mocks' captured
// upstream calls (hitlog.DirectionProxyCapture rows) by mock, for the
// /metrics endpoint — these were already recorded in hit_logs (the History/
// Dashboard views already show them), just never surfaced as Prometheus
// counters.
func (s *Store) ProxyCaptureMetricsSnapshot() ([]DirectionMetrics, error) {
	return s.directionMetricsByMockID(DirectionProxyCapture)
}

func (s *Store) directionMetricsByMockID(direction string) ([]DirectionMetrics, error) {
	rows, err := s.db.Query(`
		SELECT mock_id, protocol_type, COUNT(*),
			SUM(CASE WHEN response_status BETWEEN 200 AND 299 THEN 1 ELSE 0 END),
			SUM(CASE WHEN response_status BETWEEN 300 AND 399 THEN 1 ELSE 0 END),
			SUM(CASE WHEN response_status BETWEEN 400 AND 499 THEN 1 ELSE 0 END),
			SUM(CASE WHEN response_status >= 500 THEN 1 ELSE 0 END),
			SUM(CASE WHEN response_status = 0 THEN 1 ELSE 0 END),
			SUM(latency_ms)
		FROM hit_logs
		WHERE direction = ? AND mock_id IS NOT NULL
		GROUP BY mock_id, protocol_type`, direction)
	if err != nil {
		return nil, fmt.Errorf("aggregate %s metrics: %w", direction, err)
	}
	return scanDirectionMetrics(rows)
}

// ScheduledEventMetricsSnapshot aggregates scheduled-event fires
// (hitlog.DirectionScheduledEvent rows) by the event's own stable ID —
// scheduledevent.Worker.Fire sets MockID to the event's ID (not a mock's;
// reused for the same reason DirectionCallback/DirectionProxyCapture use
// it) rather than grouping by Name/Path, which is a mutable display value:
// renaming an event used to fragment its Prometheus series into a brand
// new label combination, with the old name's rows still counted by every
// scrape until retention aged them out.
func (s *Store) ScheduledEventMetricsSnapshot() ([]DirectionMetrics, error) {
	return s.directionMetricsByMockID(DirectionScheduledEvent)
}

func scanDirectionMetrics(rows *sql.Rows) ([]DirectionMetrics, error) {
	defer rows.Close()
	out := []DirectionMetrics{}
	for rows.Next() {
		var m DirectionMetrics
		if err := rows.Scan(&m.Label, &m.ProtocolType, &m.Total, &m.Count2xx, &m.Count3xx, &m.Count4xx, &m.Count5xx, &m.CountNoResponse, &m.LatencySumMs); err != nil {
			return nil, fmt.Errorf("scan direction metrics: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanEntry(row scanner) (*Entry, error) {
	var e Entry
	var mockID sql.NullString
	var reqHeaders, respHeaders, createdAt string

	err := row.Scan(&e.ID, &mockID, &e.ProtocolType, &e.Direction, &e.Method, &e.Path, &e.TargetURL,
		&reqHeaders, &e.RequestBody, &e.ResponseStatus, &respHeaders, &e.ResponseBody, &e.LatencyMs, &createdAt,
		&e.CollectionID, &e.CollectionName, &e.RequestName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan hit log: %w", err)
	}
	e.MockID = mockID.String
	if err := json.Unmarshal([]byte(reqHeaders), &e.RequestHeaders); err != nil {
		return nil, fmt.Errorf("unmarshal request headers: %w", err)
	}
	if err := json.Unmarshal([]byte(respHeaders), &e.ResponseHeaders); err != nil {
		return nil, fmt.Errorf("unmarshal response headers: %w", err)
	}
	if e.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	return &e, nil
}

// statusClassRange translates "2xx"/"3xx"/"4xx"/"5xx" into the inclusive
// response_status range it covers; ok is false for "" or "none" (the
// latter is handled separately since it isn't a range at all).
func statusClassRange(class string) (lo, hi int, ok bool) {
	switch class {
	case "2xx":
		return 200, 299, true
	case "3xx":
		return 300, 399, true
	case "4xx":
		return 400, 499, true
	case "5xx":
		return 500, 599, true
	default:
		return 0, 0, false
	}
}

// likeEscape escapes SQL LIKE's own wildcard characters in free-text user
// input so a search for a literal "%" or "_" (e.g. part of a path) doesn't
// get interpreted as a pattern.
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Same modernc.org/sqlite timestamp pitfall documented in every other
// store in this project: TEXT columns don't auto-scan into time.Time.
func formatTime(t time.Time) string         { return t.Format(time.RFC3339Nano) }
func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

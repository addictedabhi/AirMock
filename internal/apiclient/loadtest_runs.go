package apiclient

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// LoadTestRun is one persisted load-test run: its metadata plus the full
// aggregate result (with Samples attached — see GetLoadTestRun, which is
// the only accessor that populates them; ListLoadTestRuns returns the
// lighter LoadTestRunSummary instead).
type LoadTestRun struct {
	ID string `json:"id"`
	// ItemID/CollectionID identify which saved request this run was
	// against — both '' for an unsaved/draft tab, which still gets
	// persisted (fetchable by ID) but never appears in any item's history
	// list (see ListLoadTestRuns).
	ItemID       string         `json:"itemId,omitempty"`
	CollectionID string         `json:"collectionId,omitempty"`
	Method       string         `json:"method"`
	URL          string         `json:"url"`
	Config       LoadTestConfig `json:"config"`
	Result       *LoadTestResult `json:"result"`
	CreatedAt    time.Time      `json:"createdAt"`
}

// LoadTestRunSummary is what a history LIST needs — enough for one row
// (timestamp, req/s, p95, error rate) without paying for every sample.
type LoadTestRunSummary struct {
	ID             string    `json:"id"`
	ItemID         string    `json:"itemId,omitempty"`
	Method         string    `json:"method"`
	URL            string    `json:"url"`
	CreatedAt      time.Time `json:"createdAt"`
	TotalRequests  int       `json:"totalRequests"`
	RequestsPerSec float64   `json:"requestsPerSec"`
	P95Ms          int64     `json:"p95Ms"`
	// ErrorRate is the percentage (0-100) of requests that were a 4xx, 5xx,
	// or network error.
	ErrorRate float64 `json:"errorRate"`
}

func errorRatePercent(s StatusCounts) float64 {
	total := s.Count2xx + s.Count3xx + s.Count4xx + s.Count5xx + s.CountError
	if total == 0 {
		return 0
	}
	return float64(s.Count4xx+s.Count5xx+s.CountError) / float64(total) * 100
}

// SaveLoadTestRun persists run, assigning an ID/CreatedAt if not already
// set. Samples are stored in their own table (load_test_run_samples), so
// result_json never duplicates them — this method strips Samples from the
// JSON it writes for the aggregate row, independent of whatever run.Result
// itself still holds in memory.
func (s *Store) SaveLoadTestRun(run *LoadTestRun) error {
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	// Set in place, before marshaling below, so (a) the caller's shared
	// *LoadTestResult already carries it the moment this call returns — no
	// second round-trip needed for a just-ran live result's download link —
	// and (b) it's baked into result_json, so GetLoadTestRun's later
	// unmarshal restores it with no extra code.
	run.Result.RunID = run.ID
	configJSON, err := json.Marshal(run.Config)
	if err != nil {
		return fmt.Errorf("marshal load test config: %w", err)
	}
	resultForJSON := *run.Result
	resultForJSON.Samples = nil
	resultJSON, err := json.Marshal(resultForJSON)
	if err != nil {
		return fmt.Errorf("marshal load test result: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO load_test_runs (id, item_id, collection_id, method, url, config_json, result_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.ItemID, run.CollectionID, run.Method, run.URL, string(configJSON), string(resultJSON), formatTime(run.CreatedAt),
	); err != nil {
		return fmt.Errorf("insert load test run: %w", err)
	}
	for _, sample := range run.Result.Samples {
		if _, err := tx.Exec(
			`INSERT INTO load_test_run_samples (run_id, idx, elapsed_ms, latency_ms, status_code, error) VALUES (?, ?, ?, ?, ?, ?)`,
			run.ID, sample.Index, sample.ElapsedMs, sample.LatencyMs, sample.StatusCode, sample.Error,
		); err != nil {
			return fmt.Errorf("insert load test run sample: %w", err)
		}
	}
	return tx.Commit()
}

// ListLoadTestRuns returns summaries for every run against itemID, newest
// first.
func (s *Store) ListLoadTestRuns(itemID string) ([]*LoadTestRunSummary, error) {
	rows, err := s.db.Query(
		`SELECT id, item_id, method, url, result_json, created_at FROM load_test_runs WHERE item_id = ? ORDER BY created_at DESC`,
		itemID,
	)
	if err != nil {
		return nil, fmt.Errorf("list load test runs: %w", err)
	}
	defer rows.Close()

	out := []*LoadTestRunSummary{}
	for rows.Next() {
		var id, iid, method, url, resultJSON, createdAt string
		if err := rows.Scan(&id, &iid, &method, &url, &resultJSON, &createdAt); err != nil {
			return nil, fmt.Errorf("scan load test run: %w", err)
		}
		var result LoadTestResult
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			return nil, fmt.Errorf("unmarshal load test result: %w", err)
		}
		created, err := parseTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		out = append(out, &LoadTestRunSummary{
			ID: id, ItemID: iid, Method: method, URL: url, CreatedAt: created,
			TotalRequests: result.TotalRequests, RequestsPerSec: result.RequestsPerSec, P95Ms: result.P95Ms,
			ErrorRate: errorRatePercent(result.Statuses),
		})
	}
	return out, rows.Err()
}

// GetLoadTestRun returns ErrNotFound if id doesn't exist.
func (s *Store) GetLoadTestRun(id string) (*LoadTestRun, error) {
	row := s.db.QueryRow(
		`SELECT id, item_id, collection_id, method, url, config_json, result_json, created_at FROM load_test_runs WHERE id = ?`,
		id,
	)
	var run LoadTestRun
	var configJSON, resultJSON, createdAt string
	if err := row.Scan(&run.ID, &run.ItemID, &run.CollectionID, &run.Method, &run.URL, &configJSON, &resultJSON, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan load test run: %w", err)
	}
	if err := json.Unmarshal([]byte(configJSON), &run.Config); err != nil {
		return nil, fmt.Errorf("unmarshal load test config: %w", err)
	}
	var result LoadTestResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return nil, fmt.Errorf("unmarshal load test result: %w", err)
	}
	// Response bodies/headers are never persisted to history by design (only
	// the lightweight latency/status samples below are) — but result_json
	// can still carry Detailed: true from the original run. Force it false
	// here so the frontend's samples table (gated on .detailed) correctly
	// stays hidden for a historical view instead of rendering a misleading
	// "(empty)" body for every row; the three charts only need the
	// lightweight fields, which are always present, so they're unaffected.
	result.Detailed = false
	var err error
	if run.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}

	sampleRows, err := s.db.Query(
		`SELECT idx, elapsed_ms, latency_ms, status_code, error FROM load_test_run_samples WHERE run_id = ? ORDER BY idx ASC`,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("list load test run samples: %w", err)
	}
	defer sampleRows.Close()
	var samples []LoadTestSample
	for sampleRows.Next() {
		var smp LoadTestSample
		if err := sampleRows.Scan(&smp.Index, &smp.ElapsedMs, &smp.LatencyMs, &smp.StatusCode, &smp.Error); err != nil {
			return nil, fmt.Errorf("scan load test run sample: %w", err)
		}
		samples = append(samples, smp)
	}
	if err := sampleRows.Err(); err != nil {
		return nil, err
	}
	result.Samples = samples
	run.Result = &result
	return &run, nil
}

// DeleteLoadTestRun returns ErrNotFound if id doesn't exist.
func (s *Store) DeleteLoadTestRun(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM load_test_run_samples WHERE run_id = ?`, id); err != nil {
		return fmt.Errorf("delete load test run samples: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM load_test_runs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete load test run: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// EnforceLoadTestRunRetention deletes every run older than maxAgeDays,
// then trims each remaining item_id down to its most recent
// maxRowsPerItem runs — mirrors internal/hitlog.Store.DeleteOlderThan +
// EnforceMaxRowsPerMock's exact two-part shape. A '' item_id (a draft-tab
// run) is excluded from the per-item trim — there's no meaningful "most
// recent N" grouping for runs that don't share a real item, so only the
// age cutoff above ever reaps those.
func (s *Store) EnforceLoadTestRunRetention(maxAgeDays, maxRowsPerItem int) error {
	cutoff := formatTime(time.Now().UTC().Add(-time.Duration(maxAgeDays) * 24 * time.Hour))
	if _, err := s.db.Exec(
		`DELETE FROM load_test_run_samples WHERE run_id IN (SELECT id FROM load_test_runs WHERE created_at < ?)`,
		cutoff,
	); err != nil {
		return fmt.Errorf("delete old load test run samples: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM load_test_runs WHERE created_at < ?`, cutoff); err != nil {
		return fmt.Errorf("delete old load test runs: %w", err)
	}

	rows, err := s.db.Query(`SELECT DISTINCT item_id FROM load_test_runs WHERE item_id != ''`)
	if err != nil {
		return fmt.Errorf("list load test run item ids: %w", err)
	}
	var itemIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan item id: %w", err)
		}
		itemIDs = append(itemIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, itemID := range itemIDs {
		if _, err := s.db.Exec(
			`DELETE FROM load_test_run_samples WHERE run_id IN (
			   SELECT id FROM load_test_runs WHERE item_id = ? AND id NOT IN (
			     SELECT id FROM load_test_runs WHERE item_id = ? ORDER BY created_at DESC LIMIT ?
			   )
			 )`,
			itemID, itemID, maxRowsPerItem,
		); err != nil {
			return fmt.Errorf("enforce max rows for item %s (samples): %w", itemID, err)
		}
		if _, err := s.db.Exec(
			`DELETE FROM load_test_runs WHERE item_id = ? AND id NOT IN (
			   SELECT id FROM load_test_runs WHERE item_id = ? ORDER BY created_at DESC LIMIT ?
			 )`,
			itemID, itemID, maxRowsPerItem,
		); err != nil {
			return fmt.Errorf("enforce max rows for item %s: %w", itemID, err)
		}
	}

	// The per-item trim above runs two separate, non-transactional
	// statements per item (delete samples for runs outside the top N, then
	// delete those runs). If a SaveLoadTestRun lands between them for that
	// same item, the "top N" set can shift between the two subqueries,
	// potentially leaving sample rows whose parent run got deleted without
	// them. Sweep any such orphans now that the whole loop has settled.
	if _, err := s.db.Exec(`DELETE FROM load_test_run_samples WHERE run_id NOT IN (SELECT id FROM load_test_runs)`); err != nil {
		return fmt.Errorf("sweep orphaned load test run samples: %w", err)
	}
	return nil
}

package mock

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CallbackJob is a durable record of one async mock's pending/attempted
// callback delivery — persisted (not a bare goroutine) so delivery survives
// a process restart; CallbackWorker resumes any still-pending jobs on boot.
type CallbackJob struct {
	ID     string
	MockID string
	// TargetURL is a URL for Channel=="http" or an email address for
	// Channel=="email" — same field, resolved the same "fixed or
	// extracted" way either way, at schedule time in both cases.
	TargetURL     string
	Method        string
	Headers       map[string]string
	Payload       string
	Channel       string // "http" (default) | "email"
	Subject       string // email channel only
	Status        string // pending | claimed | succeeded | failed
	AttemptCount  int
	MaxAttempts   int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CallbackRequest is what serveRest computes at request time (target
// already resolved, body already templated) and hands off to be scheduled.
type CallbackRequest struct {
	TargetURL   string
	Method      string
	Headers     map[string]string
	Payload     string
	DelayMs     int
	MaxAttempts int
	Channel     string // "http" (default) | "email"
	Subject     string // email channel only
}

// retryBackoff mirrors the plan's "immediate, +5s, +30s" schedule: the
// first attempt fires after the configured initial delay, and each
// subsequent retry backs off further.
var retryBackoff = []time.Duration{5 * time.Second, 30 * time.Second}

// ResolveCallbackTarget resolves an AsyncConfig's target (a URL for the
// http channel, an email address for the email channel — same field,
// same "fixed or extracted from the request" mechanism either way) —
// shared by every protocol engine that can trigger an async callback
// (REST/SOAP/GraphQL via httpengine, and TCP interactions) rather than
// each maintaining its own copy of this same two-line branch.
func ResolveCallbackTarget(cfg *AsyncConfig, reqCtx RequestContext) (string, error) {
	if cfg.CallbackTargetMode == "extracted" {
		return ExtractCallbackURL(cfg.CallbackExtractPath, reqCtx)
	}
	return cfg.CallbackFixedURL, nil
}

// CallbackScheduler is implemented by *Store — declared here (rather than
// once per engine package) since every protocol engine that can trigger an
// async callback (HTTP-family via httpengine, TCP interactions) needs the
// exact same "persist a job" shape.
type CallbackScheduler interface {
	ScheduleCallback(mockID string, req CallbackRequest) error
}

// EmailTemplateResolver looks up a saved internal/smtp.Template by id, for
// an async callback whose channel is "email" and references one instead of
// inlining its own subject/body — shared for the same reason as
// CallbackScheduler above.
type EmailTemplateResolver interface {
	GetEmailTemplate(id string) (subject, htmlBody string, err error)
}

// ResolveEmailContent applies AsyncConfig's EmailTemplateID indirection (if
// set and resolvable) before rendering: the subject/body templates actually
// executed are either the saved template's, or the config's own inline
// ones — same fallback-on-lookup-failure behavior needed by every engine
// that can schedule an email-channel callback.
func ResolveEmailContent(cfg *AsyncConfig, resolver EmailTemplateResolver) (subjectTemplate, bodyTemplate string, usedTemplate bool, lookupErr error) {
	subjectTemplate, bodyTemplate = cfg.EmailSubjectTemplate, cfg.CallbackBodyTemplate
	if cfg.EmailTemplateID == "" || resolver == nil {
		return subjectTemplate, bodyTemplate, false, nil
	}
	subject, htmlBody, err := resolver.GetEmailTemplate(cfg.EmailTemplateID)
	if err != nil {
		return subjectTemplate, bodyTemplate, false, err
	}
	return subject, htmlBody, true, nil
}

// DefaultCallbackBody is what an HTTP callback carries when the mock has no
// callbackBodyTemplate at all — the same body the UI pre-fills — instead of
// an empty POST with Content-Length: 0.
const DefaultCallbackBody = `{"status":"done"}`

// DefaultCallbackPayload returns payload unchanged unless this is an HTTP
// callback whose method carries a body (POST/PUT/PATCH) and the mock defines
// no body template (bodyTemplate is the one actually used, after any email
// template indirection). A template that is set but renders to nothing is
// left alone: that is an explicit choice.
func DefaultCallbackPayload(cfg *AsyncConfig, bodyTemplate, payload string) string {
	if cfg == nil || strings.TrimSpace(bodyTemplate) != "" || cfg.CallbackChannel == "email" {
		return payload
	}
	switch strings.ToUpper(cfg.CallbackMethod) {
	case "", "POST", "PUT", "PATCH":
		return DefaultCallbackBody
	}
	return payload
}

// ScheduleCallback persists a new pending callback job.
func (s *Store) ScheduleCallback(mockID string, req CallbackRequest) error {
	maxAttempts := req.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	channel := req.Channel
	if channel == "" {
		channel = "http"
	}
	headersJSON, err := json.Marshal(req.Headers)
	if err != nil {
		return fmt.Errorf("marshal callback headers: %w", err)
	}
	now := time.Now().UTC()
	nextAttempt := now.Add(time.Duration(req.DelayMs) * time.Millisecond)

	_, err = s.db.Exec(
		`INSERT INTO callback_jobs (id, mock_id, target_url, method, headers_json, payload, channel, subject, status, attempt_count, max_attempts, next_attempt_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', 0, ?, ?, ?, ?)`,
		uuid.NewString(), mockID, req.TargetURL, req.Method, string(headersJSON), req.Payload, channel, req.Subject, maxAttempts, formatTime(nextAttempt), formatTime(now), formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("schedule callback: %w", err)
	}
	return nil
}

// claimLeaseDuration bounds how long a claimed job is allowed to sit
// mid-delivery before it's treated as abandoned (crashed process, hung
// network call) and becomes reclaimable again — generous relative to the
// 10s HTTP client timeout and a realistic SMTP handshake, while still
// keeping a genuinely-stuck job from being stuck forever.
const claimLeaseDuration = 60 * time.Second

// ClaimDueCallbackJobs atomically claims up to limit due jobs (transitions
// them out of 'pending' as part of the same query that finds them) and
// returns them, oldest-due-first.
//
// This used to be a bare SELECT with no claiming step: a job stayed
// 'pending' in the database for exactly as long as its delivery attempt
// took, so if that took longer than the worker's poll interval (500ms) —
// completely ordinary for an SMTP handshake, and not even unusual for a
// slow HTTP webhook — the very next poll tick selected the SAME row again
// and dispatched a second delivery goroutine before the first had even
// finished. One triggering request could fan out into several duplicate
// emails/webhook calls this way, worse the slower the target was.
// Claiming (marking status='claimed' with a leased next_attempt_at) closes
// that window: a job invisible to the 'pending' branch can't be
// re-selected, while a 'claimed' job whose lease has already expired
// (the delivery attempt never got to record success/failure — most likely
// because the process crashed mid-flight) becomes reclaimable again, so
// the existing "delivery survives a restart" guarantee still holds.
func (s *Store) ClaimDueCallbackJobs(limit int) ([]*CallbackJob, error) {
	now := time.Now().UTC()
	lease := now.Add(claimLeaseDuration)

	res, err := s.db.Exec(
		`UPDATE callback_jobs
		 SET status='claimed', next_attempt_at=?, updated_at=?
		 WHERE (status='pending' OR status='claimed') AND next_attempt_at <= ?`,
		formatTime(lease), formatTime(now), formatTime(now),
	)
	if err != nil {
		return nil, fmt.Errorf("lease due callback jobs: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, fmt.Errorf("lease due callback jobs: %w", err)
	} else if n == 0 {
		return nil, nil
	}

	// Matching on the exact lease timestamp (rather than status='claimed'
	// alone) picks out precisely the batch the UPDATE above just claimed —
	// not any other row already 'claimed' under an earlier, still-active
	// lease from a previous tick.
	rows, err := s.db.Query(
		`SELECT id, mock_id, target_url, method, headers_json, payload, channel, subject, status, attempt_count, max_attempts, next_attempt_at, last_error, created_at, updated_at
		 FROM callback_jobs
		 WHERE status = 'claimed' AND next_attempt_at = ?
		 ORDER BY created_at ASC
		 LIMIT ?`,
		formatTime(lease), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("claim due callback jobs: %w", err)
	}
	defer rows.Close()

	out := []*CallbackJob{}
	for rows.Next() {
		j, err := scanCallbackJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ListCallbackJobsForMock returns every callback job for a mock regardless
// of status, oldest first — used by admin/debug views and tests that need
// to observe attempt_count/status rather than just "is it still pending."
func (s *Store) ListCallbackJobsForMock(mockID string) ([]*CallbackJob, error) {
	rows, err := s.db.Query(
		`SELECT id, mock_id, target_url, method, headers_json, payload, channel, subject, status, attempt_count, max_attempts, next_attempt_at, last_error, created_at, updated_at
		 FROM callback_jobs WHERE mock_id = ? ORDER BY created_at ASC`, mockID)
	if err != nil {
		return nil, fmt.Errorf("list callback jobs: %w", err)
	}
	defer rows.Close()

	out := []*CallbackJob{}
	for rows.Next() {
		j, err := scanCallbackJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// CallbackJobStatusCounts groups every callback_jobs row by its current
// status (pending/claimed/succeeded/failed) — a live gauge of the queue's
// health (e.g. a growing "failed" or "pending" count signals callbacks are
// backing up or a target is down) that the historical per-mock hit-log
// metrics alone can't show, since those only cover deliveries that were
// actually attempted, not jobs still waiting or stuck retrying.
func (s *Store) CallbackJobStatusCounts() (map[string]int64, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM callback_jobs GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("count callback jobs by status: %w", err)
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan callback job status count: %w", err)
		}
		out[status] = count
	}
	return out, rows.Err()
}

func (s *Store) MarkCallbackSucceeded(id string) error {
	_, err := s.db.Exec(
		`UPDATE callback_jobs SET status='succeeded', attempt_count=attempt_count+1, last_error=NULL, updated_at=? WHERE id=?`,
		formatTime(time.Now().UTC()), id,
	)
	return err
}

// MarkCallbackAttemptFailed records a failed attempt. If the job has more
// attempts left it's rescheduled with backoff; otherwise it's marked failed.
func (s *Store) MarkCallbackAttemptFailed(j *CallbackJob, callErr error) error {
	attempt := j.AttemptCount + 1
	now := time.Now().UTC()

	if attempt >= j.MaxAttempts {
		_, err := s.db.Exec(
			`UPDATE callback_jobs SET status='failed', attempt_count=?, last_error=?, updated_at=? WHERE id=?`,
			attempt, callErr.Error(), formatTime(now), j.ID,
		)
		return err
	}

	backoff := retryBackoff[len(retryBackoff)-1]
	if attempt-1 < len(retryBackoff) {
		backoff = retryBackoff[attempt-1]
	}
	// Explicitly back to 'pending' — the row is currently 'claimed' (see
	// ClaimDueCallbackJobs) for the duration of the attempt that just
	// failed, and needs to leave that status for the retry to be
	// reclaimable once its backoff elapses.
	_, err := s.db.Exec(
		`UPDATE callback_jobs SET status='pending', attempt_count=?, last_error=?, next_attempt_at=?, updated_at=? WHERE id=?`,
		attempt, callErr.Error(), formatTime(now.Add(backoff)), formatTime(now), j.ID,
	)
	return err
}

func scanCallbackJob(row scanner) (*CallbackJob, error) {
	var j CallbackJob
	var headersJSON, nextAttemptAt, createdAt, updatedAt string
	var lastError *string

	if err := row.Scan(&j.ID, &j.MockID, &j.TargetURL, &j.Method, &headersJSON, &j.Payload, &j.Channel, &j.Subject, &j.Status, &j.AttemptCount, &j.MaxAttempts, &nextAttemptAt, &lastError, &createdAt, &updatedAt); err != nil {
		return nil, fmt.Errorf("scan callback job: %w", err)
	}
	if err := json.Unmarshal([]byte(headersJSON), &j.Headers); err != nil {
		return nil, fmt.Errorf("unmarshal callback headers: %w", err)
	}
	if lastError != nil {
		j.LastError = *lastError
	}

	var err error
	if j.NextAttemptAt, err = parseTime(nextAttemptAt); err != nil {
		return nil, fmt.Errorf("parse next_attempt_at: %w", err)
	}
	if j.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	if j.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return &j, nil
}

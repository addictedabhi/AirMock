// Package settings persists the small set of instance-wide (not per-mock)
// configuration knobs AirMock exposes — currently just the hit log
// retention policy. Modeled on internal/certs's GatewaySettings: a single
// row keyed 'default', since these are instance-level settings rather than
// something scoped to any one mock or project.
package settings

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Default retention policy, applied when no settings row exists yet
// (matches the values internal/hitlog/retention.go previously hardcoded).
const (
	DefaultHitLogMaxAgeDays     = 30
	DefaultHitLogMaxRowsPerMock = 10_000
	// DefaultMaxVersionsPerMock matches internal/mock/versions.go's own
	// previously-hardcoded constant, so an upgrade from an older AirMock
	// version keeps exactly the same version-history depth by default.
	DefaultMaxVersionsPerMock = 20
	// DefaultMaxCapturedBodyBytes matches internal/engine/http/capture.go's
	// own previously-hardcoded bodyCaptureLimit, so an upgrade from an
	// older AirMock version keeps logging the same amount of response body
	// by default.
	DefaultMaxCapturedBodyBytes = 64 << 10
	// DefaultSessionTimeoutMinutes matches internal/auth.Manager's own
	// previously-hardcoded defaultSessionTimeout, so an upgrade from an
	// older AirMock version (or a fresh instance that never touches this
	// setting) keeps the same session lifetime by default. Minutes, not
	// hours, so a short (e.g. 5-minute) timeout is expressible — the
	// column was originally hours-only; 000042_settings_session_timeout_
	// minutes backfills existing values (hours*60) into the new column.
	DefaultSessionTimeoutMinutes = 24 * 60
	// DefaultLoadTestRunMaxAgeDays/DefaultLoadTestRunMaxRowsPerItem mirror
	// DefaultHitLogMaxAgeDays/DefaultHitLogMaxRowsPerMock's own shape,
	// scoped to load-test run history instead of hit-log rows.
	DefaultLoadTestRunMaxAgeDays     = 30
	DefaultLoadTestRunMaxRowsPerItem = 50
)

// DefaultRedactedHeaders is applied when no settings row exists yet —
// matches the list internal/engine/http/capture.go previously hardcoded,
// so an upgrade from an older AirMock version doesn't silently start
// logging secrets it used to redact.
var DefaultRedactedHeaders = []string{"Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "X-Auth-Token"}

// Settings is the full set of instance-wide settings. New fields should be
// added here as more become configurable, following the same
// Get/Save-a-single-row pattern.
type Settings struct {
	HitLogMaxAgeDays     int
	HitLogMaxRowsPerMock int
	// MaxVersionsPerMock bounds how many past versions internal/mock keeps
	// per mock (see internal/mock/versions.go's snapshotVersion) — older
	// edits beyond this many are pruned since "undo my last change" is the
	// feature's actual purpose, not an unbounded audit trail.
	MaxVersionsPerMock int
	// RedactedHeaders lists header names whose values are shown as
	// "***REDACTED***" in the hit log instead of their real value —
	// case-insensitive, matched against both request and response headers
	// on REST/SOAP/GraphQL/WS mocks.
	RedactedHeaders []string
	// DefaultResponseDelayMs/DefaultFailureRatePercent pre-fill a brand-new
	// REST/SOAP mock's own response delay and fault-injection error rate
	// (see mock.ResponseTemplate.DelayMs / mock.FaultConfig.ErrorRatePercent)
	// in the "+ New mock" form — a starting point every new mock inherits,
	// not a value enforced on existing ones (each mock's own fields, once
	// created, are independent and can be changed/removed same as always).
	DefaultResponseDelayMs    int
	DefaultFailureRatePercent float64
	// MaxCapturedBodyBytes bounds how much of a REST/SOAP/GraphQL mock's
	// response body internal/engine/http/capture.go buffers for the hit
	// log (see its own bodyCaptureLimit doc comment) — unrelated to the
	// separate, hardcoded cap on how much of the REQUEST body is read for
	// matching/templating, which affects mock behavior itself and isn't
	// something a logging-verbosity setting should touch.
	MaxCapturedBodyBytes int
	// SessionTimeoutMinutes controls how long an admin login (see
	// internal/auth.Manager) stays valid — a rolling timeout, extended on
	// every authenticated request (any backend API call), not a fixed
	// expiry from login. Only meaningful when an admin password is
	// actually configured (see cmd/airmock/main.go's --admin-password);
	// otherwise there's no login to time out at all.
	SessionTimeoutMinutes int
	// InactivityLockMinutes drives a SEPARATE mechanism from the above: the
	// frontend's inactivity watcher (ui/src/lib/inactivityWatcher.js) logs
	// out for real after this many minutes of no actual mouse/keyboard/
	// scroll/touch activity, regardless of whether background API calls
	// have kept the session timeout above from expiring. 0 means off — a
	// real, meaningful "no auto-lock" value (unlike SessionTimeoutMinutes,
	// this is a purely additive opt-in feature, so a never-configured
	// instance correctly defaults to disabled rather than falling back to
	// some non-zero default).
	InactivityLockMinutes int
	// LoadTestRunMaxAgeDays/LoadTestRunMaxRowsPerItem bound how much
	// load-test run history (internal/apiclient.LoadTestRun) is kept per
	// item before internal/apiclient.LoadTestRunRetentionWorker purges it —
	// same two-knob shape as the hit-log retention fields above, scoped to
	// a different table.
	LoadTestRunMaxAgeDays     int
	LoadTestRunMaxRowsPerItem int
	// CORS configures the Cross-Origin Resource Sharing headers the mock
	// gateway adds (see internal/engine/http/cors.go). On by default and
	// permissive, like most mock servers, so browser front ends can call
	// the mocks without extra setup.
	CORS      CORS
	UpdatedAt time.Time
}

// CORS mirrors httpengine.CORSConfig for storage.
type CORS struct {
	Enabled          bool
	AllowOrigin      string
	AllowMethods     string
	AllowHeaders     string
	AllowCredentials bool
	MaxAgeSecs       int
}

// DefaultCORS matches httpengine.DefaultCORS.
func DefaultCORS() CORS {
	return CORS{Enabled: true, AllowOrigin: "*", AllowMethods: "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS", AllowHeaders: "*", MaxAgeSecs: 600}
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Get() (*Settings, error) {
	row := s.db.QueryRow(
		`SELECT hit_log_max_age_days, hit_log_max_rows_per_mock, max_versions_per_mock, redacted_headers_json,
		        default_response_delay_ms, default_failure_rate_percent, max_captured_body_bytes, session_timeout_minutes,
		        inactivity_lock_minutes, load_test_run_max_age_days, load_test_run_max_rows_per_item,
		        cors_enabled, cors_allow_origin, cors_allow_methods, cors_allow_headers, cors_allow_credentials, cors_max_age_secs, updated_at
		 FROM app_settings WHERE id='default'`)

	var maxAgeDays, maxRows, maxVersions, defaultDelayMs, maxCapturedBodyBytes, sessionTimeoutMinutes, inactivityLockMinutes int
	var loadTestRunMaxAgeDays, loadTestRunMaxRowsPerItem int
	var defaultFailureRate float64
	var redactedHeadersJSON sql.NullString
	var cors CORS
	var corsEnabled, corsCreds int
	var updatedAt string
	err := row.Scan(&maxAgeDays, &maxRows, &maxVersions, &redactedHeadersJSON,
		&defaultDelayMs, &defaultFailureRate, &maxCapturedBodyBytes, &sessionTimeoutMinutes, &inactivityLockMinutes,
		&loadTestRunMaxAgeDays, &loadTestRunMaxRowsPerItem,
		&corsEnabled, &cors.AllowOrigin, &cors.AllowMethods, &cors.AllowHeaders, &corsCreds, &cors.MaxAgeSecs, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return &Settings{
			HitLogMaxAgeDays:          DefaultHitLogMaxAgeDays,
			HitLogMaxRowsPerMock:      DefaultHitLogMaxRowsPerMock,
			MaxVersionsPerMock:        DefaultMaxVersionsPerMock,
			RedactedHeaders:           DefaultRedactedHeaders,
			MaxCapturedBodyBytes:      DefaultMaxCapturedBodyBytes,
			SessionTimeoutMinutes:     DefaultSessionTimeoutMinutes,
			LoadTestRunMaxAgeDays:     DefaultLoadTestRunMaxAgeDays,
			LoadTestRunMaxRowsPerItem: DefaultLoadTestRunMaxRowsPerItem,
			CORS:                      DefaultCORS(),
			// InactivityLockMinutes deliberately omitted — its zero value
			// (0/off) IS the correct default for a fresh instance.
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan app settings: %w", err)
	}

	settingsRow := &Settings{
		HitLogMaxAgeDays:          maxAgeDays,
		HitLogMaxRowsPerMock:      maxRows,
		MaxVersionsPerMock:        maxVersions,
		RedactedHeaders:           DefaultRedactedHeaders,
		DefaultResponseDelayMs:    defaultDelayMs,
		DefaultFailureRatePercent: defaultFailureRate,
		MaxCapturedBodyBytes:      maxCapturedBodyBytes,
		SessionTimeoutMinutes:     sessionTimeoutMinutes,
		InactivityLockMinutes:     inactivityLockMinutes,
		LoadTestRunMaxAgeDays:     loadTestRunMaxAgeDays,
		LoadTestRunMaxRowsPerItem: loadTestRunMaxRowsPerItem,
	}
	cors.Enabled, cors.AllowCredentials = corsEnabled != 0, corsCreds != 0
	settingsRow.CORS = cors
	if settingsRow.SessionTimeoutMinutes <= 0 {
		// Same "0 is indistinguishable from never-set" reasoning as
		// MaxVersionsPerMock/MaxCapturedBodyBytes below — and a literal 0
		// would mean every session expires instantly, which isn't a
		// meaningful value to actually honor.
		settingsRow.SessionTimeoutMinutes = DefaultSessionTimeoutMinutes
	}
	if settingsRow.MaxVersionsPerMock <= 0 {
		// Either a genuinely pre-existing app_settings row from before this
		// column existed (backfilled to 0 by the additive migration), or an
		// explicit attempt to configure "0 versions kept" — indistinguishable
		// from here, but 0 would defeat the whole feature's purpose (there'd
		// be nothing left to ever restore), so it's treated as "not set yet"
		// rather than honored literally.
		settingsRow.MaxVersionsPerMock = DefaultMaxVersionsPerMock
	}
	if settingsRow.MaxCapturedBodyBytes <= 0 {
		// Same reasoning as MaxVersionsPerMock above: a pre-existing row
		// predating this column (backfilled to 0) is indistinguishable from
		// an explicit "capture nothing", but 0 would defeat the hit log's
		// whole purpose of showing what a mock actually returned.
		settingsRow.MaxCapturedBodyBytes = DefaultMaxCapturedBodyBytes
	}
	if settingsRow.LoadTestRunMaxAgeDays <= 0 {
		// Same reasoning as MaxVersionsPerMock above: a pre-existing row
		// predating these columns (backfilled to 0) is indistinguishable
		// from an explicit "0 days", but 0 would purge every run
		// immediately, defeating the whole feature.
		settingsRow.LoadTestRunMaxAgeDays = DefaultLoadTestRunMaxAgeDays
	}
	if settingsRow.LoadTestRunMaxRowsPerItem <= 0 {
		settingsRow.LoadTestRunMaxRowsPerItem = DefaultLoadTestRunMaxRowsPerItem
	}
	if redactedHeadersJSON.Valid && redactedHeadersJSON.String != "" {
		if err := json.Unmarshal([]byte(redactedHeadersJSON.String), &settingsRow.RedactedHeaders); err != nil {
			return nil, fmt.Errorf("unmarshal redacted headers: %w", err)
		}
	}
	if settingsRow.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return settingsRow, nil
}

func (s *Store) Save(settingsRow *Settings) error {
	settingsRow.UpdatedAt = time.Now().UTC()
	redactedHeadersJSON, err := json.Marshal(settingsRow.RedactedHeaders)
	if err != nil {
		return fmt.Errorf("marshal redacted headers: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO app_settings (id, hit_log_max_age_days, hit_log_max_rows_per_mock, max_versions_per_mock, redacted_headers_json,
		                           default_response_delay_ms, default_failure_rate_percent, max_captured_body_bytes, session_timeout_minutes,
		                           inactivity_lock_minutes, load_test_run_max_age_days, load_test_run_max_rows_per_item,
		                           cors_enabled, cors_allow_origin, cors_allow_methods, cors_allow_headers, cors_allow_credentials, cors_max_age_secs, updated_at)
		 VALUES ('default', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   hit_log_max_age_days=excluded.hit_log_max_age_days,
		   hit_log_max_rows_per_mock=excluded.hit_log_max_rows_per_mock,
		   max_versions_per_mock=excluded.max_versions_per_mock,
		   redacted_headers_json=excluded.redacted_headers_json,
		   default_response_delay_ms=excluded.default_response_delay_ms,
		   default_failure_rate_percent=excluded.default_failure_rate_percent,
		   max_captured_body_bytes=excluded.max_captured_body_bytes,
		   session_timeout_minutes=excluded.session_timeout_minutes,
		   inactivity_lock_minutes=excluded.inactivity_lock_minutes,
		   load_test_run_max_age_days=excluded.load_test_run_max_age_days,
		   load_test_run_max_rows_per_item=excluded.load_test_run_max_rows_per_item,
		   cors_enabled=excluded.cors_enabled,
		   cors_allow_origin=excluded.cors_allow_origin,
		   cors_allow_methods=excluded.cors_allow_methods,
		   cors_allow_headers=excluded.cors_allow_headers,
		   cors_allow_credentials=excluded.cors_allow_credentials,
		   cors_max_age_secs=excluded.cors_max_age_secs,
		   updated_at=excluded.updated_at`,
		settingsRow.HitLogMaxAgeDays, settingsRow.HitLogMaxRowsPerMock, settingsRow.MaxVersionsPerMock, string(redactedHeadersJSON),
		settingsRow.DefaultResponseDelayMs, settingsRow.DefaultFailureRatePercent, settingsRow.MaxCapturedBodyBytes, settingsRow.SessionTimeoutMinutes,
		settingsRow.InactivityLockMinutes, settingsRow.LoadTestRunMaxAgeDays, settingsRow.LoadTestRunMaxRowsPerItem,
		boolToInt(settingsRow.CORS.Enabled), settingsRow.CORS.AllowOrigin, settingsRow.CORS.AllowMethods, settingsRow.CORS.AllowHeaders,
		boolToInt(settingsRow.CORS.AllowCredentials), settingsRow.CORS.MaxAgeSecs, formatTime(settingsRow.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("save app settings: %w", err)
	}
	return nil
}

func formatTime(t time.Time) string         { return t.Format(time.RFC3339Nano) }
func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

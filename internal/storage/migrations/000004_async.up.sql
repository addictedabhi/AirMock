ALTER TABLE mocks ADD COLUMN mode TEXT NOT NULL DEFAULT 'sync';
ALTER TABLE mocks ADD COLUMN async_config_json TEXT;

CREATE TABLE IF NOT EXISTS callback_jobs (
    id              TEXT PRIMARY KEY,
    mock_id         TEXT NOT NULL REFERENCES mocks(id),
    target_url      TEXT NOT NULL,
    method          TEXT NOT NULL DEFAULT 'POST',
    headers_json    TEXT NOT NULL DEFAULT '{}',
    payload         TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'pending', -- pending | claimed | succeeded | failed
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    max_attempts    INTEGER NOT NULL DEFAULT 3,
    next_attempt_at TEXT NOT NULL,
    last_error      TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_callback_jobs_status_next ON callback_jobs(status, next_attempt_at);

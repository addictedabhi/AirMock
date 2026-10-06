-- A scheduled event fires an outbound HTTP callback on a fixed interval,
-- independent of any incoming mock hit (see internal/scheduledevent).
CREATE TABLE IF NOT EXISTS scheduled_events (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 1,
    interval_secs INTEGER NOT NULL,
    target_url    TEXT NOT NULL,
    method        TEXT NOT NULL DEFAULT 'POST',
    headers_json  TEXT NOT NULL DEFAULT '{}',
    body_template TEXT NOT NULL DEFAULT '',
    last_fired_at TEXT,
    last_status   INTEGER,
    last_error    TEXT,
    next_fire_at  TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_scheduled_events_due ON scheduled_events(enabled, next_fire_at);

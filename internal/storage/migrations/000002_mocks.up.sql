CREATE TABLE IF NOT EXISTS mocks (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    protocol_type TEXT NOT NULL DEFAULT 'rest',
    method        TEXT NOT NULL,
    path_pattern  TEXT NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 1,
    response_json TEXT NOT NULL,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX IF NOT EXISTS idx_mocks_method_path ON mocks(method, path_pattern);

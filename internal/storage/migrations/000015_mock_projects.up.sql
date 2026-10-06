CREATE TABLE IF NOT EXISTS mock_projects (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    base_path  TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

ALTER TABLE mocks ADD COLUMN project_id TEXT NOT NULL DEFAULT '';

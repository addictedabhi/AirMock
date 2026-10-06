CREATE TABLE IF NOT EXISTS workspaces (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

ALTER TABLE collections ADD COLUMN workspace_id TEXT NOT NULL DEFAULT '';
ALTER TABLE environments ADD COLUMN workspace_id TEXT NOT NULL DEFAULT '';

-- Every install starts with exactly one workspace, and every
-- already-existing collection/environment belongs to it. The fixed
-- timestamp (rather than SQLite's own datetime('now')) matches the
-- RFC3339Nano format internal/apiclient/store.go's parseTime expects —
-- a real created_at doesn't matter for a migration-seeded row.
INSERT INTO workspaces (id, name, created_at, updated_at)
VALUES ('default', 'Default', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');

UPDATE collections SET workspace_id = 'default' WHERE workspace_id = '';
UPDATE environments SET workspace_id = 'default' WHERE workspace_id = '';

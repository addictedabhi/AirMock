-- One row per past state of a mock, written just before every Update (see
-- Store.snapshotVersion) — so "version history" always means "what this
-- mock looked like immediately before its most recent N edits," not a
-- separately-triggered snapshot a user has to remember to take.
CREATE TABLE IF NOT EXISTS mock_versions (
    id              TEXT PRIMARY KEY,
    mock_id         TEXT NOT NULL,
    definition_json TEXT NOT NULL,
    created_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_mock_versions_mock_id ON mock_versions(mock_id, created_at DESC);

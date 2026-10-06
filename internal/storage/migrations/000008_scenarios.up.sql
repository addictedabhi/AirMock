ALTER TABLE mocks ADD COLUMN scenario_json TEXT;

CREATE TABLE IF NOT EXISTS mock_state (
    mock_id     TEXT NOT NULL,
    session_key TEXT NOT NULL,
    step        INTEGER NOT NULL DEFAULT 0,
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (mock_id, session_key)
);

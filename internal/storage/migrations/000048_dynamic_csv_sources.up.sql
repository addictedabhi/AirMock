CREATE TABLE dynamic_csv_sources (
    owner_id   TEXT PRIMARY KEY,
    mode       TEXT NOT NULL CHECK (mode IN ('round_robin','random')) DEFAULT 'round_robin',
    content    TEXT NOT NULL,
    next_index INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

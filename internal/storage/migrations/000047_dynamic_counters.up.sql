CREATE TABLE dynamic_counters (
    owner_id   TEXT NOT NULL,
    name       TEXT NOT NULL,
    value      INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (owner_id, name)
);

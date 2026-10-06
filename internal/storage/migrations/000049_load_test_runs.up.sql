CREATE TABLE load_test_runs (
    id TEXT PRIMARY KEY,
    item_id TEXT NOT NULL DEFAULT '',
    collection_id TEXT NOT NULL DEFAULT '',
    method TEXT NOT NULL,
    url TEXT NOT NULL,
    config_json TEXT NOT NULL,
    result_json TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL
);

CREATE INDEX idx_load_test_runs_item_id ON load_test_runs (item_id);
CREATE INDEX idx_load_test_runs_collection_id ON load_test_runs (collection_id);

CREATE TABLE load_test_run_samples (
    run_id TEXT NOT NULL,
    idx INTEGER NOT NULL,
    elapsed_ms INTEGER NOT NULL,
    latency_ms INTEGER NOT NULL,
    status_code INTEGER NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, idx)
);

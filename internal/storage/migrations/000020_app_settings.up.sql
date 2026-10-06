CREATE TABLE app_settings (
    id TEXT PRIMARY KEY,
    hit_log_max_age_days INTEGER NOT NULL,
    hit_log_max_rows_per_mock INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);

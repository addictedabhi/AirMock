ALTER TABLE app_settings ADD COLUMN default_response_delay_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE app_settings ADD COLUMN default_failure_rate_percent REAL NOT NULL DEFAULT 0;
ALTER TABLE app_settings ADD COLUMN max_captured_body_bytes INTEGER NOT NULL DEFAULT 0;

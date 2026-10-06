ALTER TABLE app_settings ADD COLUMN load_test_run_max_age_days INTEGER NOT NULL DEFAULT 0;
ALTER TABLE app_settings ADD COLUMN load_test_run_max_rows_per_item INTEGER NOT NULL DEFAULT 0;

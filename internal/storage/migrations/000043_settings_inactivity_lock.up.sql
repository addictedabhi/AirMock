-- 0 (the default for every existing row, since this is a brand-new opt-in
-- feature) means "off" — unlike session_timeout_minutes, 0 is a genuinely
-- meaningful, intentional value here, not a "not configured yet" sentinel
-- that needs falling back to some non-zero default.
ALTER TABLE app_settings ADD COLUMN inactivity_lock_minutes INTEGER NOT NULL DEFAULT 0;

ALTER TABLE app_settings ADD COLUMN session_timeout_minutes INTEGER NOT NULL DEFAULT 0;
-- Backfill from the old hours-granularity column so an existing saved
-- value (rather than silently reverting to the default) survives the
-- switch to minutes-granularity.
UPDATE app_settings SET session_timeout_minutes = session_timeout_hours * 60 WHERE session_timeout_hours > 0;

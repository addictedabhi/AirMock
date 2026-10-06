DROP INDEX IF EXISTS idx_email_templates_name;
DROP TABLE IF EXISTS email_templates;
DROP TABLE IF EXISTS smtp_settings;

-- This project's migrations are additive-only in practice (see
-- 000004_async.down.sql for the same reasoning) — the down migration for
-- the two callback_jobs ADD COLUMNs above is intentionally a no-op rather
-- than depending on SQLite 3.35+'s ALTER TABLE DROP COLUMN.

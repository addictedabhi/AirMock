DROP INDEX IF EXISTS idx_callback_jobs_status_next;
DROP TABLE IF EXISTS callback_jobs;

-- SQLite has no DROP COLUMN prior to 3.35's ALTER TABLE DROP COLUMN; since
-- this project's migrations are additive-only in practice, the down
-- migration for the two ADD COLUMNs above is intentionally a no-op.

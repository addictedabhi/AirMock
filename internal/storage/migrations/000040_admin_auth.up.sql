-- Singleton row (id='default') holding the admin login's password hash —
-- see internal/auth. An empty/missing hash means login is disabled,
-- same meaning as cfg.AdminPassword being unset.
CREATE TABLE IF NOT EXISTS admin_auth (
    id            TEXT PRIMARY KEY DEFAULT 'default',
    password_hash TEXT NOT NULL DEFAULT ''
);

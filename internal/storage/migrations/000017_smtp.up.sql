-- Singleton row (id='default') describing the outgoing SMTP relay used to
-- deliver an async mock's callback as an email instead of an HTTP webhook.
CREATE TABLE IF NOT EXISTS smtp_settings (
    id           TEXT PRIMARY KEY DEFAULT 'default',
    host         TEXT NOT NULL DEFAULT '',
    port         INTEGER NOT NULL DEFAULT 587,
    username     TEXT NOT NULL DEFAULT '',
    password     TEXT NOT NULL DEFAULT '',
    from_address TEXT NOT NULL DEFAULT '',
    use_tls      INTEGER NOT NULL DEFAULT 1,
    updated_at   TEXT NOT NULL
);

-- Named HTML email templates with {{placeholder}} substitution (the same
-- text/template+sprig engine as every other response body in this app),
-- reusable across any number of async mocks' email callbacks instead of
-- copy-pasting one inline per mock.
CREATE TABLE IF NOT EXISTS email_templates (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    subject    TEXT NOT NULL DEFAULT '',
    html_body  TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_email_templates_name ON email_templates (lower(trim(name)));

-- An async mock's callback can now be delivered over email instead of an
-- HTTP webhook; channel defaults to 'http' so every existing callback_jobs
-- row (and every mock that never touches this) keeps behaving exactly as
-- before. subject is only meaningful for channel='email'.
ALTER TABLE callback_jobs ADD COLUMN channel TEXT NOT NULL DEFAULT 'http';
ALTER TABLE callback_jobs ADD COLUMN subject TEXT NOT NULL DEFAULT '';

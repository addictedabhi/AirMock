-- The "personal" (display name) part of the From header — RFC 2822
-- terminology for the "John Doe" in `From: John Doe <john@example.com>`.
ALTER TABLE smtp_settings ADD COLUMN from_name TEXT NOT NULL DEFAULT '';

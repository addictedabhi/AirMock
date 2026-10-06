ALTER TABLE workspaces ADD COLUMN lock_credential_type TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN lock_password_hash TEXT NOT NULL DEFAULT '';

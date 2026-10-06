-- Length of a PIN credential (0 = not a PIN, or not known yet), so the login
-- and workspace-unlock keypads can submit exactly when the PIN is complete
-- instead of guessing 4 digits.
ALTER TABLE admin_auth ADD COLUMN pin_length INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workspaces ADD COLUMN lock_pin_length INTEGER NOT NULL DEFAULT 0;

package auth

import (
	"database/sql"
	"errors"
	"fmt"
)

// Store persists the admin login's credential (see admin_auth in
// internal/storage/migrations) — a single row, same "singleton keyed by
// id='default'" pattern internal/certs and internal/settings already use
// for their own instance-wide, non-per-mock state.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// GetCredential returns the persisted credential type ("pin" or
// "password") and bcrypt hash, or ("", "") if none has ever been set
// (login is disabled) — never an error for that case, since "not
// configured yet" is the normal, expected state for a fresh instance.
func (s *Store) GetCredential() (credentialType string, hash string, err error) {
	var t, h sql.NullString
	scanErr := s.db.QueryRow(`SELECT credential_type, password_hash FROM admin_auth WHERE id='default'`).Scan(&t, &h)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return "", "", nil
	}
	if scanErr != nil {
		return "", "", fmt.Errorf("get admin credential: %w", scanErr)
	}
	return t.String, h.String, nil
}

// SetCredential saves credentialType/hash as the current admin credential.
// An empty hash means "no password" — the same as it never having been
// set — which is how Manager.ClearPassword disables login.
func (s *Store) SetCredential(credentialType, hash string) error {
	_, err := s.db.Exec(
		`INSERT INTO admin_auth (id, credential_type, password_hash) VALUES ('default', ?, ?)
		 ON CONFLICT(id) DO UPDATE SET credential_type=excluded.credential_type, password_hash=excluded.password_hash`,
		credentialType, hash,
	)
	if err != nil {
		return fmt.Errorf("set admin credential: %w", err)
	}
	return nil
}

// GetPinLength returns the stored PIN length, or 0 if the credential is not a
// PIN or its length is not known (a PIN saved before the length was recorded).
func (s *Store) GetPinLength() (int, error) {
	var n sql.NullInt64
	err := s.db.QueryRow(`SELECT pin_length FROM admin_auth WHERE id='default'`).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get admin PIN length: %w", err)
	}
	return int(n.Int64), nil
}

// SetPinLength records the PIN length for the current credential.
func (s *Store) SetPinLength(n int) error {
	_, err := s.db.Exec(
		`INSERT INTO admin_auth (id, credential_type, password_hash, pin_length) VALUES ('default', '', '', ?)
		 ON CONFLICT(id) DO UPDATE SET pin_length=excluded.pin_length`, n)
	if err != nil {
		return fmt.Errorf("set admin PIN length: %w", err)
	}
	return nil
}

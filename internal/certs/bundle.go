package certs

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrDuplicateBundleName mirrors ErrDuplicateName — the bundle picker
// (mock/project/gateway TLS forms) is keyed on name for the user, not id.
var ErrDuplicateBundleName = errors.New("certs: a bundle with this name already exists")

// Bundle names a related CA + server cert + client cert together — the
// output of the "Generate certificate" quick-flow (which already creates
// exactly these three), now persisted as one named, reusable group instead
// of three independently-tracked rows related only by IssuerID. Applying a
// bundle to a mock/project/the gateway resolves ServerCertID (the TLS
// listener's own cert) and CAID (for verifying client certs) in one pick
// instead of two. ServerCertID/ClientCertID are optional — a bundle can be
// CA-only (e.g. just a trust root to verify client certs against) — but
// CAID is required since every bundle exists to relate certs to ONE CA.
type Bundle struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	CAID         string    `json:"caId"`
	ServerCertID string    `json:"serverCertId,omitempty"`
	ClientCertID string    `json:"clientCertId,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func (s *Store) bundleNameTaken(name, excludeID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM cert_bundles WHERE lower(trim(name)) = lower(trim(?)) AND id != ?`,
		name, excludeID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check duplicate bundle name: %w", err)
	}
	return count > 0, nil
}

func (s *Store) CreateBundle(b *Bundle) (*Bundle, error) {
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	if b.Name != "" {
		taken, err := s.bundleNameTaken(b.Name, b.ID)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, ErrDuplicateBundleName
		}
	}
	now := time.Now().UTC()
	b.CreatedAt, b.UpdatedAt = now, now

	_, err := s.db.Exec(
		`INSERT INTO cert_bundles (id, name, ca_id, server_cert_id, client_cert_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.Name, nullableString(b.CAID), nullableString(b.ServerCertID), nullableString(b.ClientCertID),
		formatTime(b.CreatedAt), formatTime(b.UpdatedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("insert bundle: %w", err)
	}
	return b, nil
}

func (s *Store) UpdateBundle(b *Bundle) (*Bundle, error) {
	if b.Name != "" {
		taken, err := s.bundleNameTaken(b.Name, b.ID)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, ErrDuplicateBundleName
		}
	}
	b.UpdatedAt = time.Now().UTC()

	res, err := s.db.Exec(
		`UPDATE cert_bundles SET name=?, ca_id=?, server_cert_id=?, client_cert_id=?, updated_at=? WHERE id=?`,
		b.Name, nullableString(b.CAID), nullableString(b.ServerCertID), nullableString(b.ClientCertID), formatTime(b.UpdatedAt), b.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update bundle: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return b, nil
}

func (s *Store) GetBundle(id string) (*Bundle, error) {
	row := s.db.QueryRow(
		`SELECT id, name, ca_id, server_cert_id, client_cert_id, created_at, updated_at FROM cert_bundles WHERE id=?`, id)
	return scanBundle(row)
}

func (s *Store) ListBundles() ([]*Bundle, error) {
	rows, err := s.db.Query(
		`SELECT id, name, ca_id, server_cert_id, client_cert_id, created_at, updated_at FROM cert_bundles ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list bundles: %w", err)
	}
	defer rows.Close()

	out := []*Bundle{}
	for rows.Next() {
		b, err := scanBundle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteBundle removes only the bundle grouping, never the underlying
// certificates — a bundle is a reference layer over rows in `certificates`
// that may still be directly referenced elsewhere (a mock's raw
// certificateId, another bundle, download links already handed out), same
// non-destructive philosophy as mock.Store.DeleteProject.
func (s *Store) DeleteBundle(id string) error {
	res, err := s.db.Exec(`DELETE FROM cert_bundles WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete bundle: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanBundle(row scanner) (*Bundle, error) {
	var b Bundle
	var caID, serverCertID, clientCertID sql.NullString
	var createdAt, updatedAt string

	if err := row.Scan(&b.ID, &b.Name, &caID, &serverCertID, &clientCertID, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan bundle: %w", err)
	}
	b.CAID = caID.String
	b.ServerCertID = serverCertID.String
	b.ClientCertID = clientCertID.String

	var err error
	if b.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	if b.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return &b, nil
}

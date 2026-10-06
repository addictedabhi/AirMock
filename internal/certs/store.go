package certs

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrNotFound = errors.New("certs: not found")

// ErrDuplicateName guards certificate names being unique store-wide, since
// the cert/CA picker dropdowns (mock HTTPS binding, mTLS verify-CA) are
// keyed on name for the user, not the underlying ID.
var ErrDuplicateName = errors.New("certs: a certificate with this name already exists")

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) nameTaken(name, excludeID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM certificates WHERE lower(trim(name)) = lower(trim(?)) AND id != ?`,
		name, excludeID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check duplicate certificate name: %w", err)
	}
	return count > 0, nil
}

func (s *Store) Save(c *Certificate) error {
	if c.Name != "" {
		taken, err := s.nameTaken(c.Name, c.ID)
		if err != nil {
			return err
		}
		if taken {
			return ErrDuplicateName
		}
	}
	sansJSON, err := json.Marshal(c.SANs)
	if err != nil {
		return fmt.Errorf("marshal sans: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO certificates (id, name, kind, issuer_id, common_name, sans_json, key_algorithm, cert_pem, key_pem, not_before, not_after, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, string(c.Kind), nullableString(c.IssuerID), c.CommonName, string(sansJSON), string(c.KeyAlgorithm),
		c.CertPEM, c.KeyPEM, formatTime(c.NotBefore), formatTime(c.NotAfter), formatTime(c.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert certificate: %w", err)
	}
	return nil
}

// Update overwrites an existing certificate row in place (same id) — used
// by "renew," which regenerates the key/cert material with the same name/
// kind/commonName/SANs/issuer but a fresh validity window, so every
// existing certId/clientCaId reference (a mock's HTTPS binding, the
// gateway's mTLS trust CA) keeps pointing at something valid without
// having to be manually rebound to a new id.
func (s *Store) Update(c *Certificate) error {
	if c.Name != "" {
		taken, err := s.nameTaken(c.Name, c.ID)
		if err != nil {
			return err
		}
		if taken {
			return ErrDuplicateName
		}
	}
	sansJSON, err := json.Marshal(c.SANs)
	if err != nil {
		return fmt.Errorf("marshal sans: %w", err)
	}
	res, err := s.db.Exec(
		`UPDATE certificates SET name=?, kind=?, issuer_id=?, common_name=?, sans_json=?, key_algorithm=?, cert_pem=?, key_pem=?, not_before=?, not_after=?
		 WHERE id=?`,
		c.Name, string(c.Kind), nullableString(c.IssuerID), c.CommonName, string(sansJSON), string(c.KeyAlgorithm),
		c.CertPEM, c.KeyPEM, formatTime(c.NotBefore), formatTime(c.NotAfter), c.ID,
	)
	if err != nil {
		return fmt.Errorf("update certificate: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update certificate: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Get(id string) (*Certificate, error) {
	row := s.db.QueryRow(
		`SELECT id, name, kind, issuer_id, common_name, sans_json, key_algorithm, cert_pem, key_pem, not_before, not_after, created_at
		 FROM certificates WHERE id=?`, id)
	return scanCertificate(row)
}

func (s *Store) List() ([]*Certificate, error) {
	rows, err := s.db.Query(
		`SELECT id, name, kind, issuer_id, common_name, sans_json, key_algorithm, cert_pem, key_pem, not_before, not_after, created_at
		 FROM certificates ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list certificates: %w", err)
	}
	defer rows.Close()

	out := []*Certificate{}
	for rows.Next() {
		c, err := scanCertificate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListCAs returns only CA-kind certificates, for issuer/verify-CA dropdowns.
func (s *Store) ListCAs() ([]*Certificate, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var cas []*Certificate
	for _, c := range all {
		if c.Kind == KindCA {
			cas = append(cas, c)
		}
	}
	return cas, nil
}

func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM certificates WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete certificate: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanCertificate(row scanner) (*Certificate, error) {
	var c Certificate
	var kind, sansJSON, keyAlgo, notBefore, notAfter, createdAt string
	var issuerID sql.NullString

	if err := row.Scan(&c.ID, &c.Name, &kind, &issuerID, &c.CommonName, &sansJSON, &keyAlgo, &c.CertPEM, &c.KeyPEM, &notBefore, &notAfter, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan certificate: %w", err)
	}
	c.Kind = Kind(kind)
	c.IssuerID = issuerID.String
	c.KeyAlgorithm = KeyAlgorithm(keyAlgo)
	c.HasKey = c.KeyPEM != ""
	if err := json.Unmarshal([]byte(sansJSON), &c.SANs); err != nil {
		return nil, fmt.Errorf("unmarshal sans: %w", err)
	}

	var err error
	if c.NotBefore, err = parseTime(notBefore); err != nil {
		return nil, fmt.Errorf("parse not_before: %w", err)
	}
	if c.NotAfter, err = parseTime(notAfter); err != nil {
		return nil, fmt.Errorf("parse not_after: %w", err)
	}
	if c.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	return &c, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Same pitfall as internal/mock/store.go: modernc.org/sqlite doesn't
// auto-scan TEXT into time.Time, so timestamps round-trip as RFC3339Nano
// strings explicitly.
func formatTime(t time.Time) string         { return t.Format(time.RFC3339Nano) }
func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

package smtp

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("smtp: template not found")

// ErrDuplicateName guards template names being unique store-wide, since the
// template picker on an async mock's email callback is keyed on name for
// the user, not the underlying id.
var ErrDuplicateName = errors.New("smtp: a template with this name already exists")

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) GetSettings() (Settings, error) {
	row := s.db.QueryRow(
		`SELECT host, port, username, password, from_name, from_address, use_tls, updated_at FROM smtp_settings WHERE id='default'`)

	var useTLSInt int
	var updatedAt string
	var st Settings
	err := row.Scan(&st.Host, &st.Port, &st.Username, &st.Password, &st.FromName, &st.FromAddress, &useTLSInt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{Port: 587, UseTLS: true}, nil // no row yet => sensible defaults, not configured
	}
	if err != nil {
		return Settings{}, fmt.Errorf("scan smtp settings: %w", err)
	}
	st.UseTLS = useTLSInt != 0
	if st.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return Settings{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return st, nil
}

func (s *Store) SaveSettings(st Settings) (Settings, error) {
	st.UpdatedAt = time.Now().UTC()
	_, err := s.db.Exec(
		`INSERT INTO smtp_settings (id, host, port, username, password, from_name, from_address, use_tls, updated_at)
		 VALUES ('default', ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   host=excluded.host, port=excluded.port, username=excluded.username, password=excluded.password,
		   from_name=excluded.from_name, from_address=excluded.from_address, use_tls=excluded.use_tls, updated_at=excluded.updated_at`,
		st.Host, st.Port, st.Username, st.Password, st.FromName, st.FromAddress, boolToInt(st.UseTLS), formatTime(st.UpdatedAt),
	)
	if err != nil {
		return Settings{}, fmt.Errorf("save smtp settings: %w", err)
	}
	return st, nil
}

func (s *Store) nameTaken(name, excludeID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM email_templates WHERE lower(trim(name)) = lower(trim(?)) AND id != ?`,
		name, excludeID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check duplicate template name: %w", err)
	}
	return count > 0, nil
}

func (s *Store) ListTemplates() ([]*Template, error) {
	rows, err := s.db.Query(`SELECT id, name, subject, html_body, created_at, updated_at FROM email_templates ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list email templates: %w", err)
	}
	defer rows.Close()

	out := []*Template{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetTemplate(id string) (*Template, error) {
	row := s.db.QueryRow(`SELECT id, name, subject, html_body, created_at, updated_at FROM email_templates WHERE id=?`, id)
	return scanTemplate(row)
}

// GetEmailTemplate adapts GetTemplate to the (subject, htmlBody string, err
// error) shape internal/engine/http.EmailTemplateResolver expects, so that
// package can depend on a narrow interface instead of this concrete store.
func (s *Store) GetEmailTemplate(id string) (subject, htmlBody string, err error) {
	t, err := s.GetTemplate(id)
	if err != nil {
		return "", "", err
	}
	return t.Subject, t.HTMLBody, nil
}

func (s *Store) CreateTemplate(t *Template) (*Template, error) {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if taken, err := s.nameTaken(t.Name, t.ID); err != nil {
		return nil, err
	} else if taken {
		return nil, ErrDuplicateName
	}
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt = now, now
	_, err := s.db.Exec(
		`INSERT INTO email_templates (id, name, subject, html_body, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Subject, t.HTMLBody, formatTime(t.CreatedAt), formatTime(t.UpdatedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("insert email template: %w", err)
	}
	return t, nil
}

func (s *Store) UpdateTemplate(t *Template) (*Template, error) {
	if taken, err := s.nameTaken(t.Name, t.ID); err != nil {
		return nil, err
	} else if taken {
		return nil, ErrDuplicateName
	}
	t.UpdatedAt = time.Now().UTC()
	res, err := s.db.Exec(
		`UPDATE email_templates SET name=?, subject=?, html_body=?, updated_at=? WHERE id=?`,
		t.Name, t.Subject, t.HTMLBody, formatTime(t.UpdatedAt), t.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update email template: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("update email template: %w", err)
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	return t, nil
}

func (s *Store) DeleteTemplate(id string) error {
	res, err := s.db.Exec(`DELETE FROM email_templates WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete email template: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete email template: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTemplate(row scanner) (*Template, error) {
	var t Template
	var createdAt, updatedAt string
	err := row.Scan(&t.ID, &t.Name, &t.Subject, &t.HTMLBody, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan email template: %w", err)
	}
	if t.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	if t.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return &t, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Same modernc.org/sqlite timestamp pitfall documented in every other store
// in this project: TEXT columns don't auto-scan into time.Time.
func formatTime(t time.Time) string         { return t.Format(time.RFC3339Nano) }
func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

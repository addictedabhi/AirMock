package mock

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// marshalProjectTLS/unmarshalProjectTLS round-trip Project.TLS through the
// tls_json column the same way mock.Definition's optional protocol configs
// do — nil marshals to "" (NOT NULL DEFAULT ” from the migration) rather
// than the literal string "null", so an empty column reads back as nil
// without a special case.
func marshalProjectTLS(tls *TCPTLSConfig) (string, error) {
	if tls == nil {
		return "", nil
	}
	b, err := json.Marshal(tls)
	if err != nil {
		return "", fmt.Errorf("marshal project tls: %w", err)
	}
	return string(b), nil
}

func unmarshalProjectTLS(raw string) (*TCPTLSConfig, error) {
	if raw == "" {
		return nil, nil
	}
	var tls TCPTLSConfig
	if err := json.Unmarshal([]byte(raw), &tls); err != nil {
		return nil, fmt.Errorf("unmarshal project tls: %w", err)
	}
	return &tls, nil
}

// ErrDuplicateProjectName guards project names being unique app-wide, since
// the project picker (Mocks and TCP Mocks pages) is keyed on name for the
// user, not the underlying ID.
var ErrDuplicateProjectName = errors.New("mock: a project with this name already exists")

func (s *Store) projectNameTaken(name, excludeID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM mock_projects WHERE lower(trim(name)) = lower(trim(?)) AND id != ?`,
		name, excludeID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check duplicate project name: %w", err)
	}
	return count > 0, nil
}

func (s *Store) CreateProject(p *Project) (*Project, error) {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.Name != "" {
		taken, err := s.projectNameTaken(p.Name, p.ID)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, ErrDuplicateProjectName
		}
	}
	now := time.Now().UTC()
	p.CreatedAt, p.UpdatedAt = now, now

	tlsJSON, err := marshalProjectTLS(p.TLS)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(
		`INSERT INTO mock_projects (id, name, base_path, gateway_port, tls_json, workspace_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.BasePath, p.GatewayPort, tlsJSON, p.WorkspaceID, formatTime(p.CreatedAt), formatTime(p.UpdatedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("insert project: %w", err)
	}
	return p, nil
}

func (s *Store) UpdateProject(p *Project) (*Project, error) {
	if p.Name != "" {
		taken, err := s.projectNameTaken(p.Name, p.ID)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, ErrDuplicateProjectName
		}
	}
	p.UpdatedAt = time.Now().UTC()

	tlsJSON, err := marshalProjectTLS(p.TLS)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Exec(
		`UPDATE mock_projects SET name=?, base_path=?, gateway_port=?, tls_json=?, workspace_id=?, updated_at=? WHERE id=?`,
		p.Name, p.BasePath, p.GatewayPort, tlsJSON, p.WorkspaceID, formatTime(p.UpdatedAt), p.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update project: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return p, nil
}

// DeleteProject removes the project AND every mock inside it — deleting a
// project is deleting its whole API surface, not just a grouping label.
// This only handles the database side (mocks, their version history, then
// the project row itself, all in one transaction); the caller
// (MockProjectsHandler.delete) is responsible for unregistering each
// deleted mock from its live engine afterward — call ListByProject first to
// get that list before this removes their rows.
func (s *Store) DeleteProject(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM mock_versions WHERE mock_id IN (SELECT id FROM mocks WHERE project_id = ?)`, id); err != nil {
		return fmt.Errorf("delete project mocks' version history: %w", err)
	}
	// Same reasoning as mock.Store.Delete's own cleanup: each mock's
	// counters/CSV attachment is keyed by its own id with no FK/CASCADE,
	// so it must be swept here too — before the mocks themselves are
	// deleted below, while the subquery can still resolve which ids belong
	// to this project.
	if _, err := tx.Exec(`DELETE FROM dynamic_counters WHERE owner_id IN (SELECT id FROM mocks WHERE project_id = ?)`, id); err != nil {
		return fmt.Errorf("delete project mocks' counters: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM dynamic_csv_sources WHERE owner_id IN (SELECT id FROM mocks WHERE project_id = ?)`, id); err != nil {
		return fmt.Errorf("delete project mocks' csv sources: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM mocks WHERE project_id = ?`, id); err != nil {
		return fmt.Errorf("delete project mocks: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM mock_projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) ListProjects() ([]*Project, error) {
	rows, err := s.db.Query(`SELECT id, name, base_path, gateway_port, tls_json, workspace_id, created_at, updated_at FROM mock_projects ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	out := []*Project{}
	for rows.Next() {
		var p Project
		var tlsJSON, createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.Name, &p.BasePath, &p.GatewayPort, &tlsJSON, &p.WorkspaceID, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		if p.TLS, err = unmarshalProjectTLS(tlsJSON); err != nil {
			return nil, err
		}
		if p.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		if p.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, fmt.Errorf("parse updated_at: %w", err)
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// GetProject fetches a single project by ID.
func (s *Store) GetProject(id string) (*Project, error) {
	row := s.db.QueryRow(`SELECT id, name, base_path, gateway_port, tls_json, workspace_id, created_at, updated_at FROM mock_projects WHERE id=?`, id)
	var p Project
	var tlsJSON, createdAt, updatedAt string
	err := row.Scan(&p.ID, &p.Name, &p.BasePath, &p.GatewayPort, &tlsJSON, &p.WorkspaceID, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan project: %w", err)
	}
	if p.TLS, err = unmarshalProjectTLS(tlsJSON); err != nil {
		return nil, err
	}
	if p.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	if p.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return &p, nil
}

// GatewayPortForProject implements httpengine.ProjectPortResolver: it
// returns 0 (meaning "use the shared default gateway") for an empty
// projectID or one that no longer resolves to a real project — deleting a
// project now cascades to its mocks, so this only matters for the brief
// window between the project row being gone and the engine actually
// unregistering each of its mocks (MockProjectsHandler.delete does that
// right after); failing open there is simpler than erroring out mid-unwind.
func (s *Store) GatewayPortForProject(projectID string) (int, error) {
	if projectID == "" {
		return 0, nil
	}
	p, err := s.GetProject(projectID)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return p.GatewayPort, nil
}

// TLSConfigForProject looks up projectID's TLS config, if any — fails open
// the same way GatewayPortForProject does, returning nil (meaning "no TLS")
// for an empty or unresolvable projectID rather than erroring.
func (s *Store) TLSConfigForProject(projectID string) (*TCPTLSConfig, error) {
	if projectID == "" {
		return nil, nil
	}
	p, err := s.GetProject(projectID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p.TLS, nil
}

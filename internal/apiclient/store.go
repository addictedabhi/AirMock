package apiclient

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var ErrNotFound = errors.New("apiclient: not found")

// Duplicate-name errors: collections and environments are scoped unique
// per workspace (two different workspaces may reuse a name freely), while
// workspace names are unique across the whole install.
var (
	ErrDuplicateCollectionName  = errors.New("apiclient: a collection with this name already exists in this workspace")
	ErrDuplicateEnvironmentName = errors.New("apiclient: an environment with this name already exists in this workspace")
	ErrDuplicateWorkspaceName   = errors.New("apiclient: a workspace with this name already exists")
)

// Workspace-lock errors — see SetWorkspaceLock/ClearWorkspaceLock.
var (
	ErrIncorrectWorkspacePassword = errors.New("apiclient: incorrect workspace password")
	ErrWorkspaceNotLocked         = errors.New("apiclient: this workspace has no lock set")
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) collectionNameTaken(workspaceID, name, excludeID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM collections WHERE workspace_id = ? AND lower(trim(name)) = lower(trim(?)) AND id != ?`,
		workspaceID, name, excludeID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check duplicate collection name: %w", err)
	}
	return count > 0, nil
}

func (s *Store) environmentNameTaken(workspaceID, name, excludeID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM environments WHERE workspace_id = ? AND lower(trim(name)) = lower(trim(?)) AND id != ?`,
		workspaceID, name, excludeID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check duplicate environment name: %w", err)
	}
	return count > 0, nil
}

func (s *Store) workspaceNameTaken(name, excludeID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM workspaces WHERE lower(trim(name)) = lower(trim(?)) AND id != ?`,
		name, excludeID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check duplicate workspace name: %w", err)
	}
	return count > 0, nil
}

// assignIDs fills in an ID for every item in the tree that doesn't already
// have one — new items authored in the UI arrive without one.
func assignIDs(items []Item) {
	for i := range items {
		if items[i].ID == "" {
			items[i].ID = uuid.NewString()
		}
		if len(items[i].Items) > 0 {
			assignIDs(items[i].Items)
		}
	}
}

func (s *Store) CreateCollection(c *Collection) (*Collection, error) {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	if c.WorkspaceID == "" {
		c.WorkspaceID = DefaultWorkspaceID
	}
	if c.Name != "" {
		taken, err := s.collectionNameTaken(c.WorkspaceID, c.Name, c.ID)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, ErrDuplicateCollectionName
		}
	}
	assignIDs(c.Items)
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now

	itemsJSON, err := json.Marshal(c.Items)
	if err != nil {
		return nil, fmt.Errorf("marshal items: %w", err)
	}
	variablesJSON, err := marshalVariables(c.Variables)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(
		`INSERT INTO collections (id, workspace_id, name, items_json, variables_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.WorkspaceID, c.Name, string(itemsJSON), variablesJSON, formatTime(c.CreatedAt), formatTime(c.UpdatedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("insert collection: %w", err)
	}
	return c, nil
}

// marshalVariables always produces a valid JSON object ("{}" for a nil/
// empty map) — variables_json is NOT NULL with no separate "unset" state,
// matching how items_json is never null either.
func marshalVariables(vars map[string]string) (string, error) {
	if vars == nil {
		vars = map[string]string{}
	}
	b, err := json.Marshal(vars)
	if err != nil {
		return "", fmt.Errorf("marshal variables: %w", err)
	}
	return string(b), nil
}

func (s *Store) UpdateCollection(c *Collection) (*Collection, error) {
	if c.Name != "" {
		// workspace_id isn't mutable via update (see the SQL below), so the
		// duplicate check must scope against the collection's actual
		// existing workspace, not whatever WorkspaceID the caller's payload
		// happens to carry.
		var workspaceID string
		if err := s.db.QueryRow(`SELECT workspace_id FROM collections WHERE id=?`, c.ID).Scan(&workspaceID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("lookup collection workspace: %w", err)
		}
		taken, err := s.collectionNameTaken(workspaceID, c.Name, c.ID)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, ErrDuplicateCollectionName
		}
	}
	assignIDs(c.Items)
	c.UpdatedAt = time.Now().UTC()

	itemsJSON, err := json.Marshal(c.Items)
	if err != nil {
		return nil, fmt.Errorf("marshal items: %w", err)
	}
	variablesJSON, err := marshalVariables(c.Variables)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Exec(
		`UPDATE collections SET name=?, items_json=?, variables_json=?, updated_at=? WHERE id=?`,
		c.Name, string(itemsJSON), variablesJSON, formatTime(c.UpdatedAt), c.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update collection: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return c, nil
}

// MoveCollection reassigns a collection to a different workspace — the one
// field UpdateCollection deliberately never touches (see its own comment:
// "workspace_id isn't mutable via update"), since a plain rename/edit
// should never accidentally relocate a collection. The duplicate-name
// check is scoped to the DESTINATION workspace, since arriving there is
// exactly when a name clash first becomes possible.
func (s *Store) MoveCollection(id, workspaceID string) (*Collection, error) {
	c, err := s.GetCollection(id)
	if err != nil {
		return nil, err
	}
	taken, err := s.collectionNameTaken(workspaceID, c.Name, id)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrDuplicateCollectionName
	}
	res, err := s.db.Exec(`UPDATE collections SET workspace_id=?, updated_at=? WHERE id=?`, workspaceID, formatTime(time.Now().UTC()), id)
	if err != nil {
		return nil, fmt.Errorf("move collection: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	c.WorkspaceID = workspaceID
	return c, nil
}

func (s *Store) DeleteCollection(id string) error {
	// Same reasoning as mock.Store.Delete's own dynamic-value cleanup:
	// collection_id has no FK/CASCADE, so a deleted collection's load-test
	// run history would otherwise sit orphaned forever.
	if _, err := s.db.Exec(`DELETE FROM load_test_run_samples WHERE run_id IN (SELECT id FROM load_test_runs WHERE collection_id = ?)`, id); err != nil {
		return fmt.Errorf("delete collection's load test run samples: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM load_test_runs WHERE collection_id = ?`, id); err != nil {
		return fmt.Errorf("delete collection's load test runs: %w", err)
	}
	res, err := s.db.Exec(`DELETE FROM collections WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete collection: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetCollection(id string) (*Collection, error) {
	row := s.db.QueryRow(`SELECT id, workspace_id, name, items_json, variables_json, created_at, updated_at FROM collections WHERE id=?`, id)
	return scanCollection(row)
}

// ListCollections returns collections in workspaceID, or every collection
// (across all workspaces) if workspaceID is empty.
func (s *Store) ListCollections(workspaceID string) ([]*Collection, error) {
	query := `SELECT id, workspace_id, name, items_json, variables_json, created_at, updated_at FROM collections`
	args := []any{}
	if workspaceID != "" {
		query += ` WHERE workspace_id = ?`
		args = append(args, workspaceID)
	}
	query += ` ORDER BY created_at ASC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	defer rows.Close()

	out := []*Collection{}
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanCollection(row scanner) (*Collection, error) {
	var c Collection
	var itemsJSON, variablesJSON, createdAt, updatedAt string
	if err := row.Scan(&c.ID, &c.WorkspaceID, &c.Name, &itemsJSON, &variablesJSON, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan collection: %w", err)
	}
	if err := json.Unmarshal([]byte(itemsJSON), &c.Items); err != nil {
		return nil, fmt.Errorf("unmarshal items: %w", err)
	}
	if err := json.Unmarshal([]byte(variablesJSON), &c.Variables); err != nil {
		return nil, fmt.Errorf("unmarshal variables: %w", err)
	}
	var err error
	if c.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	if c.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return &c, nil
}

func (s *Store) CreateEnvironment(e *Environment) (*Environment, error) {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.WorkspaceID == "" {
		e.WorkspaceID = DefaultWorkspaceID
	}
	if e.Name != "" {
		taken, err := s.environmentNameTaken(e.WorkspaceID, e.Name, e.ID)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, ErrDuplicateEnvironmentName
		}
	}
	now := time.Now().UTC()
	e.CreatedAt, e.UpdatedAt = now, now

	varsJSON, err := json.Marshal(e.Variables)
	if err != nil {
		return nil, fmt.Errorf("marshal variables: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO environments (id, workspace_id, name, variables_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.WorkspaceID, e.Name, string(varsJSON), formatTime(e.CreatedAt), formatTime(e.UpdatedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("insert environment: %w", err)
	}
	return e, nil
}

func (s *Store) UpdateEnvironment(e *Environment) (*Environment, error) {
	if e.Name != "" {
		var workspaceID string
		if err := s.db.QueryRow(`SELECT workspace_id FROM environments WHERE id=?`, e.ID).Scan(&workspaceID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("lookup environment workspace: %w", err)
		}
		taken, err := s.environmentNameTaken(workspaceID, e.Name, e.ID)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, ErrDuplicateEnvironmentName
		}
	}
	e.UpdatedAt = time.Now().UTC()
	varsJSON, err := json.Marshal(e.Variables)
	if err != nil {
		return nil, fmt.Errorf("marshal variables: %w", err)
	}
	res, err := s.db.Exec(
		`UPDATE environments SET name=?, variables_json=?, updated_at=? WHERE id=?`,
		e.Name, string(varsJSON), formatTime(e.UpdatedAt), e.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update environment: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return e, nil
}

func (s *Store) DeleteEnvironment(id string) error {
	res, err := s.db.Exec(`DELETE FROM environments WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete environment: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetEnvironment(id string) (*Environment, error) {
	row := s.db.QueryRow(`SELECT id, workspace_id, name, variables_json, created_at, updated_at FROM environments WHERE id=?`, id)
	return scanEnvironment(row)
}

// ListEnvironments returns environments in workspaceID, or every
// environment (across all workspaces) if workspaceID is empty.
func (s *Store) ListEnvironments(workspaceID string) ([]*Environment, error) {
	query := `SELECT id, workspace_id, name, variables_json, created_at, updated_at FROM environments`
	args := []any{}
	if workspaceID != "" {
		query += ` WHERE workspace_id = ?`
		args = append(args, workspaceID)
	}
	query += ` ORDER BY created_at ASC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	defer rows.Close()

	out := []*Environment{}
	for rows.Next() {
		e, err := scanEnvironment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanEnvironment(row scanner) (*Environment, error) {
	var e Environment
	var varsJSON, createdAt, updatedAt string
	if err := row.Scan(&e.ID, &e.WorkspaceID, &e.Name, &varsJSON, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan environment: %w", err)
	}
	if err := json.Unmarshal([]byte(varsJSON), &e.Variables); err != nil {
		return nil, fmt.Errorf("unmarshal variables: %w", err)
	}
	var err error
	if e.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	if e.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return &e, nil
}

// ErrLastWorkspace guards against ever ending up with zero workspaces —
// the UI always needs at least one to select and create collections into.
var ErrLastWorkspace = errors.New("apiclient: cannot delete the only remaining workspace")

// ErrDefaultWorkspace guards the one workspace every install starts with
// and that any pre-workspace collection/environment (or any caller that
// omits workspaceId) falls back to — deleting it is never allowed,
// regardless of how many other workspaces exist.
var ErrDefaultWorkspace = errors.New("apiclient: the Default workspace cannot be deleted")

// ErrDefaultWorkspaceLock is ErrDefaultWorkspace's counterpart for
// LockWorkspace — same guarded workspace, worded for the operation that
// was actually refused so the error reaching the client isn't misleading.
var ErrDefaultWorkspaceLock = errors.New("apiclient: the Default workspace cannot be locked")

func (s *Store) CreateWorkspace(w *Workspace) (*Workspace, error) {
	if w.ID == "" {
		w.ID = uuid.NewString()
	}
	if w.Name != "" {
		taken, err := s.workspaceNameTaken(w.Name, w.ID)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, ErrDuplicateWorkspaceName
		}
	}
	now := time.Now().UTC()
	w.CreatedAt, w.UpdatedAt = now, now

	_, err := s.db.Exec(
		`INSERT INTO workspaces (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		w.ID, w.Name, formatTime(w.CreatedAt), formatTime(w.UpdatedAt),
	)
	if err != nil {
		return nil, fmt.Errorf("insert workspace: %w", err)
	}
	return w, nil
}

func (s *Store) ListWorkspaces() ([]*Workspace, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at, updated_at, lock_credential_type, lock_password_hash <> '', lock_pin_length FROM workspaces ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()

	out := []*Workspace{}
	for rows.Next() {
		var w Workspace
		var createdAt, updatedAt string
		var lockedInt int
		if err := rows.Scan(&w.ID, &w.Name, &createdAt, &updatedAt, &w.LockCredentialType, &lockedInt, &w.LockPinLength); err != nil {
			return nil, fmt.Errorf("scan workspace: %w", err)
		}
		w.Locked = lockedInt != 0
		if !w.Locked {
			w.LockCredentialType = ""
			w.LockPinLength = 0
		}
		if w.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		if w.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, fmt.Errorf("parse updated_at: %w", err)
		}
		out = append(out, &w)
	}
	return out, rows.Err()
}

// DeleteWorkspace removes a workspace and every collection/environment in
// it. Refuses to delete the Default workspace at all, or the last
// remaining workspace regardless of which one it is — the UI always needs
// somewhere to select and create new collections/environments into.
func (s *Store) DeleteWorkspace(id string) error {
	if id == DefaultWorkspaceID {
		return ErrDefaultWorkspace
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&count); err != nil {
		return fmt.Errorf("count workspaces: %w", err)
	}
	if count <= 1 {
		return ErrLastWorkspace
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Same reasoning as DeleteCollection's own cleanup: collection_id (and
	// in turn run_id) has no FK/CASCADE, and this deletes collections via
	// raw SQL below rather than calling DeleteCollection itself, so its
	// cascade has to be reproduced here explicitly — otherwise every
	// collection in this workspace would leave its load-test-run history
	// (and those runs' samples) orphaned forever.
	if _, err := tx.Exec(
		`DELETE FROM load_test_run_samples WHERE run_id IN (
		   SELECT id FROM load_test_runs WHERE collection_id IN (SELECT id FROM collections WHERE workspace_id = ?)
		 )`, id,
	); err != nil {
		return fmt.Errorf("delete workspace load test run samples: %w", err)
	}
	if _, err := tx.Exec(
		`DELETE FROM load_test_runs WHERE collection_id IN (SELECT id FROM collections WHERE workspace_id = ?)`, id,
	); err != nil {
		return fmt.Errorf("delete workspace load test runs: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM collections WHERE workspace_id = ?`, id); err != nil {
		return fmt.Errorf("delete workspace collections: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM environments WHERE workspace_id = ?`, id); err != nil {
		return fmt.Errorf("delete workspace environments: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete workspace: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// getWorkspaceLockHash returns the persisted lock credential type and
// bcrypt hash for id, or ("", "") if that workspace isn't locked. Internal
// to this package — every exported method involving the hash (below) stays
// entirely within this file, the same way internal/auth keeps its own
// bcrypt handling inside Manager rather than exposing hashes to its HTTP
// handler layer.
func (s *Store) getWorkspaceLockHash(id string) (credentialType, hash string, err error) {
	var t, h sql.NullString
	scanErr := s.db.QueryRow(`SELECT lock_credential_type, lock_password_hash FROM workspaces WHERE id = ?`, id).Scan(&t, &h)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if scanErr != nil {
		return "", "", fmt.Errorf("get workspace lock: %w", scanErr)
	}
	return t.String, h.String, nil
}

// IsWorkspaceLocked reports whether id currently has a lock set — the
// narrow check internal/web/api/mocks.go needs before allowing a
// modification to a mock mapped to this workspace, without needing
// anything else about the lock itself. Returns ErrNotFound if no such
// workspace exists.
func (s *Store) IsWorkspaceLocked(id string) (bool, error) {
	_, hash, err := s.getWorkspaceLockHash(id)
	if err != nil {
		return false, err
	}
	return hash != "", nil
}

// VerifyWorkspacePassword reports whether password matches id's current
// lock, returning ErrWorkspaceNotLocked if it has none and
// ErrIncorrectWorkspacePassword on a mismatch.
func (s *Store) VerifyWorkspacePassword(id, password string) error {
	credType, hash, err := s.getWorkspaceLockHash(id)
	if err != nil {
		return err
	}
	if hash == "" {
		return ErrWorkspaceNotLocked
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return ErrIncorrectWorkspacePassword
	}
	// A correct PIN of a not-yet-known length teaches it (locks made before
	// the length was recorded), so the unlock keypad can submit exactly when
	// the PIN is complete from then on.
	if credType == "pin" {
		_, _ = s.db.Exec(`UPDATE workspaces SET lock_pin_length = ? WHERE id = ? AND lock_pin_length = 0`, len(password), id)
	}
	return nil
}

// LockWorkspace sets or changes id's PIN/password to (credentialType,
// newPassword) — bcrypt-hashed here so the hash never has to leave this
// package. The caller (internal/web/api) is responsible for verifying the
// CURRENT password first via VerifyWorkspacePassword when one is already
// set — this method trusts it, the same way internal/auth's own
// Manager.SetPassword trusts its HTTP handler to have already validated
// format/authorization before calling it.
//
// Refuses to lock the Default workspace, mirroring ErrDefaultWorkspace's
// guard on DeleteWorkspace — it's the one workspace every mock without an
// explicit assignment falls back to, so locking it would risk locking
// everyone out of ungrouped mocks with no "Default" workspace left to fall
// back to for viewing them.
func (s *Store) LockWorkspace(id, credentialType, newPassword string) error {
	if id == DefaultWorkspaceID {
		return ErrDefaultWorkspaceLock
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash workspace password: %w", err)
	}
	pinLength := 0
	if credentialType == "pin" {
		pinLength = len(newPassword)
	}
	res, err := s.db.Exec(`UPDATE workspaces SET lock_credential_type = ?, lock_password_hash = ?, lock_pin_length = ? WHERE id = ?`, credentialType, string(hash), pinLength, id)
	if err != nil {
		return fmt.Errorf("set workspace lock: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearWorkspaceLock removes id's lock entirely (the caller has already
// verified the current password via VerifyWorkspacePassword). Returns
// ErrWorkspaceNotLocked if it wasn't locked to begin with, so a caller
// can't "successfully" remove a lock that never existed.
func (s *Store) ClearWorkspaceLock(id string) error {
	_, hash, err := s.getWorkspaceLockHash(id)
	if err != nil {
		return err
	}
	if hash == "" {
		return ErrWorkspaceNotLocked
	}
	res, err := s.db.Exec(`UPDATE workspaces SET lock_credential_type = '', lock_password_hash = '', lock_pin_length = 0 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("clear workspace lock: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Same modernc.org/sqlite timestamp pitfall as internal/mock/store.go and
// internal/certs/store.go: TEXT columns don't auto-scan into time.Time.
func formatTime(t time.Time) string         { return t.Format(time.RFC3339Nano) }
func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

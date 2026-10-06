package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAppliesMigrations(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var value string
	if err := db.QueryRow("SELECT value FROM schema_info WHERE key = 'created_at'").Scan(&value); err != nil {
		t.Fatalf("expected schema_info row after migration: %v", err)
	}
	if value == "" {
		t.Fatal("expected non-empty created_at value")
	}

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected db file to exist: %v", err)
	}
}

// TestOpenRestrictsFilePermissions guards a real gap: the DB holds
// plaintext secrets (certificate private keys, SMTP relay credentials,
// mock/collection auth tokens) but was left at the OS default umask
// (commonly world-readable 0644) instead of owner-only.
func TestOpenRestrictsFilePermissions(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat db file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected db file permissions 0600, got %o", perm)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	db1.Close()

	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open (re-running migrations against existing db): %v", err)
	}
	defer db2.Close()
}

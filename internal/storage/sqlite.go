package storage

import (
	"database/sql"
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (creating if necessary) the SQLite database at path, applies any
// pending migrations, and returns a ready-to-use *sql.DB.
func Open(path string) (*sql.DB, error) {
	// synchronous=NORMAL is the setting SQLite's own docs recommend once
	// WAL is on: still crash-safe (a WAL-mode commit can't corrupt the
	// database on a crash the way non-WAL FULL guards against), but skips
	// the extra fsync FULL does on every single commit — worth having now
	// that every protocol engine (REST/SOAP/GraphQL/TCP/SMTP/MQTT/WS) logs
	// a hit-log write per request/message through this one connection.
	// modernc.org/sqlite passes this whole string on to SQLite with
	// SQLITE_OPEN_URI set, so it's parsed as a URI, not a plain path — a
	// Windows path from filepath.Join (e.g. C:\Users\x\.airmock\airmock.db)
	// uses backslashes, which aren't valid URI path separators and break
	// that parsing. filepath.ToSlash is a no-op on every other OS.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", filepath.ToSlash(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // modernc.org/sqlite: single-writer, WAL still allows concurrent readers internally

	if err := migrateUp(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// The DB holds plaintext secrets (certificate private keys, SMTP relay
	// credentials, mock/collection auth tokens) — lock the file (and its
	// WAL/SHM sidecars, present once WAL mode's first write creates them) to
	// owner-only rather than leaving it at the OS default umask, which is
	// commonly world-readable (0644).
	restrictFilePermissions(path)
	return db, nil
}

// restrictFilePermissions chmods path and its WAL/SHM sidecars to 0600.
// Errors are logged, not returned — a permission-tightening step that fails
// (e.g. the data dir is on a filesystem that ignores POSIX permissions)
// shouldn't block startup entirely; the file already existed at whatever
// permissions the OS gave it before this call.
func restrictFilePermissions(path string) {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(p); err != nil {
			continue // sidecar files don't exist until the first WAL write
		}
		if err := os.Chmod(p, 0o600); err != nil {
			log.Printf("airmock: warning: failed to restrict permissions on %s: %v", p, err)
		}
	}
}

func migrateUp(db *sql.DB) error {
	srcDriver, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	dbDriver, err := sqlite.WithInstance(db, &sqlite.Config{})
	if err != nil {
		return err
	}
	m, err := migrate.NewWithInstance("iofs", srcDriver, "sqlite", dbDriver)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	return nil
}

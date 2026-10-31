// Package store implements persistence on SQLite.
//
// Two connection pools are used: a single-connection writer (SQLite allows
// one writer at a time; serialising in Go avoids SQLITE_BUSY storms) and a
// small read-only pool that benefits from WAL concurrent reads.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"lightmail/migrations"
)

var ErrNotFound = errors.New("not found")

type DB struct {
	W    *sql.DB
	R    *sql.DB
	Path string
}

const pragmas = "_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)"

// Open opens (and creates if needed) the database and applies migrations.
func Open(ctx context.Context, path string, readers int) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	dsn := filepath.ToSlash(path)
	w, err := sql.Open("sqlite", dsn+"?"+pragmas+"&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxIdleTime(0)
	if err := w.PingContext(ctx); err != nil {
		w.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	db := &DB{W: w, Path: path}
	if err := db.migrate(ctx); err != nil {
		w.Close()
		return nil, err
	}
	// Cheap statistics refresh recommended by SQLite for new connections.
	_, _ = w.ExecContext(ctx, "PRAGMA optimize=0x10002")
	if readers < 1 {
		readers = 1
	}
	r, err := sql.Open("sqlite", dsn+"?"+pragmas+"&_pragma=query_only(1)")
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(readers)
	r.SetMaxIdleConns(readers)
	r.SetConnMaxIdleTime(5 * time.Minute)
	db.R = r
	_ = os.Chmod(path, 0o600)
	return db, nil
}

func (db *DB) Close() error {
	var errs []error
	if db.R != nil {
		errs = append(errs, db.R.Close())
	}
	if db.W != nil {
		_, _ = db.W.Exec("PRAGMA optimize")
		_, _ = db.W.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		errs = append(errs, db.W.Close())
	}
	return errors.Join(errs...)
}

func (db *DB) Ping(ctx context.Context) error { return db.R.PingContext(ctx) }

// SchemaVersion returns the latest applied migration number.
func (db *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := db.R.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&v)
	return v, err
}

func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.W.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var version int
		if _, err := fmt.Sscanf(name, "%04d_", &version); err != nil {
			return fmt.Errorf("migration %s: bad name", name)
		}
		var exists int
		if err := db.W.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=?`, version).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return err
		}
		tx, err := db.W.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,?)`, version, name, time.Now().Unix()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Tx runs fn inside a write transaction.
func (db *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// GetMeta / SetMeta store small internal values (e.g. key check).
func (db *DB) GetMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := db.R.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

func (db *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := db.W.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// VacuumInto writes a consistent snapshot of the live database to dst.
func (db *DB) VacuumInto(ctx context.Context, dst string) error {
	_, err := db.W.ExecContext(ctx, `VACUUM INTO ?`, filepath.ToSlash(dst))
	return err
}

func now() int64 { return time.Now().Unix() }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// placeholders returns "?,?,?" for n values.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func int64Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, v := range ids {
		out[i] = v
	}
	return out
}

// Optimize refreshes planner statistics (SQLite recommends running it
// periodically on long-lived connections).
func (db *DB) Optimize(ctx context.Context) error {
	_, err := db.W.ExecContext(ctx, `PRAGMA optimize`)
	return err
}

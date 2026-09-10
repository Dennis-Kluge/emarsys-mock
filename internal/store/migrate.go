package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrate applies every embedded migration that has not run yet, in filename
// order. Migrations are numbered (0001_, 0002_, ...) and never edited once
// merged; a change goes into a new file.
func (db *DB) Migrate() error {
	if _, err := db.Write.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}

	applied := map[string]bool{}
	rows, err := db.Write.Query(`SELECT name FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		applied[n] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, name := range names {
		if applied[name] {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		tx, err := db.Write.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (name) VALUES (?)`, name); err != nil {
			tx.Rollback()
			return fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", name, err)
		}
	}
	return nil
}

// Reset drops every table and re-runs the migrations, returning the database to
// its seeded state. It backs POST /_ctl/reset, which tests call between cases,
// so it has to stay fast: on an in-memory database this is a few milliseconds.
func (db *DB) Reset() error {
	if err := db.dropAll(context.Background()); err != nil {
		return err
	}
	// Migrate needs the write pool's connection, so dropAll must have released
	// it by now -- the pool holds exactly one and would otherwise deadlock.
	return db.Migrate()
}

func (db *DB) dropAll(ctx context.Context) error {
	// Dropping tables in an arbitrary order trips the foreign keys that point
	// at already-dropped tables, and PRAGMA foreign_keys is a no-op inside a
	// transaction. So take one connection, turn the checks off on it, and drop
	// outside a transaction.
	conn, err := db.Write.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys: %w", err)
	}
	defer conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)

	// The table list has to come from this same connection: the write pool
	// holds exactly one, so asking the pool for a second would deadlock.
	tables, err := userTables(ctx, conn)
	if err != nil {
		return err
	}
	for _, t := range tables {
		if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS "`+t+`"`); err != nil {
			return fmt.Errorf("drop %s: %w", t, err)
		}
	}
	return nil
}

// querier is satisfied by *sql.DB, *sql.Conn and *sql.Tx alike.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func userTables(ctx context.Context, q querier) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

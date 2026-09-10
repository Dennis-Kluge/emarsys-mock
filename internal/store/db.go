// Package store owns the SQLite database: connection setup, migrations and the
// queries the handlers build on.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"runtime"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver: no CGO, so the binary cross-compiles
)

// DB wraps the two connection pools the service uses.
//
// SQLite serialises writers, so the write pool is pinned to a single connection
// and readers get their own pool. In WAL mode readers do not block on the
// writer, which keeps the dashboard responsive while a batch import runs.
type DB struct {
	Read   *sql.DB
	Write  *sql.DB
	dsn    string
	memory bool
}

// Open connects to dsn. The literal ":memory:" selects a private in-memory
// database, which is what tests and CI use.
//
// A plain ":memory:" DSN would hand every pooled connection its own empty
// database. The shared-cache form below plus a single pinned connection is what
// makes an in-memory database behave like a file one.
func Open(dsn string) (*DB, error) {
	memory := dsn == ":memory:" || dsn == ""
	var connStr string

	if memory {
		name, err := randomName()
		if err != nil {
			return nil, err
		}
		connStr = "file:" + name + "?mode=memory&cache=shared" +
			"&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	} else {
		connStr = "file:" + url.PathEscape(dsn) +
			"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	}

	write, err := sql.Open("sqlite", connStr)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	write.SetConnMaxLifetime(0)
	if err := write.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	db := &DB{Write: write, dsn: connStr, memory: memory}

	if memory {
		// The shared-cache database only lives as long as a connection to it
		// does, and that connection is the write pool's. Reusing it for reads
		// keeps the lifetime rule trivially correct.
		db.Read = write
		return db, nil
	}

	read, err := sql.Open("sqlite", connStr)
	if err != nil {
		return nil, fmt.Errorf("open sqlite (read): %w", err)
	}
	readers := runtime.NumCPU()
	if readers < 4 {
		readers = 4
	}
	read.SetMaxOpenConns(readers)
	read.SetMaxIdleConns(readers)
	db.Read = read

	return db, nil
}

func (db *DB) Close() error {
	var errs []string
	if db.Read != nil && db.Read != db.Write {
		if err := db.Read.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if db.Write != nil {
		if err := db.Write.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("close: %s", strings.Join(errs, "; "))
	}
	return nil
}

func randomName() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate database name: %w", err)
	}
	return "emarsysmock-" + hex.EncodeToString(buf), nil
}

package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return db
}

func TestMigrateSeedsSystemFields(t *testing.T) {
	db := newTestDB(t)

	// Integrations hardcode these ids, so the seed must place them exactly.
	want := map[int]string{
		1:  "First Name",
		2:  "Last Name",
		3:  "E-Mail",
		4:  "Date of birth",
		31: "Opt-in",
	}
	for id, name := range want {
		var got string
		var isSystem int
		err := db.Read.QueryRow(`SELECT name, is_system FROM fields WHERE id = ?`, id).Scan(&got, &isSystem)
		if err != nil {
			t.Fatalf("field %d: %v", id, err)
		}
		if got != name {
			t.Errorf("field %d name = %q, want %q", id, got, name)
		}
		if isSystem != 1 {
			t.Errorf("field %d is_system = %d, want 1", id, isSystem)
		}
	}

	// contact/query only works on indexed columns; e-mail has to be one.
	var indexed int
	if err := db.Read.QueryRow(`SELECT is_indexed FROM fields WHERE id = 3`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != 1 {
		t.Error("field 3 (e-mail) must be indexed")
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	if err := db.Migrate(); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	var fields int
	if err := db.Read.QueryRow(`SELECT COUNT(*) FROM fields`).Scan(&fields); err != nil {
		t.Fatal(err)
	}
	if fields != 9 {
		t.Errorf("field count = %d after re-running migrations, want 9", fields)
	}
}

func TestInMemoryDatabasesAreIsolated(t *testing.T) {
	// A plain ":memory:" DSN would give every pool its own database and every
	// Open its own too. The shared-cache naming has to keep instances apart
	// while keeping a single instance's connections together.
	a, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := a.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Read.Exec(`SELECT COUNT(*) FROM fields`); err == nil {
		t.Fatal("second in-memory database sees the first one's tables")
	}
}

func TestResetRestoresSeedState(t *testing.T) {
	db := newTestDB(t)

	if _, err := db.Write.Exec(
		`INSERT INTO contacts (uid, created_at, updated_at) VALUES ('u1','now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Write.Exec(`DELETE FROM fields WHERE id = 31`); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if err := db.Reset(); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	elapsed := time.Since(start)

	var contacts, optIn int
	if err := db.Read.QueryRow(`SELECT COUNT(*) FROM contacts`).Scan(&contacts); err != nil {
		t.Fatal(err)
	}
	if err := db.Read.QueryRow(`SELECT COUNT(*) FROM fields WHERE id = 31`).Scan(&optIn); err != nil {
		t.Fatal(err)
	}
	if contacts != 0 {
		t.Errorf("contacts after reset = %d, want 0", contacts)
	}
	if optIn != 1 {
		t.Error("opt-in field was not restored by reset")
	}

	if elapsed > resetBudget {
		t.Errorf("Reset() took %s, want under %s", elapsed, resetBudget)
	}
}

func TestFileDatabaseServesConcurrentReadsAndWrites(t *testing.T) {
	// WAL plus a single-connection write pool is what keeps this from
	// deadlocking or returning "database is locked".
	db, err := Open(filepath.Join(t.TempDir(), "mock.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := db.Write.Exec(
				`INSERT INTO contacts (uid, created_at, updated_at) VALUES (?, 'now', 'now')`,
				"uid-"+string(rune('a'+i)))
			errs <- err
		}()
		go func() {
			defer wg.Done()
			var n int
			errs <- db.Read.QueryRow(`SELECT COUNT(*) FROM contacts`).Scan(&n)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent access: %v", err)
		}
	}
}

func TestRequestLogTrimsToMax(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	const max = 50
	for i := range 300 {
		code := 0
		err := db.AppendRequestLog(ctx, RequestLogEntry{
			TS:         time.Now(),
			Method:     "POST",
			Path:       "/api/v2/contact",
			HTTPStatus: 200,
			ReplyCode:  &code,
			DurationMS: int64(i),
		}, max)
		if err != nil {
			t.Fatalf("AppendRequestLog(%d) error = %v", i, err)
		}
	}

	var n int
	if err := db.Read.QueryRow(`SELECT COUNT(*) FROM request_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	// Trimming runs every 128th insert, so the table sits at or just above max
	// rather than exactly on it.
	if n > max+128 {
		t.Errorf("request_log holds %d rows, want at most %d", n, max+128)
	}
	if n == 300 {
		t.Error("request_log was never trimmed")
	}
}

func TestAPIUserLookup(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	byName, err := db.APIUserByUsername(ctx, "mock-api-user")
	if err != nil {
		t.Fatalf("APIUserByUsername() error = %v", err)
	}
	if byName.Secret != "mock-secret" || len(byName.Permissions) != 1 || byName.Permissions[0] != "*" {
		t.Errorf("seeded user = %+v", byName)
	}

	byClient, err := db.APIUserByClientID(ctx, "mock-client")
	if err != nil {
		t.Fatalf("APIUserByClientID() error = %v", err)
	}
	if byClient.ID != byName.ID {
		t.Error("username and client id lookups returned different users")
	}

	if _, err := db.APIUserByUsername(ctx, "nobody"); err != ErrNoAPIUser {
		t.Errorf("unknown user error = %v, want ErrNoAPIUser", err)
	}
}

func TestRememberNonce(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now()

	fresh, err := db.RememberNonce(ctx, "abc", now)
	if err != nil || !fresh {
		t.Fatalf("first RememberNonce() = %v, %v; want true, nil", fresh, err)
	}
	fresh, err = db.RememberNonce(ctx, "abc", now)
	if err != nil || fresh {
		t.Fatalf("second RememberNonce() = %v, %v; want false, nil", fresh, err)
	}
}

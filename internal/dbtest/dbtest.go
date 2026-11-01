// Package dbtest gives tests a throwaway Postgres schema so they never touch dev data.
package dbtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"

	"littleworld/internal/db"
)

var (
	once    sync.Once
	created bool // shared schema exists and must be dropped by Main
	err     error
)

// Main runs a package's tests and then drops the shared schema. Every test package
// using Migrated must have: func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }
func Main(m *testing.M) int {
	code := m.Run()
	if created {
		if base, baseErr := sql.Open("pgx", os.Getenv("DATABASE_URL")); baseErr == nil {
			if _, dropErr := base.Exec("DROP SCHEMA " + sharedSchema + " CASCADE"); dropErr != nil {
				fmt.Fprintln(os.Stderr, "dbtest: drop shared schema:", dropErr)
			}
			base.Close()
		}
	}
	return code
}

// Per-process name: `go test ./...` runs packages in parallel against one database.
var sharedSchema = fmt.Sprintf("mtest_shared_%d", os.Getpid())

// Pool is an empty schema private to one test; it is dropped when the test ends.
func Pool(t testing.TB) *sql.DB {
	t.Helper()
	dsn := requireDSN(t)
	ctx := context.Background()

	base, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open base database: %v", err)
	}

	schema := "mtest_" + randomSuffix(t)
	if _, err := base.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		base.Close()
		t.Fatalf("create schema: %v", err)
	}

	pool, err := openInSchema(t, dsn, schema)
	if err != nil {
		base.Close()
		t.Fatalf("open scoped database: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		if _, err := base.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Logf("drop schema %s: %v", schema, err)
		}
		base.Close()
	})
	return pool
}

// Migrated is a migrated schema shared by the whole test binary. Tables are
// truncated before each test, which is far cheaper than migrating per test.
func Migrated(t testing.TB) *sql.DB {
	t.Helper()
	dsn := requireDSN(t)

	once.Do(func() { err = migrateShared(dsn) })
	if err != nil {
		t.Fatalf("prepare shared schema: %v", err)
	}

	pool, openErr := openInSchema(t, dsn, sharedSchema)
	if openErr != nil {
		t.Fatalf("open shared schema: %v", openErr)
	}
	t.Cleanup(func() { pool.Close() })

	if _, truncErr := pool.ExecContext(t.Context(),
		`TRUNCATE users, tags, books, bundles, orders RESTART IDENTITY CASCADE`); truncErr != nil {
		t.Fatalf("truncate: %v", truncErr)
	}
	return pool
}

func migrateShared(dsn string) error {
	ctx := context.Background()

	base, openErr := db.Open(ctx, dsn)
	if openErr != nil {
		return openErr
	}
	defer base.Close()

	if _, execErr := base.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+sharedSchema+" CASCADE"); execErr != nil {
		return fmt.Errorf("reset shared schema: %w", execErr)
	}
	if _, execErr := base.ExecContext(ctx, "CREATE SCHEMA "+sharedSchema); execErr != nil {
		return fmt.Errorf("create shared schema: %w", execErr)
	}

	pool, openErr := db.Open(ctx, withSearchPath(dsn, sharedSchema))
	if openErr != nil {
		return openErr
	}
	defer pool.Close()

	if upErr := db.Up(ctx, pool); upErr != nil {
		return fmt.Errorf("migrate shared schema: %w", upErr)
	}

	created = true
	return nil
}

// openInSchema opens a pool whose default search_path is the given schema.
func openInSchema(t testing.TB, dsn, schema string) (*sql.DB, error) {
	t.Helper()
	return db.Open(context.Background(), withSearchPath(dsn, schema))
}

func withSearchPath(dsn, schema string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func requireDSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set; skipping database tests")
	}
	return dsn
}

func randomSuffix(t testing.TB) string {
	t.Helper()
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("random schema suffix: %v", err)
	}
	return hex.EncodeToString(raw)
}

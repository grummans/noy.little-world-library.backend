package db_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"littleworld/internal/db"
	"littleworld/internal/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }
func TestMigrationsUpDown(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := t.Context()

	if err := db.Up(ctx, pool); err != nil {
		t.Fatalf("up: %v", err)
	}
	for _, tbl := range []string{"users", "sessions", "books", "tags", "book_tags",
		"bundles", "bundle_books", "bundle_tags", "orders", "activities"} {
		if !tableExists(t, pool, tbl) {
			t.Errorf("table %s missing after up", tbl)
		}
	}

	if err := db.Reset(ctx, pool); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if tableExists(t, pool, "orders") {
		t.Error("orders still exists after reset")
	}

	// Down must leave the schema reusable for the next deploy.
	if err := db.Up(ctx, pool); err != nil {
		t.Fatalf("second up: %v", err)
	}
}

func TestConstraints(t *testing.T) {
	pool := dbtest.Migrated(t)
	ctx := t.Context()

	mustExec(t, pool, `INSERT INTO users (display_name, email, password_hash, role) VALUES ('Test User', 'teacher@example.com', 'x', 'teacher')`)
	mustExec(t, pool, `INSERT INTO users (display_name, email, password_hash, role) VALUES ('Test User', 'parent@example.com', 'x', 'parent')`)
	mustExec(t, pool, `INSERT INTO users (display_name, email, password_hash, role) VALUES ('Test User', 'cascade@example.com', 'x', 'parent')`)

	if err := exec(ctx, pool, `INSERT INTO users (display_name, email, password_hash, role) VALUES ('Test User', 'TEACHER@example.com', 'x', 'teacher')`); err == nil {
		t.Error("duplicate email differing only in case was accepted")
	}
	if err := exec(ctx, pool, `INSERT INTO users (email, password_hash, role) VALUES ('third@example.com', 'x', 'admin')`); err == nil {
		t.Error("invalid role was accepted")
	}

	mustExec(t, pool, `INSERT INTO books (owner_id, slug, title, access_type) VALUES (1, 'free-book', 'Free', 'free')`)
	mustExec(t, pool, `INSERT INTO books (owner_id, slug, title, access_type, price_vnd) VALUES (1, 'paid-book', 'Paid', 'paid', 99000)`)

	if err := exec(ctx, pool, `INSERT INTO books (owner_id, slug, title, access_type, price_vnd) VALUES (1, 'free-with-price', 'Bad', 'free', 1000)`); err == nil {
		t.Error("free book with a price was accepted")
	}
	if err := exec(ctx, pool, `INSERT INTO books (owner_id, slug, title, access_type) VALUES (1, 'paid-no-price', 'Bad', 'paid')`); err == nil {
		t.Error("paid book without a price was accepted")
	}
	if err := exec(ctx, pool, `INSERT INTO books (owner_id, slug, title, access_type, price_vnd) VALUES (1, 'paid-zero', 'Bad', 'paid', 0)`); err == nil {
		t.Error("paid book with zero price was accepted")
	}

	mustExec(t, pool, `INSERT INTO bundles (owner_id, slug, title, price_vnd) VALUES (1, 'bundle', 'Bundle', 199000)`)

	if err := exec(ctx, pool, `INSERT INTO orders (buyer_id, amount_vnd, vnpay_txn_ref) VALUES (2, 1000, 'ref-none')`); err == nil {
		t.Error("order without book or bundle was accepted")
	}
	if err := exec(ctx, pool, `INSERT INTO orders (buyer_id, book_id, bundle_id, amount_vnd, vnpay_txn_ref) VALUES (2, 1, 1, 1000, 'ref-both')`); err == nil {
		t.Error("order with both book and bundle was accepted")
	}
	mustExec(t, pool, `INSERT INTO orders (buyer_id, book_id, amount_vnd, vnpay_txn_ref) VALUES (2, 2, 99000, 'ref-1')`)
	if err := exec(ctx, pool, `INSERT INTO orders (buyer_id, book_id, amount_vnd, vnpay_txn_ref) VALUES (2, 1, 99000, 'ref-1')`); err == nil {
		t.Error("duplicate vnpay_txn_ref was accepted")
	}

	mustExec(t, pool, `INSERT INTO bundle_books (bundle_id, book_id, position) VALUES (1, 2, 1)`)
	if err := exec(ctx, pool, `INSERT INTO bundle_books (bundle_id, book_id, position) VALUES (1, 1, 1)`); err == nil {
		t.Error("duplicate bundle position was accepted")
	}

	mustExec(t, pool, `INSERT INTO sessions (user_id, token_hash, expires_at) VALUES (3, 'hash', now() + interval '1 day')`)
	mustExec(t, pool, `DELETE FROM users WHERE id = 3`)
	var sessions int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != 0 {
		t.Errorf("sessions not cascaded on user delete: %d left", sessions)
	}
}

func tableExists(t *testing.T, pool *sql.DB, name string) bool {
	t.Helper()
	var found bool
	err := pool.QueryRowContext(t.Context(),
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = $1
		)`, name).Scan(&found)
	if err != nil {
		t.Fatalf("check table %s: %v", name, err)
	}
	return found
}

func exec(ctx context.Context, pool *sql.DB, query string) error {
	_, err := pool.ExecContext(ctx, query)
	return err
}

func mustExec(t *testing.T, pool *sql.DB, query string) {
	t.Helper()
	if err := exec(t.Context(), pool, query); err != nil {
		t.Fatalf("exec %s: %v", strings.TrimSpace(query), err)
	}
}

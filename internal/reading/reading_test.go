package reading_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"

	"littleworld/internal/dbtest"
	"littleworld/internal/reading"
	"littleworld/internal/storagetest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type fixture struct {
	svc   *reading.Service
	db    *sql.DB
	store *storagetest.Mem
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := dbtest.Migrated(t)
	store := storagetest.New()
	return fixture{svc: reading.New(pool, store), db: pool, store: store}
}

func insertUser(t *testing.T, db *sql.DB, email, role string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(t.Context(),
		`INSERT INTO users (email, password_hash, role) VALUES ($1, 'x', $2) RETURNING id`,
		email, role).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func insertBook(t *testing.T, db *sql.DB, ownerID int64, slug, access string, price *int64, status string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(t.Context(),
		`INSERT INTO books (owner_id, slug, title, description, cover_object_key, epub_object_key,
			access_type, price_vnd, status, published_at)
		 VALUES ($1, $2, 'Sách', 'Mô tả', 'books/1/cover.png', 'books/1/book.epub', $3, $4,
		         $5, CASE WHEN $5 = 'published' THEN now() ELSE NULL END)
		 RETURNING id`,
		ownerID, slug, access, price, status).Scan(&id); err != nil {
		t.Fatalf("insert book: %v", err)
	}
	return id
}

var orderSeq int

func insertOrder(t *testing.T, db *sql.DB, buyerID int64, bookID, bundleID *int64, status string) {
	t.Helper()
	orderSeq++
	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO orders (buyer_id, book_id, bundle_id, amount_vnd, status, vnpay_txn_ref, paid_at)
		 VALUES ($1, $2, $3, 10000, $4, $5, CASE WHEN $4 = 'paid' THEN now() ELSE NULL END)`,
		buyerID, bookID, bundleID, status, fmt.Sprintf("ref-%d", orderSeq)); err != nil {
		t.Fatalf("insert order: %v", err)
	}
}

func price(v int64) *int64 { return &v }

func TestFreeBookIsPubliclyReadable(t *testing.T) {
	f := newFixture(t)
	teacher := insertUser(t, f.db, "teacher@example.com", "teacher")
	insertBook(t, f.db, teacher, "mien-phi", "free", nil, "published")

	session, err := f.svc.Open(context.Background(), 0, "mien-phi")
	if err != nil {
		t.Fatalf("open free book: %v", err)
	}
	if session.Source != "free" || session.URL == "" || session.ExpiresIn <= 0 {
		t.Errorf("session = %+v", session)
	}
}

func TestPaidBookRequiresPurchase(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com", "teacher")
	parent := insertUser(t, f.db, "parent@example.com", "parent")
	otherParent := insertUser(t, f.db, "other@example.com", "parent")
	bookID := insertBook(t, f.db, teacher, "tra-phi", "paid", price(99000), "published")

	if _, err := f.svc.Open(ctx, 0, "tra-phi"); !errors.Is(err, reading.ErrForbidden) {
		t.Errorf("anonymous: err = %v, want ErrForbidden", err)
	}
	if _, err := f.svc.Open(ctx, otherParent, "tra-phi"); !errors.Is(err, reading.ErrForbidden) {
		t.Errorf("unrelated parent: err = %v, want ErrForbidden", err)
	}

	// A pending order grants nothing: only a verified paid order does.
	insertOrder(t, f.db, parent, &bookID, nil, "pending")
	if _, err := f.svc.Open(ctx, parent, "tra-phi"); !errors.Is(err, reading.ErrForbidden) {
		t.Errorf("pending order: err = %v, want ErrForbidden", err)
	}

	insertOrder(t, f.db, parent, &bookID, nil, "paid")
	session, err := f.svc.Open(ctx, parent, "tra-phi")
	if err != nil {
		t.Fatalf("paid order: %v", err)
	}
	if session.Source != "purchase" {
		t.Errorf("source = %q, want purchase", session.Source)
	}
}

func TestBundlePurchaseGrantsItsBooksOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com", "teacher")
	parent := insertUser(t, f.db, "parent@example.com", "parent")

	inBundle := insertBook(t, f.db, teacher, "trong-goi", "paid", price(10000), "published")
	insertBook(t, f.db, teacher, "ngoai-goi", "paid", price(10000), "published")

	var bundleID int64
	if err := f.db.QueryRowContext(ctx,
		`INSERT INTO bundles (owner_id, slug, title, description, price_vnd, status, published_at)
		 VALUES ($1, 'goi', 'Gói', 'Mô tả', 15000, 'published', now()) RETURNING id`,
		teacher).Scan(&bundleID); err != nil {
		t.Fatalf("insert bundle: %v", err)
	}
	if _, err := f.db.ExecContext(ctx,
		`INSERT INTO bundle_books (bundle_id, book_id, position) VALUES ($1, $2, 1)`,
		bundleID, inBundle); err != nil {
		t.Fatalf("insert bundle book: %v", err)
	}

	insertOrder(t, f.db, parent, nil, &bundleID, "paid")

	session, err := f.svc.Open(ctx, parent, "trong-goi")
	if err != nil {
		t.Fatalf("book inside purchased bundle: %v", err)
	}
	if session.Source != "bundle" {
		t.Errorf("source = %q, want bundle", session.Source)
	}
	if _, err := f.svc.Open(ctx, parent, "ngoai-goi"); !errors.Is(err, reading.ErrForbidden) {
		t.Errorf("book outside bundle: err = %v, want ErrForbidden", err)
	}
}

func TestDraftBookIsNotReadable(t *testing.T) {
	f := newFixture(t)
	teacher := insertUser(t, f.db, "teacher@example.com", "teacher")
	insertBook(t, f.db, teacher, "con-nhap", "free", nil, "draft")

	if _, err := f.svc.Open(context.Background(), 0, "con-nhap"); !errors.Is(err, reading.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// A paid order keeps working after the seller unpublishes the bundle: readers do not
// lose a book they bought. Selling it again is a checkout rule, not a reading rule.
func TestUnpublishedBundleKeepsPurchaseAccess(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com", "teacher")
	parent := insertUser(t, f.db, "parent@example.com", "parent")

	bookID := insertBook(t, f.db, teacher, "trong-goi", "paid", price(10000), "published")

	var bundleID int64
	if err := f.db.QueryRowContext(ctx,
		`INSERT INTO bundles (owner_id, slug, title, description, price_vnd, status, published_at)
		 VALUES ($1, 'goi', 'Gói', 'Mô tả', 15000, 'published', now()) RETURNING id`,
		teacher).Scan(&bundleID); err != nil {
		t.Fatalf("insert bundle: %v", err)
	}
	if _, err := f.db.ExecContext(ctx,
		`INSERT INTO bundle_books (bundle_id, book_id, position) VALUES ($1, $2, 1)`,
		bundleID, bookID); err != nil {
		t.Fatalf("insert bundle book: %v", err)
	}
	insertOrder(t, f.db, parent, nil, &bundleID, "paid")

	if _, err := f.db.ExecContext(ctx,
		`UPDATE bundles SET status = 'draft', published_at = NULL WHERE id = $1`, bundleID); err != nil {
		t.Fatalf("unpublish bundle: %v", err)
	}

	if _, err := f.svc.Open(ctx, parent, "trong-goi"); err != nil {
		t.Errorf("purchase lost after unpublish: %v", err)
	}
}

// Only a verified paid order grants access.
func TestOnlyPaidOrdersGrantAccess(t *testing.T) {
	for _, status := range []string{"pending", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			teacher := insertUser(t, f.db, "teacher@example.com", "teacher")
			parent := insertUser(t, f.db, "parent@example.com", "parent")
			bookID := insertBook(t, f.db, teacher, "tra-phi", "paid", price(1000), "published")

			insertOrder(t, f.db, parent, &bookID, nil, status)
			if _, err := f.svc.Open(ctx, parent, "tra-phi"); !errors.Is(err, reading.ErrForbidden) {
				t.Errorf("order %q: err = %v, want ErrForbidden", status, err)
			}
		})
	}
}

// An unpublished book stays unreadable even for its buyer.
func TestUnpublishedBookIsNotReadable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com", "teacher")
	parent := insertUser(t, f.db, "parent@example.com", "parent")
	bookID := insertBook(t, f.db, teacher, "tra-phi", "paid", price(1000), "published")
	insertOrder(t, f.db, parent, &bookID, nil, "paid")

	if _, err := f.db.ExecContext(ctx,
		`UPDATE books SET status = 'draft', published_at = NULL WHERE id = $1`, bookID); err != nil {
		t.Fatalf("unpublish book: %v", err)
	}

	if _, err := f.svc.Open(ctx, parent, "tra-phi"); !errors.Is(err, reading.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

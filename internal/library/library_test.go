package library_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"littleworld/internal/catalog"
	"littleworld/internal/dbtest"
	"littleworld/internal/library"
	"littleworld/internal/storagetest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type fixture struct {
	svc   *library.Service
	db    *sql.DB
	store *storagetest.Mem
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := dbtest.Migrated(t)
	store := storagetest.New()
	return fixture{svc: library.New(pool, catalog.New(pool, store)), db: pool, store: store}
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

func insertBook(t *testing.T, db *sql.DB, ownerID int64, slug, access, status string) int64 {
	t.Helper()
	var id int64
	var price *int64
	if access == "paid" {
		value := int64(10000)
		price = &value
	}
	if err := db.QueryRowContext(t.Context(),
		`INSERT INTO books (owner_id, slug, title, description, cover_object_key, epub_object_key,
			access_type, price_vnd, status, published_at)
		 VALUES ($1, $2, $3, 'Mô tả', $4, $5, $6, $7, $8,
		         CASE WHEN $8 = 'published' THEN now() ELSE NULL END)
		 RETURNING id`,
		ownerID, slug, "Sách "+slug, "books/"+slug+"/cover.png",
		"books/"+slug+"/book.epub", access, price, status).Scan(&id); err != nil {
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
		buyerID, bookID, bundleID, status, "txn-"+itoa(orderSeq)); err != nil {
		t.Fatalf("insert order: %v", err)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

func TestShelfShowsOnlyPaidPublishedItems(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com", "teacher")
	parent := insertUser(t, f.db, "parent@example.com", "parent")

	bought := insertBook(t, f.db, teacher, "da-mua", "paid", "published")
	unpublished := insertBook(t, f.db, teacher, "da-an", "paid", "draft")
	pending := insertBook(t, f.db, teacher, "chua-tra", "paid", "published")

	insertOrder(t, f.db, parent, &bought, nil, "paid")
	insertOrder(t, f.db, parent, &unpublished, nil, "paid")
	insertOrder(t, f.db, parent, &pending, nil, "pending")

	shelf, err := f.svc.Load(ctx, parent)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(shelf.Books) != 1 || shelf.Books[0].Slug != "da-mua" {
		t.Fatalf("books = %+v", shelf.Books)
	}
	if len(shelf.Readable) != 1 || shelf.Readable[0].Origin.Type != "purchase" {
		t.Errorf("readable = %+v", shelf.Readable)
	}
}

func TestShelfKeepsBundleAsProductAndLabelsItsBooks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com", "teacher")
	parent := insertUser(t, f.db, "parent@example.com", "parent")

	first := insertBook(t, f.db, teacher, "sach-mot", "paid", "published")
	second := insertBook(t, f.db, teacher, "sach-hai", "paid", "published")
	direct := insertBook(t, f.db, teacher, "mua-le", "paid", "published")

	var bundleID int64
	if err := f.db.QueryRowContext(ctx,
		`INSERT INTO bundles (owner_id, slug, title, description, cover_object_key, price_vnd, status, published_at)
		 VALUES ($1, 'goi-hai-sach', 'Gói hai sách', 'Mô tả', 'bundles/1/cover.png', 25000, 'published', now())
		 RETURNING id`, teacher).Scan(&bundleID); err != nil {
		t.Fatalf("insert bundle: %v", err)
	}
	for i, bookID := range []int64{first, second} {
		if _, err := f.db.ExecContext(ctx,
			`INSERT INTO bundle_books (bundle_id, book_id, position) VALUES ($1, $2, $3)`,
			bundleID, bookID, i+1); err != nil {
			t.Fatalf("insert bundle book: %v", err)
		}
	}

	insertOrder(t, f.db, parent, nil, &bundleID, "paid")
	insertOrder(t, f.db, parent, &direct, nil, "paid")

	shelf, err := f.svc.Load(ctx, parent)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if len(shelf.Bundles) != 1 || shelf.Bundles[0].Bundle.Slug != "goi-hai-sach" {
		t.Fatalf("bundles = %+v", shelf.Bundles)
	}
	if len(shelf.Bundles[0].Books) != 2 {
		t.Errorf("bundle books = %d, want 2", len(shelf.Bundles[0].Books))
	}
	if shelf.Bundles[0].PurchasedAt.IsZero() {
		t.Error("purchase time missing")
	}
	if shelf.Bundles[0].Bundle.CoverURL == "" {
		t.Error("bundle cover url missing")
	}

	// Only the individually bought book is listed under books.
	if len(shelf.Books) != 1 || shelf.Books[0].Slug != "mua-le" {
		t.Errorf("books = %+v", shelf.Books)
	}

	// The readable list has the direct purchase first, then bundle books labelled with
	// their bundle, with no duplicates.
	if len(shelf.Readable) != 3 {
		t.Fatalf("continue = %d items, want 3", len(shelf.Readable))
	}
	if shelf.Readable[0].Origin.Type != "purchase" || shelf.Readable[0].Book.Slug != "mua-le" {
		t.Errorf("first continue item = %+v", shelf.Readable[0])
	}
	bundleLabelled := 0
	for _, item := range shelf.Readable[1:] {
		if item.Origin.Type != "bundle" || item.Origin.BundleSlug != "goi-hai-sach" ||
			item.Origin.BundleName != "Gói hai sách" {
			t.Errorf("bundle origin = %+v", item.Origin)
		}
		bundleLabelled++
	}
	if bundleLabelled != 2 {
		t.Errorf("bundle-labelled books = %d, want 2", bundleLabelled)
	}
}

// A book bought twice (once alone, once in a bundle) appears once, as a purchase.
func TestShelfDeduplicatesBooks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com", "teacher")
	parent := insertUser(t, f.db, "parent@example.com", "parent")

	bookID := insertBook(t, f.db, teacher, "sach", "paid", "published")
	var bundleID int64
	if err := f.db.QueryRowContext(ctx,
		`INSERT INTO bundles (owner_id, slug, title, description, price_vnd, status, published_at)
		 VALUES ($1, 'goi', 'Gói', 'Mô tả', 20000, 'published', now()) RETURNING id`,
		teacher).Scan(&bundleID); err != nil {
		t.Fatalf("insert bundle: %v", err)
	}
	if _, err := f.db.ExecContext(ctx,
		`INSERT INTO bundle_books (bundle_id, book_id, position) VALUES ($1, $2, 1)`,
		bundleID, bookID); err != nil {
		t.Fatalf("insert bundle book: %v", err)
	}

	insertOrder(t, f.db, parent, &bookID, nil, "paid")
	insertOrder(t, f.db, parent, nil, &bundleID, "paid")

	shelf, err := f.svc.Load(ctx, parent)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(shelf.Readable) != 1 {
		t.Fatalf("continue = %d items, want 1", len(shelf.Readable))
	}
	if shelf.Readable[0].Origin.Type != "purchase" {
		t.Errorf("origin = %+v, want purchase to win", shelf.Readable[0].Origin)
	}
	if len(shelf.Bundles) != 1 || len(shelf.Bundles[0].Books) != 1 {
		t.Errorf("bundle product must still show its book: %+v", shelf.Bundles)
	}
}

func TestEmptyShelf(t *testing.T) {
	f := newFixture(t)
	parent := insertUser(t, f.db, "parent@example.com", "parent")

	shelf, err := f.svc.Load(context.Background(), parent)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if shelf.Bundles == nil || shelf.Books == nil || shelf.Readable == nil {
		t.Error("empty shelf must serialise as empty arrays, not null")
	}
	if len(shelf.Bundles)+len(shelf.Books)+len(shelf.Readable) != 0 {
		t.Errorf("shelf not empty: %+v", shelf)
	}
}

// Buying the same product twice must not duplicate anything on the shelf.
func TestDuplicatePaidOrdersAreCollapsed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com", "teacher")
	parent := insertUser(t, f.db, "parent@example.com", "parent")

	bookID := insertBook(t, f.db, teacher, "sach", "paid", "published")
	var bundleID int64
	if err := f.db.QueryRowContext(ctx,
		`INSERT INTO bundles (owner_id, slug, title, description, price_vnd, status, published_at)
		 VALUES ($1, 'goi', 'Gói', 'Mô tả', 20000, 'published', now()) RETURNING id`,
		teacher).Scan(&bundleID); err != nil {
		t.Fatalf("insert bundle: %v", err)
	}
	if _, err := f.db.ExecContext(ctx,
		`INSERT INTO bundle_books (bundle_id, book_id, position) VALUES ($1, $2, 1)`,
		bundleID, bookID); err != nil {
		t.Fatalf("insert bundle book: %v", err)
	}

	insertOrder(t, f.db, parent, &bookID, nil, "paid")
	insertOrder(t, f.db, parent, &bookID, nil, "paid")
	insertOrder(t, f.db, parent, nil, &bundleID, "paid")
	insertOrder(t, f.db, parent, nil, &bundleID, "paid")

	shelf, err := f.svc.Load(ctx, parent)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(shelf.Books) != 1 {
		t.Errorf("books = %d, want 1", len(shelf.Books))
	}
	if len(shelf.Bundles) != 1 {
		t.Errorf("bundles = %d, want 1", len(shelf.Bundles))
	}
	if len(shelf.Readable) != 1 {
		t.Errorf("continue = %d, want 1", len(shelf.Readable))
	}
}

// Another parent's purchases must never appear.
func TestShelfIsPerBuyer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com", "teacher")
	mine := insertUser(t, f.db, "mine@example.com", "parent")
	theirs := insertUser(t, f.db, "theirs@example.com", "parent")

	myBook := insertBook(t, f.db, teacher, "cua-toi", "paid", "published")
	theirBook := insertBook(t, f.db, teacher, "cua-ho", "paid", "published")
	insertOrder(t, f.db, mine, &myBook, nil, "paid")
	insertOrder(t, f.db, theirs, &theirBook, nil, "paid")

	shelf, err := f.svc.Load(ctx, mine)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(shelf.Books) != 1 || shelf.Books[0].Slug != "cua-toi" {
		t.Errorf("shelf = %+v, want only my book", shelf.Books)
	}
}

// A paid bundle whose book was later unpublished keeps the product but hides the book.
func TestPaidBundleHidesUnpublishedBook(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com", "teacher")
	parent := insertUser(t, f.db, "parent@example.com", "parent")

	published := insertBook(t, f.db, teacher, "con-hien", "paid", "published")
	hidden := insertBook(t, f.db, teacher, "da-an", "paid", "draft")

	var bundleID int64
	if err := f.db.QueryRowContext(ctx,
		`INSERT INTO bundles (owner_id, slug, title, description, price_vnd, status, published_at)
		 VALUES ($1, 'goi', 'Gói', 'Mô tả', 20000, 'published', now()) RETURNING id`,
		teacher).Scan(&bundleID); err != nil {
		t.Fatalf("insert bundle: %v", err)
	}
	for i, bookID := range []int64{published, hidden} {
		if _, err := f.db.ExecContext(ctx,
			`INSERT INTO bundle_books (bundle_id, book_id, position) VALUES ($1, $2, $3)`,
			bundleID, bookID, i+1); err != nil {
			t.Fatalf("insert bundle book: %v", err)
		}
	}
	insertOrder(t, f.db, parent, nil, &bundleID, "paid")

	shelf, err := f.svc.Load(ctx, parent)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(shelf.Bundles) != 1 {
		t.Fatalf("bundle product missing: %+v", shelf.Bundles)
	}
	if len(shelf.Bundles[0].Books) != 1 || shelf.Bundles[0].Books[0].Slug != "con-hien" {
		t.Errorf("bundle books = %+v, want only the published one", shelf.Bundles[0].Books)
	}
	if len(shelf.Readable) != 1 {
		t.Errorf("continue = %d, want 1", len(shelf.Readable))
	}
}

// Cards must carry the same fields as the storefront, not zero values.
func TestShelfCardsAreComplete(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com", "teacher")
	parent := insertUser(t, f.db, "parent@example.com", "parent")

	bookID := insertBook(t, f.db, teacher, "sach", "paid", "published")
	var tagID int64
	if err := f.db.QueryRowContext(ctx,
		`INSERT INTO tags (slug, name, created_by) VALUES ('dong-vat', 'Động vật', $1) RETURNING id`,
		teacher).Scan(&tagID); err != nil {
		t.Fatalf("insert tag: %v", err)
	}
	if _, err := f.db.ExecContext(ctx,
		`INSERT INTO book_tags (book_id, tag_id) VALUES ($1, $2)`, bookID, tagID); err != nil {
		t.Fatalf("assign tag: %v", err)
	}
	insertOrder(t, f.db, parent, &bookID, nil, "paid")

	shelf, err := f.svc.Load(ctx, parent)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	card := shelf.Books[0]
	if card.AccessType != "paid" {
		t.Errorf("access_type = %q, want paid", card.AccessType)
	}
	if card.PublishedAt.IsZero() {
		t.Error("published_at missing")
	}
	if card.PriceVND == nil || *card.PriceVND != 10000 {
		t.Errorf("price = %v", card.PriceVND)
	}
	if len(card.TagSlugs) != 1 || card.TagSlugs[0] != "dong-vat" {
		t.Errorf("tag slugs = %v", card.TagSlugs)
	}
	if card.CoverURL == "" {
		t.Error("cover url missing")
	}
}

package catalog_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"littleworld/internal/catalog"
	"littleworld/internal/dbtest"
	"littleworld/internal/storagetest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type fixture struct {
	svc   *catalog.Service
	db    *sql.DB
	store *storagetest.Mem
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := dbtest.Migrated(t)
	store := storagetest.New()
	return fixture{svc: catalog.New(pool, store), db: pool, store: store}
}

func insertUser(t *testing.T, db *sql.DB, email string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(t.Context(),
		`INSERT INTO users (email, password_hash, role) VALUES ($1, 'x', 'teacher') RETURNING id`,
		email).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func insertBook(t *testing.T, db *sql.DB, ownerID int64, slug, title, status string, access string, price *int64) int64 {
	t.Helper()
	var id int64
	err := db.QueryRowContext(t.Context(),
		`INSERT INTO books (owner_id, slug, title, description, cover_object_key, epub_object_key,
			access_type, price_vnd, status, published_at)
		 VALUES ($1, $2, $3, 'Mô tả', $4, $5, $6, $7, $8,
		         CASE WHEN $8 = 'published' THEN now() ELSE NULL END)
		 RETURNING id`,
		ownerID, slug, title, "books/"+slug+"/cover.png", "books/"+slug+"/book.epub",
		access, price, status).Scan(&id)
	if err != nil {
		t.Fatalf("insert book %s: %v", slug, err)
	}
	return id
}

func price(v int64) *int64 { return &v }

func TestListOnlyReturnsPublished(t *testing.T) {
	f := newFixture(t)
	teacher := insertUser(t, f.db, "teacher@example.com")

	insertBook(t, f.db, teacher, "dong-vat", "Động vật quanh bé", "published", "free", nil)
	insertBook(t, f.db, teacher, "the-gioi", "Thế giới thực vật", "published", "paid", price(99000))
	insertBook(t, f.db, teacher, "nhap", "Sách nháp", "draft", "free", nil)

	cards, hasMore, err := f.svc.List(context.Background(), catalog.Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if hasMore {
		t.Error("has_more = true on a short list")
	}
	if len(cards) != 2 {
		t.Fatalf("cards = %d, want 2 published", len(cards))
	}
	for _, card := range cards {
		if card.Slug == "nhap" {
			t.Error("draft leaked into the public list")
		}
		if card.CoverURL == "" {
			t.Errorf("cover url missing for %s", card.Slug)
		}
	}
}

func TestListFiltersAndSearch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com")

	animal := insertBook(t, f.db, teacher, "dong-vat", "Động vật quanh bé", "published", "free", nil)
	plant := insertBook(t, f.db, teacher, "the-gioi", "Thế giới thực vật", "published", "paid", price(99000))

	// A topic tag on the animal book only.
	var tagID int64
	if err := f.db.QueryRowContext(ctx,
		`INSERT INTO tags (slug, name, created_by) VALUES ('thien-nhien', 'Thiên nhiên', $1) RETURNING id`,
		teacher).Scan(&tagID); err != nil {
		t.Fatalf("insert tag: %v", err)
	}
	if _, err := f.db.ExecContext(ctx,
		`INSERT INTO book_tags (book_id, tag_id) VALUES ($1, $2)`, animal, tagID); err != nil {
		t.Fatalf("assign tag: %v", err)
	}
	_ = plant

	// Search without diacritics must match the Vietnamese title.
	bySlugSearch, _, err := f.svc.List(ctx, catalog.Filter{Query: "dong vat"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(bySlugSearch) != 1 || bySlugSearch[0].Slug != "dong-vat" {
		t.Errorf("slug search = %+v", bySlugSearch)
	}

	byTitleSearch, _, err := f.svc.List(ctx, catalog.Filter{Query: "Thực vật"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(byTitleSearch) != 1 || byTitleSearch[0].Slug != "the-gioi" {
		t.Errorf("title search = %+v", byTitleSearch)
	}

	byTag, _, err := f.svc.List(ctx, catalog.Filter{TagSlug: "thien-nhien"})
	if err != nil {
		t.Fatalf("tag filter: %v", err)
	}
	if len(byTag) != 1 || byTag[0].Slug != "dong-vat" {
		t.Errorf("tag filter = %+v", byTag)
	}
	if len(byTag[0].TagSlugs) != 1 || byTag[0].TagSlugs[0] != "thien-nhien" {
		t.Errorf("tag slugs = %v", byTag[0].TagSlugs)
	}

	if _, _, err := f.svc.List(ctx, catalog.Filter{Kind: "nope"}); err == nil {
		t.Error("unknown kind was accepted")
	}
}

func TestListPaginatesAndIncludesBundles(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com")

	first := insertBook(t, f.db, teacher, "sach-mot", "Sách một", "published", "paid", price(10000))
	second := insertBook(t, f.db, teacher, "sach-hai", "Sách hai", "published", "paid", price(20000))

	var bundleID int64
	if err := f.db.QueryRowContext(ctx,
		`INSERT INTO bundles (owner_id, slug, title, description, price_vnd, status, published_at)
		 VALUES ($1, 'goi-hai-sach', 'Gói hai sách', 'Mô tả', 25000, 'published', now()) RETURNING id`,
		teacher).Scan(&bundleID); err != nil {
		t.Fatalf("insert bundle: %v", err)
	}
	for i, bookID := range []int64{first, second} {
		if _, err := f.db.ExecContext(ctx,
			`INSERT INTO bundle_books (bundle_id, book_id, position) VALUES ($1, $2, $3)`,
			bundleID, bookID, i+1); err != nil {
			t.Fatalf("insert bundle book: %v", err)
		}
	}

	page1, hasMore, err := f.svc.List(ctx, catalog.Filter{Limit: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page1) != 2 || !hasMore {
		t.Fatalf("page1 = %d items, has_more = %v", len(page1), hasMore)
	}

	page2, hasMore, err := f.svc.List(ctx, catalog.Filter{Limit: 2, Page: 2})
	if err != nil {
		t.Fatalf("list page 2: %v", err)
	}
	if len(page2) != 1 || hasMore {
		t.Fatalf("page2 = %d items, has_more = %v", len(page2), hasMore)
	}

	onlyBundles, _, err := f.svc.List(ctx, catalog.Filter{Kind: catalog.KindBundle})
	if err != nil {
		t.Fatalf("list bundles: %v", err)
	}
	if len(onlyBundles) != 1 || onlyBundles[0].Kind != catalog.KindBundle {
		t.Fatalf("bundles = %+v", onlyBundles)
	}
	if onlyBundles[0].BookCount != 2 {
		t.Errorf("book count = %d, want 2", onlyBundles[0].BookCount)
	}
	if onlyBundles[0].AccessType != catalog.AccessPaid {
		t.Errorf("bundle access type = %q", onlyBundles[0].AccessType)
	}
}

func TestBookDetailHidesDrafts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com")

	insertBook(t, f.db, teacher, "da-xuat-ban", "Đã xuất bản", "published", "free", nil)
	insertBook(t, f.db, teacher, "con-nhap", "Còn nháp", "draft", "free", nil)

	detail, err := f.svc.Book(ctx, "da-xuat-ban")
	if err != nil {
		t.Fatalf("published detail: %v", err)
	}
	if detail.Title != "Đã xuất bản" || detail.Description != "Mô tả" || !detail.HasEPUB {
		t.Errorf("detail = %+v", detail)
	}
	if detail.CoverURL == "" {
		t.Error("cover url missing")
	}

	if _, err := f.svc.Book(ctx, "con-nhap"); err == nil {
		t.Error("draft book detail was readable")
	}
}

func TestBundleDetailListsOnlyPublishedBooks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com")

	published := insertBook(t, f.db, teacher, "da-xuat-ban", "Đã xuất bản", "published", "paid", price(10000))
	draft := insertBook(t, f.db, teacher, "con-nhap", "Còn nháp", "draft", "paid", price(10000))

	var bundleID int64
	if err := f.db.QueryRowContext(ctx,
		`INSERT INTO bundles (owner_id, slug, title, description, price_vnd, status, published_at)
		 VALUES ($1, 'goi', 'Gói', 'Mô tả', 15000, 'published', now()) RETURNING id`,
		teacher).Scan(&bundleID); err != nil {
		t.Fatalf("insert bundle: %v", err)
	}
	for i, bookID := range []int64{published, draft} {
		if _, err := f.db.ExecContext(ctx,
			`INSERT INTO bundle_books (bundle_id, book_id, position) VALUES ($1, $2, $3)`,
			bundleID, bookID, i+1); err != nil {
			t.Fatalf("insert bundle book: %v", err)
		}
	}

	detail, err := f.svc.Bundle(ctx, "goi")
	if err != nil {
		t.Fatalf("bundle detail: %v", err)
	}
	if len(detail.Books) != 1 || detail.Books[0].Slug != "da-xuat-ban" {
		t.Errorf("bundle books = %+v", detail.Books)
	}
	// The count must match what the buyer can actually read: only eligible books.
	if detail.BookCount != 1 {
		t.Errorf("book count = %d, want 1 published eligible book", detail.BookCount)
	}

	if _, err := f.svc.Bundle(ctx, "khong-ton-tai"); err == nil {
		t.Error("missing bundle was returned")
	}
}

func TestPaginationOrderIsStable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com")

	for _, slug := range []string{"a-book", "b-book", "c-book"} {
		insertBook(t, f.db, teacher, slug, slug, "published", "free", nil)
	}

	page1, hasMore, err := f.svc.List(ctx, catalog.Filter{Limit: 2})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	page2, more, err := f.svc.List(ctx, catalog.Filter{Limit: 2, Page: 2})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if !hasMore || more {
		t.Fatalf("has_more = %v then %v", hasMore, more)
	}

	seen := map[string]bool{}
	for _, card := range append(page1, page2...) {
		if seen[card.Slug] {
			t.Errorf("duplicate item across pages: %s", card.Slug)
		}
		seen[card.Slug] = true
	}
	if len(seen) != 3 {
		t.Errorf("saw %d distinct books, want 3", len(seen))
	}
}

// A wildcard in the search box must be literal, not "match everything".
func TestSearchEscapesWildcards(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	teacher := insertUser(t, f.db, "teacher@example.com")

	insertBook(t, f.db, teacher, "khong-lien-quan", "Sách thường", "published", "free", nil)

	cards, _, err := f.svc.List(ctx, catalog.Filter{Query: "%"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(cards) != 0 {
		t.Errorf("search %% returned %d books", len(cards))
	}
}

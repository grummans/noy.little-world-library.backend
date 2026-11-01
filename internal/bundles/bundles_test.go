package bundles_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"littleworld/internal/books"
	"littleworld/internal/bundles"
	"littleworld/internal/dbtest"
	"littleworld/internal/storagetest"
	"littleworld/internal/tags"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type fixture struct {
	svc      *bundles.Service
	books    *books.Service
	store    *storagetest.Mem
	db       *sql.DB
	teacher  int64
	other    int64
	paidBook int64
	freeBook int64
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := dbtest.Migrated(t)
	store := storagetest.New()

	f := fixture{svc: bundles.New(pool, store), books: books.New(pool, store), store: store, db: pool}
	for _, row := range []struct {
		email string
		dest  *int64
	}{{"teacher@example.com", &f.teacher}, {"other@example.com", &f.other}} {
		if err := pool.QueryRowContext(t.Context(),
			`INSERT INTO users (email, password_hash, role) VALUES ($1, 'x', 'teacher') RETURNING id`,
			row.email).Scan(row.dest); err != nil {
			t.Fatalf("insert teacher: %v", err)
		}
	}

	f.paidBook = insertBook(t, pool, f.teacher, "paid-book", booksPaid)
	f.freeBook = insertBook(t, pool, f.teacher, "free-book", booksFree)
	return f
}

const (
	booksPaid = iota
	booksFree
)

func insertBook(t *testing.T, pool *sql.DB, ownerID int64, slug string, kind int) int64 {
	t.Helper()

	var id int64
	var err error
	switch kind {
	case booksPaid:
		err = pool.QueryRowContext(t.Context(),
			`INSERT INTO books (owner_id, slug, title, description, cover_object_key,
				epub_object_key, access_type, price_vnd, status, published_at)
			 VALUES ($1, $2, 'Paid', 'desc', 'books/1/cover.png', 'books/1/book.epub', 'paid', 99000, 'published', now())
			 RETURNING id`, ownerID, slug).Scan(&id)
	default:
		err = pool.QueryRowContext(t.Context(),
			`INSERT INTO books (owner_id, slug, title, access_type) VALUES ($1, $2, 'Free', 'free') RETURNING id`,
			ownerID, slug).Scan(&id)
	}
	if err != nil {
		t.Fatalf("insert book %s: %v", slug, err)
	}
	return id
}

func meta() bundles.Metadata {
	price := int64(199000)
	return bundles.Metadata{Title: "Gói động vật", Description: "Ba sách", PriceVND: &price}
}

func TestCreateBundleWithPaidBooks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	bundle, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{
		Metadata: meta(),
		BookIDs:  []int64{f.paidBook},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if bundle.Slug != "goi-dong-vat" {
		t.Errorf("slug = %q", bundle.Slug)
	}
	if bundle.Status != bundles.StatusDraft || bundle.PriceVND != 199000 {
		t.Errorf("bundle = %+v", bundle)
	}
	if len(bundle.BookIDs) != 1 || bundle.BookIDs[0] != f.paidBook {
		t.Errorf("book ids = %v", bundle.BookIDs)
	}
}

func TestBundleRejectsIneligibleBooks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Free book.
	if _, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{
		Metadata: meta(), BookIDs: []int64{f.freeBook},
	}); !errors.Is(err, bundles.ErrInvalidBook) {
		t.Errorf("free book: err = %v, want ErrInvalidBook", err)
	}

	// Another teacher's paid book.
	otherBook := insertBook(t, f.db, f.other, "other-paid", booksPaid)
	if _, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{
		Metadata: meta(), BookIDs: []int64{otherBook},
	}); !errors.Is(err, bundles.ErrInvalidBook) {
		t.Errorf("other teacher's book: err = %v, want ErrInvalidBook", err)
	}

	// Draft book of the same teacher.
	var draftID int64
	if err := f.db.QueryRowContext(ctx,
		`INSERT INTO books (owner_id, slug, title, access_type, price_vnd)
		 VALUES ($1, 'draft-paid', 'Draft', 'paid', 5000) RETURNING id`, f.teacher).Scan(&draftID); err != nil {
		t.Fatalf("insert draft: %v", err)
	}
	if _, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{
		Metadata: meta(), BookIDs: []int64{draftID},
	}); !errors.Is(err, bundles.ErrInvalidBook) {
		t.Errorf("draft book: err = %v, want ErrInvalidBook", err)
	}

	// Nothing is written when validation fails.
	list, err := f.svc.ListByOwner(ctx, f.teacher)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("invalid bundles were saved: %d", len(list))
	}
}

func TestBundleValidatesMetadata(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	noPrice := bundles.Metadata{Title: "Gói", Description: "x"}
	if _, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{Metadata: noPrice}); !errors.Is(err, bundles.ErrValidation) {
		t.Errorf("missing price: err = %v", err)
	}

	zeroPrice := int64(0)
	if _, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{
		Metadata: bundles.Metadata{Title: "Gói", PriceVND: &zeroPrice},
	}); !errors.Is(err, bundles.ErrValidation) {
		t.Errorf("zero price: err = %v", err)
	}

	if _, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{
		Metadata: bundles.Metadata{Title: "  ", PriceVND: &zeroPrice},
	}); !errors.Is(err, bundles.ErrValidation) {
		t.Errorf("empty title: err = %v", err)
	}

	if _, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{Metadata: meta(), BookIDs: []int64{f.paidBook}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{Metadata: meta(), BookIDs: []int64{f.paidBook}}); !errors.Is(err, bundles.ErrSlugTaken) {
		t.Errorf("duplicate slug: err = %v", err)
	}
}

func TestPublishRequiresBooksAndOwnership(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	emptyMeta := meta()
	emptyMeta.Title = "Gói chưa có sách"
	empty, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{Metadata: emptyMeta})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.svc.Publish(ctx, f.teacher, empty.ID); !errors.Is(err, bundles.ErrNotPublishable) {
		t.Errorf("empty bundle publish: err = %v", err)
	}

	full, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{Metadata: meta(), BookIDs: []int64{f.paidBook}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.svc.Publish(ctx, f.other, full.ID); !errors.Is(err, bundles.ErrNotFound) {
		t.Errorf("other teacher publish: err = %v", err)
	}

	published, err := f.svc.Publish(ctx, f.teacher, full.ID)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.Status != bundles.StatusPublished || published.PublishedAt == nil {
		t.Errorf("published = %+v", published)
	}

	unpublished, err := f.svc.Unpublish(ctx, f.teacher, full.ID)
	if err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	if unpublished.Status != bundles.StatusDraft || unpublished.PublishedAt != nil {
		t.Errorf("unpublished = %+v", unpublished)
	}
}

func TestUpdateReplacesBookOrderAndTags(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	var tagID int64
	if err := f.db.QueryRowContext(ctx,
		`INSERT INTO tags (slug, name) VALUES ('dong-vat', 'Động vật') RETURNING id`).Scan(&tagID); err != nil {
		t.Fatalf("insert tag: %v", err)
	}
	secondBook := insertBook(t, f.db, f.teacher, "second-paid", booksPaid)

	input := bundles.CreateInput{Metadata: meta(), BookIDs: []int64{f.paidBook, secondBook}}
	bundle, err := f.svc.Create(ctx, f.teacher, input)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(bundle.BookIDs) != 2 || bundle.BookIDs[0] != f.paidBook {
		t.Fatalf("book ids = %v", bundle.BookIDs)
	}

	updatedMeta := meta()
	updatedMeta.Title = "Gói mới"
	updatedMeta.TagIDs = []int64{tagID}
	updated, err := f.svc.Update(ctx, f.teacher, bundle.ID, bundles.CreateInput{
		Metadata: updatedMeta,
		BookIDs:  []int64{secondBook},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(updated.BookIDs) != 1 || updated.BookIDs[0] != secondBook {
		t.Errorf("book ids after update = %v", updated.BookIDs)
	}
	if len(updated.Tags) != 1 || updated.Tags[0] != tagID {
		t.Errorf("tags = %v", updated.Tags)
	}
	// Renaming must not move a bundle that is already linked or sold.
	if updated.Slug != bundle.Slug {
		t.Errorf("slug = %q, want unchanged %q", updated.Slug, bundle.Slug)
	}

	if _, err := f.svc.Update(ctx, f.teacher, bundle.ID, bundles.CreateInput{
		Metadata: updatedMeta, BookIDs: []int64{f.freeBook},
	}); !errors.Is(err, bundles.ErrInvalidBook) {
		t.Errorf("update with free book: err = %v", err)
	}
}

func TestCoverUploadStagesAndPromotes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	bundle, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{Metadata: meta()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	cover := storagetest.PNG(t, 64)
	withCover, err := f.svc.ReplaceCover(ctx, f.teacher, bundle.ID, bundles.Upload{
		ContentType: "image/png", Size: int64(len(cover)), Body: bytes.NewReader(cover),
	})
	if err != nil {
		t.Fatalf("replace cover: %v", err)
	}
	if withCover.CoverKey == nil || !strings.HasSuffix(*withCover.CoverKey, "cover.png") {
		t.Errorf("cover key = %v", withCover.CoverKey)
	}
	for _, key := range f.store.Keys() {
		if strings.HasPrefix(key, "uploads/") {
			t.Errorf("staging object left behind: %s", key)
		}
	}

	fake := []byte("<html>nope</html>")
	if _, err := f.svc.ReplaceCover(ctx, f.teacher, bundle.ID, bundles.Upload{
		ContentType: "image/png", Size: int64(len(fake)), Body: bytes.NewReader(fake),
	}); err == nil {
		t.Error("invalid cover was accepted")
	}
}

func TestTagsHelperRejectsUnknownIDs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	bundle, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{Metadata: meta()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	badMeta := meta()
	badMeta.TagIDs = []int64{424242}
	if _, err := f.svc.Update(ctx, f.teacher, bundle.ID, bundles.CreateInput{Metadata: badMeta}); !errors.Is(err, tags.ErrUnknownTag) {
		t.Errorf("err = %v, want tags.ErrUnknownTag", err)
	}
}

func TestPublishedBundleBlocksBookDowngrade(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	bundle, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{Metadata: meta(), BookIDs: []int64{f.paidBook}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.svc.Publish(ctx, f.teacher, bundle.ID); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// The book is now sold inside a published bundle: it may not leave the catalog.
	if _, err := f.books.Unpublish(ctx, f.teacher, f.paidBook); !errors.Is(err, books.ErrValidation) {
		t.Errorf("unpublish bundled book: err = %v, want ErrValidation", err)
	}
	if _, err := f.books.Update(ctx, f.teacher, f.paidBook, books.Metadata{
		Title:       "Paid",
		Description: "desc",
		AccessType:  books.AccessFree,
	}); !errors.Is(err, books.ErrValidation) {
		t.Errorf("make bundled book free: err = %v, want ErrValidation", err)
	}
}

func TestUpdatePublishedBundleRejectsEmptyBookList(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	bundle, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{Metadata: meta(), BookIDs: []int64{f.paidBook}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.svc.Publish(ctx, f.teacher, bundle.ID); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// A published bundle keeps the books its buyers already paid for.
	if _, err := f.svc.Update(ctx, f.teacher, bundle.ID, bundles.CreateInput{Metadata: meta()}); !errors.Is(err, bundles.ErrValidation) {
		t.Errorf("empty list on published bundle: err = %v, want ErrValidation", err)
	}
}

func TestUpdateReordersBooks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	second := insertBook(t, f.db, f.teacher, "second-paid", booksPaid)
	bundle, err := f.svc.Create(ctx, f.teacher, bundles.CreateInput{
		Metadata: meta(), BookIDs: []int64{f.paidBook, second},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	reordered, err := f.svc.Update(ctx, f.teacher, bundle.ID, bundles.CreateInput{
		Metadata: meta(), BookIDs: []int64{second, f.paidBook},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(reordered.BookIDs) != 2 || reordered.BookIDs[0] != second || reordered.BookIDs[1] != f.paidBook {
		t.Errorf("book order = %v, want [%d %d]", reordered.BookIDs, second, f.paidBook)
	}
}

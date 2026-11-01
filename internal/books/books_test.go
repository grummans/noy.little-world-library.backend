package books_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"littleworld/internal/books"
	"littleworld/internal/dbtest"
	"littleworld/internal/storage"
	"littleworld/internal/storagetest"
	"littleworld/internal/tags"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

// testPool is the schema of the most recent newService call. Tests in this package
// run sequentially, so a single slot is enough for assertions that need raw SQL.
var testPool *sql.DB

func newService(t *testing.T) (*books.Service, *storagetest.Mem, int64, int64) {
	t.Helper()
	pool := dbtest.Migrated(t)
	testPool = pool
	store := storagetest.New()

	var teacherID, otherTeacherID int64
	for _, row := range []struct {
		email string
		dest  *int64
	}{
		{"teacher@example.com", &teacherID},
		{"other@example.com", &otherTeacherID},
	} {
		if err := pool.QueryRowContext(t.Context(),
			`INSERT INTO users (email, password_hash, role) VALUES ($1, 'x', 'teacher') RETURNING id`,
			row.email).Scan(row.dest); err != nil {
			t.Fatalf("insert teacher: %v", err)
		}
	}
	return books.New(pool, store), store, teacherID, otherTeacherID
}

func epubUpload(t *testing.T) *books.Upload {
	t.Helper()
	data := storagetest.EPUB(t)
	return &books.Upload{ContentType: storage.EpubContentType, Size: int64(len(data)), Body: bytes.NewReader(data)}
}

func coverUpload(t *testing.T) *books.Upload {
	t.Helper()
	data := storagetest.PNG(t, 64)
	return &books.Upload{ContentType: "image/png", Size: int64(len(data)), Body: bytes.NewReader(data)}
}

func draftMeta() books.Metadata {
	return books.Metadata{Title: "Động vật quanh bé", Description: "Sách về động vật", AccessType: books.AccessFree}
}

func TestCreateDraftWithFiles(t *testing.T) {
	svc, store, teacherID, _ := newService(t)
	ctx := context.Background()

	book, err := svc.Create(ctx, teacherID, books.CreateInput{
		Metadata: draftMeta(),
		Epub:     epubUpload(t),
		Cover:    coverUpload(t),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if book.Status != books.StatusDraft {
		t.Errorf("status = %q, want draft", book.Status)
	}
	if book.Slug != "dong-vat-quanh-be" {
		t.Errorf("slug = %q, want derived from title", book.Slug)
	}
	if book.EpubKey == nil || *book.EpubKey != storage.EpubKey(book.ID) {
		t.Errorf("epub key = %v", book.EpubKey)
	}
	if book.CoverKey == nil || !strings.HasSuffix(*book.CoverKey, "/cover.png") {
		t.Errorf("cover key = %v", book.CoverKey)
	}
	for _, key := range store.Keys() {
		if strings.HasPrefix(key, "uploads/") {
			t.Errorf("staging object left behind: %s", key)
		}
	}
}

func TestCreateRejectsInvalidEPUBAndRollsBack(t *testing.T) {
	for name, opts := range map[string]func(*storagetest.EPUBOptions){
		"mimetype not first":  func(o *storagetest.EPUBOptions) { o.MimetypeLast = true },
		"mimetype deflated":   func(o *storagetest.EPUBOptions) { o.DeflateMimetype = true },
		"missing container":   func(o *storagetest.EPUBOptions) { o.SkipContainer = true },
		"wrong mimetype text": func(o *storagetest.EPUBOptions) { o.Mimetype = "application/zip" },
	} {
		t.Run(name, func(t *testing.T) {
			svc, store, teacherID, _ := newService(t)
			ctx := context.Background()

			bad := storagetest.EPUB(t, opts)
			_, err := svc.Create(ctx, teacherID, books.CreateInput{
				Metadata: draftMeta(),
				Epub:     &books.Upload{ContentType: storage.EpubContentType, Size: int64(len(bad)), Body: bytes.NewReader(bad)},
			})
			if !errors.Is(err, storage.ErrInvalidEpub) {
				t.Fatalf("err = %v, want ErrInvalidEpub", err)
			}

			list, err := svc.ListByOwner(ctx, teacherID)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(list) != 0 {
				t.Errorf("draft not rolled back: %d books left", len(list))
			}
			if keys := store.Keys(); len(keys) != 0 {
				t.Errorf("objects left after rollback: %v", keys)
			}
		})
	}
}

// A bad cover must not replace the EPUB of a book that is already on sale.
func TestReplaceFilesKeepsOldEPUBWhenCoverIsInvalid(t *testing.T) {
	svc, store, teacherID, _ := newService(t)
	ctx := context.Background()

	book, err := svc.Create(ctx, teacherID, books.CreateInput{
		Metadata: draftMeta(),
		Epub:     epubUpload(t),
		Cover:    coverUpload(t),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	before, _ := store.Get(storage.EpubKey(book.ID))

	fakeImage := []byte("<html>not an image</html>")
	_, err = svc.ReplaceFiles(ctx, teacherID, book.ID, books.CreateInput{
		Epub:  epubUpload(t),
		Cover: &books.Upload{ContentType: "image/png", Size: int64(len(fakeImage)), Body: bytes.NewReader(fakeImage)},
	})
	if !errors.Is(err, storage.ErrInvalidImage) {
		t.Fatalf("err = %v, want ErrInvalidImage", err)
	}

	after, ok := store.Get(storage.EpubKey(book.ID))
	if !ok || !bytes.Equal(before, after) {
		t.Error("epub was replaced even though the cover failed validation")
	}
	for _, key := range store.Keys() {
		if strings.HasPrefix(key, "uploads/") {
			t.Errorf("staging object left behind: %s", key)
		}
	}
}

func TestCoverMustMatchDeclaredType(t *testing.T) {
	svc, _, teacherID, _ := newService(t)
	ctx := context.Background()

	png := storagetest.PNG(t, 64)
	_, err := svc.Create(ctx, teacherID, books.CreateInput{
		Metadata: draftMeta(),
		Cover:    &books.Upload{ContentType: "image/jpeg", Size: int64(len(png)), Body: bytes.NewReader(png)},
	})
	if !errors.Is(err, storage.ErrInvalidImage) {
		t.Fatalf("png declared as jpeg: err = %v, want ErrInvalidImage", err)
	}
}
func TestTagsAreStoredAndReplaced(t *testing.T) {
	svc, _, teacherID, _ := newService(t)
	ctx := context.Background()

	var animalID, familyID int64
	for _, row := range []struct {
		slug, name string
		dest       *int64
	}{{"dong-vat", "Động vật", &animalID}, {"gia-dinh", "Gia đình", &familyID}} {
		if err := testPool.QueryRowContext(ctx,
			`INSERT INTO tags (slug, name) VALUES ($1, $2) RETURNING id`,
			row.slug, row.name).Scan(row.dest); err != nil {
			t.Fatalf("insert tag: %v", err)
		}
	}

	meta := draftMeta()
	meta.TagID = &animalID
	book, err := svc.Create(ctx, teacherID, books.CreateInput{Metadata: meta})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(book.Tags) != 1 || book.Tags[0] != animalID {
		t.Fatalf("tags = %v, want [%d]", book.Tags, animalID)
	}

	updated, err := svc.Update(ctx, teacherID, book.ID, books.Metadata{
		Title:       book.Title,
		Description: book.Description,
		AccessType:  books.AccessFree,
		TagID:       &familyID,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	// One topic per book: the switch replaces the previous one instead of adding.
	if len(updated.Tags) != 1 || updated.Tags[0] != familyID {
		t.Fatalf("tags = %v, want [%d]", updated.Tags, familyID)
	}

	// The database refuses a second topic even if something writes around the service.
	if _, err := testPool.ExecContext(ctx,
		`INSERT INTO book_tags (book_id, tag_id) VALUES ($1, $2)`, book.ID, animalID,
	); err == nil {
		t.Fatal("second book_tags row was accepted, want unique violation")
	}

	if _, err := svc.Update(ctx, teacherID, book.ID, books.Metadata{
		Title:       book.Title,
		Description: book.Description,
		AccessType:  books.AccessFree,
		TagID:       ptr[int64](9999),
	}); !errors.Is(err, tags.ErrUnknownTag) {
		t.Fatalf("unknown tag: err = %v, want ErrUnknownTag", err)
	}
}

func TestPublishedBookCannotLoseDescription(t *testing.T) {
	svc, _, teacherID, _ := newService(t)
	ctx := context.Background()

	book, err := svc.Create(ctx, teacherID, books.CreateInput{
		Metadata: draftMeta(),
		Epub:     epubUpload(t),
		Cover:    coverUpload(t),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Publish(ctx, teacherID, book.ID); err != nil {
		t.Fatalf("publish: %v", err)
	}

	cleared := draftMeta()
	cleared.Description = ""
	if _, err := svc.Update(ctx, teacherID, book.ID, cleared); !errors.Is(err, books.ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
}

func TestCreateRejectsFakeCover(t *testing.T) {
	svc, _, teacherID, _ := newService(t)
	ctx := context.Background()

	fake := []byte("<html>not an image</html>")
	_, err := svc.Create(ctx, teacherID, books.CreateInput{
		Metadata: draftMeta(),
		Cover:    &books.Upload{ContentType: "image/png", Size: int64(len(fake)), Body: bytes.NewReader(fake)},
	})
	if !errors.Is(err, storage.ErrInvalidImage) {
		t.Fatalf("err = %v, want ErrInvalidImage", err)
	}
}

func TestCreateValidatesMetadata(t *testing.T) {
	svc, _, teacherID, otherTeacherID := newService(t)
	ctx := context.Background()

	paidNoPrice := draftMeta()
	paidNoPrice.AccessType = books.AccessPaid
	if _, err := svc.Create(ctx, teacherID, books.CreateInput{Metadata: paidNoPrice}); !errors.Is(err, books.ErrValidation) {
		t.Errorf("paid without price: err = %v", err)
	}

	if _, err := svc.Create(ctx, teacherID, books.CreateInput{Metadata: books.Metadata{Title: " "}}); !errors.Is(err, books.ErrValidation) {
		t.Errorf("empty title: err = %v", err)
	}

	badAge := draftMeta()
	badAge.AgeMin, badAge.AgeMax = ptr[int16](6), ptr[int16](3)
	if _, err := svc.Create(ctx, teacherID, books.CreateInput{Metadata: badAge}); !errors.Is(err, books.ErrValidation) {
		t.Errorf("inverted ages: err = %v", err)
	}

	if _, err := svc.Create(ctx, teacherID, books.CreateInput{Metadata: draftMeta()}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Create(ctx, otherTeacherID, books.CreateInput{Metadata: draftMeta()}); !errors.Is(err, books.ErrSlugTaken) {
		t.Errorf("duplicate slug across teachers: err = %v", err)
	}
}

func TestPublishRequiresFilesAndDescription(t *testing.T) {
	svc, _, teacherID, otherTeacherID := newService(t)
	ctx := context.Background()

	emptyMeta := draftMeta()
	emptyMeta.Title = "Chưa có tệp"
	empty, err := svc.Create(ctx, teacherID, books.CreateInput{Metadata: emptyMeta})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Publish(ctx, teacherID, empty.ID); !errors.Is(err, books.ErrNotPublishable) {
		t.Errorf("publish without files: err = %v, want ErrNotPublishable", err)
	}

	book, err := svc.Create(ctx, teacherID, books.CreateInput{
		Metadata: draftMeta(),
		Epub:     epubUpload(t),
		Cover:    coverUpload(t),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := svc.Publish(ctx, otherTeacherID, book.ID); !errors.Is(err, books.ErrNotFound) {
		t.Errorf("other teacher publish: err = %v, want ErrNotFound", err)
	}

	published, err := svc.Publish(ctx, teacherID, book.ID)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.Status != books.StatusPublished || published.PublishedAt == nil {
		t.Errorf("published = %+v", published)
	}

	unpublished, err := svc.Unpublish(ctx, teacherID, book.ID)
	if err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	if unpublished.Status != books.StatusDraft || unpublished.PublishedAt != nil {
		t.Errorf("unpublished = %+v", unpublished)
	}
}

func TestOwnershipIsolation(t *testing.T) {
	svc, _, teacherID, otherTeacherID := newService(t)
	ctx := context.Background()

	book, err := svc.Create(ctx, teacherID, books.CreateInput{Metadata: draftMeta()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := svc.Get(ctx, otherTeacherID, book.ID); !errors.Is(err, books.ErrNotFound) {
		t.Errorf("other teacher read: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Update(ctx, otherTeacherID, book.ID, draftMeta()); !errors.Is(err, books.ErrNotFound) {
		t.Errorf("other teacher update: err = %v", err)
	}

	otherList, err := svc.ListByOwner(ctx, otherTeacherID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(otherList) != 0 {
		t.Errorf("other teacher sees %d books", len(otherList))
	}
}

func TestUpdateAndReplaceFiles(t *testing.T) {
	svc, store, teacherID, _ := newService(t)
	ctx := context.Background()

	book, err := svc.Create(ctx, teacherID, books.CreateInput{
		Metadata: draftMeta(),
		Epub:     epubUpload(t),
		Cover:    coverUpload(t),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updated, err := svc.Update(ctx, teacherID, book.ID, books.Metadata{
		Title:       "Thế giới thực vật",
		Description: "Mô tả mới",
		AccessType:  books.AccessPaid,
		PriceVND:    ptr[int64](99000),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	// Renaming must not move a published book to a new URL.
	if updated.Slug != book.Slug {
		t.Errorf("slug changed on rename: %q -> %q", book.Slug, updated.Slug)
	}
	if updated.Title != "Thế giới thực vật" || updated.PriceVND == nil || *updated.PriceVND != 99000 {
		t.Errorf("updated = %+v", updated)
	}

	// An explicit slug is still honoured.
	moved, err := svc.Update(ctx, teacherID, book.ID, books.Metadata{
		Slug:        "the-gioi-thuc-vat",
		Title:       "Thế giới thực vật",
		Description: "Mô tả mới",
		AccessType:  books.AccessPaid,
		PriceVND:    ptr[int64](99000),
	})
	if err != nil {
		t.Fatalf("update with slug: %v", err)
	}
	if moved.Slug != "the-gioi-thuc-vat" {
		t.Errorf("explicit slug not applied: %+v", moved)
	}

	if _, err := svc.ReplaceFiles(ctx, teacherID, book.ID, books.CreateInput{Epub: epubUpload(t)}); err != nil {
		t.Fatalf("replace epub: %v", err)
	}
	if _, ok := store.Get(storage.EpubKey(book.ID)); !ok {
		t.Error("epub not stored at the fixed key")
	}
}

func ptr[T any](v T) *T { return &v }

// Page count and media flags come from the uploaded EPUB, not from the client.
func TestUploadRecordsEPUBFacts(t *testing.T) {
	svc, _, teacherID, _ := newService(t)
	ctx := context.Background()

	book, err := svc.Create(ctx, teacherID, books.CreateInput{
		Metadata: draftMeta(),
		Epub:     epubUpload(t),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if book.PageCount == nil || *book.PageCount != 3 {
		t.Errorf("page count = %v, want 3", book.PageCount)
	}
	if !book.HasAudio || !book.HasVideo {
		t.Errorf("media flags = audio:%v video:%v", book.HasAudio, book.HasVideo)
	}

	// Replacing the EPUB re-reads the file, so the numbers cannot go stale.
	replaced, err := svc.ReplaceFiles(ctx, teacherID, book.ID, books.CreateInput{Epub: epubUpload(t)})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if replaced.PageCount == nil || *replaced.PageCount != 3 {
		t.Errorf("page count after replace = %v", replaced.PageCount)
	}
}

package activities_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"

	"littleworld/internal/activities"
	"littleworld/internal/dbtest"
	"littleworld/internal/storage"
	"littleworld/internal/storagetest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func newService(t *testing.T) (*activities.Service, *storagetest.Mem, int64, int64) {
	t.Helper()
	pool := dbtest.Migrated(t)
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
	return activities.New(pool, store), store, teacherID, otherTeacherID
}

func baseMeta() activities.Metadata {
	return activities.Metadata{
		Title:       "Chủ đề động vật",
		Description: "Bé nhận biết các con vật quen thuộc.",
		EmbedURL:    "https://wordwall.net/embed/629ec0e8ad7649f5b530f95fd81df87e?themeId=1&templateId=3",
		GameType:    "Ghép đôi",
		Topic:       "Động vật",
		Skills:      []string{"Nhận biết", "Từ vựng"},
		AgeMin:      ptr[int16](3),
		AgeMax:      ptr[int16](5),
	}
}

func ptr[T any](v T) *T { return &v }

func imagePNG(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 30, G: 110, B: 180, A: 255})

	buf := &bytes.Buffer{}
	if err := png.Encode(buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func thumbnailUpload(t *testing.T, width, height int) activities.Upload {
	t.Helper()
	data := imagePNG(t, width, height)
	return activities.Upload{ContentType: "image/png", Size: int64(len(data)), Body: bytes.NewReader(data)}
}

func TestCreateAndPublish(t *testing.T) {
	svc, _, teacherID, _ := newService(t)
	ctx := context.Background()

	activity, err := svc.Create(ctx, teacherID, baseMeta())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if activity.Status != activities.StatusDraft {
		t.Errorf("status = %q, want draft", activity.Status)
	}
	if activity.Slug != "chu-de-dong-vat" {
		t.Errorf("slug = %q", activity.Slug)
	}
	// Unknown display parameters are dropped, so the stored URL is canonical.
	if activity.EmbedURL != "https://wordwall.net/embed/629ec0e8ad7649f5b530f95fd81df87e?templateId=3&themeId=1" {
		t.Errorf("embed URL = %q", activity.EmbedURL)
	}

	published, err := svc.SetStatus(ctx, teacherID, activity.ID, activities.StatusPublished)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.Status != activities.StatusPublished || published.PublishedAt == nil {
		t.Errorf("published = %+v", published)
	}

	// Hiding clears published_at and removes the game from the public catalog.
	hidden, err := svc.SetStatus(ctx, teacherID, activity.ID, activities.StatusHidden)
	if err != nil {
		t.Fatalf("hide: %v", err)
	}
	if hidden.Status != activities.StatusHidden || hidden.PublishedAt != nil {
		t.Errorf("hidden = %+v", hidden)
	}
	if _, err := svc.PublicGet(ctx, activity.Slug); !errors.Is(err, activities.ErrNotFound) {
		t.Errorf("hidden game public get: err = %v", err)
	}
}

func TestPublishNeedsMetadata(t *testing.T) {
	svc, _, teacherID, _ := newService(t)
	ctx := context.Background()

	meta := baseMeta()
	meta.Description = ""
	activity, err := svc.Create(ctx, teacherID, meta)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.SetStatus(ctx, teacherID, activity.ID, activities.StatusPublished); !errors.Is(err, activities.ErrNotPublishable) {
		t.Fatalf("err = %v, want ErrNotPublishable", err)
	}
	if _, err := svc.SetStatus(ctx, teacherID, activity.ID, "live"); !errors.Is(err, activities.ErrValidation) {
		t.Fatalf("unknown status: err = %v", err)
	}
}

func TestOwnershipIsolation(t *testing.T) {
	svc, _, teacherID, otherTeacherID := newService(t)
	ctx := context.Background()

	activity, err := svc.Create(ctx, teacherID, baseMeta())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := svc.Get(ctx, otherTeacherID, activity.ID); !errors.Is(err, activities.ErrNotFound) {
		t.Errorf("read another teacher's game: err = %v", err)
	}
	if _, err := svc.Update(ctx, otherTeacherID, activity.ID, baseMeta()); !errors.Is(err, activities.ErrNotFound) {
		t.Errorf("update another teacher's game: err = %v", err)
	}
	if _, err := svc.SetStatus(ctx, otherTeacherID, activity.ID, activities.StatusPublished); !errors.Is(err, activities.ErrNotFound) {
		t.Errorf("publish another teacher's game: err = %v", err)
	}

	if _, err := svc.Create(ctx, otherTeacherID, baseMeta()); !errors.Is(err, activities.ErrSlugTaken) {
		t.Errorf("duplicate slug across teachers: err = %v", err)
	}
}

func TestUpdateKeepsSlugAndValidatesAges(t *testing.T) {
	svc, _, teacherID, _ := newService(t)
	ctx := context.Background()

	activity, err := svc.Create(ctx, teacherID, baseMeta())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	renamed := baseMeta()
	renamed.Title = "Chủ đề động vật quanh bé"
	renamed.Slug = ""
	updated, err := svc.Update(ctx, teacherID, activity.ID, renamed)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Slug != activity.Slug {
		t.Errorf("slug changed to %q", updated.Slug)
	}
	if updated.Title != renamed.Title {
		t.Errorf("title = %q", updated.Title)
	}

	inverted := baseMeta()
	inverted.AgeMin, inverted.AgeMax = ptr[int16](8), ptr[int16](4)
	if _, err := svc.Update(ctx, teacherID, activity.ID, inverted); !errors.Is(err, activities.ErrValidation) {
		t.Errorf("inverted ages: err = %v", err)
	}
}

func TestPublicListFilters(t *testing.T) {
	svc, _, teacherID, _ := newService(t)
	ctx := context.Background()

	animals := baseMeta()
	motor := baseMeta()
	motor.Title = "Vận động cùng bé"
	motor.GameType = "Mê cung"
	motor.Skills = []string{"Vận động"}
	motor.AgeMin, motor.AgeMax = ptr[int16](6), ptr[int16](8)

	draft := baseMeta()
	draft.Title = "Bản nháp"

	animalsActivity, err := svc.Create(ctx, teacherID, animals)
	if err != nil {
		t.Fatalf("create animals: %v", err)
	}
	motorActivity, err := svc.Create(ctx, teacherID, motor)
	if err != nil {
		t.Fatalf("create motor: %v", err)
	}
	if _, err := svc.Create(ctx, teacherID, draft); err != nil {
		t.Fatalf("create draft: %v", err)
	}
	for _, id := range []int64{animalsActivity.ID, motorActivity.ID} {
		if _, err := svc.SetStatus(ctx, teacherID, id, activities.StatusPublished); err != nil {
			t.Fatalf("publish %d: %v", id, err)
		}
	}

	all, hasMore, err := svc.PublicList(ctx, activities.Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 || hasMore {
		t.Fatalf("published games = %d, has_more = %v", len(all), hasMore)
	}

	byType, _, err := svc.PublicList(ctx, activities.Filter{GameType: "Mê cung"})
	if err != nil || len(byType) != 1 || byType[0].ID != motorActivity.ID {
		t.Fatalf("filter by type: %v, %d items", err, len(byType))
	}

	bySkill, _, err := svc.PublicList(ctx, activities.Filter{Skill: "Từ vựng"})
	if err != nil || len(bySkill) != 1 || bySkill[0].ID != animalsActivity.ID {
		t.Fatalf("filter by skill: %v, %d items", err, len(bySkill))
	}

	byAge, _, err := svc.PublicList(ctx, activities.Filter{Age: ptr[int16](7)})
	if err != nil || len(byAge) != 1 || byAge[0].ID != motorActivity.ID {
		t.Fatalf("filter by age: %v, %d items", err, len(byAge))
	}

	byQuery, _, err := svc.PublicList(ctx, activities.Filter{Query: "vận động"})
	if err != nil || len(byQuery) != 1 {
		t.Fatalf("search by title: %v, %d items", err, len(byQuery))
	}

	facets, err := svc.Facets(ctx)
	if err != nil {
		t.Fatalf("facets: %v", err)
	}
	if len(facets.GameTypes) != 2 || len(facets.Skills) != 3 {
		t.Errorf("facets = %+v", facets)
	}
}

func TestThumbnailReplace(t *testing.T) {
	svc, store, teacherID, _ := newService(t)
	ctx := context.Background()

	activity, err := svc.Create(ctx, teacherID, baseMeta())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	withImage, err := svc.SetThumbnail(ctx, teacherID, activity.ID, thumbnailUpload(t, 1200, 630))
	if err != nil {
		t.Fatalf("set thumbnail: %v", err)
	}
	if withImage.ThumbnailKey == nil || withImage.ThumbnailURL == "" {
		t.Fatalf("thumbnail = %+v", withImage)
	}
	if _, ok := store.Get(*withImage.ThumbnailKey); !ok {
		t.Fatalf("object %q not stored", *withImage.ThumbnailKey)
	}

	// A square image is not a card shape and must be rejected before it is stored.
	_, err = svc.SetThumbnail(ctx, teacherID, activity.ID, thumbnailUpload(t, 400, 400))
	if !errors.Is(err, storage.ErrInvalidThumbnail) {
		t.Fatalf("square thumbnail: err = %v", err)
	}

	fake := []byte("<html>not an image</html>")
	_, err = svc.SetThumbnail(ctx, teacherID, activity.ID, activities.Upload{
		ContentType: "image/png", Size: int64(len(fake)), Body: bytes.NewReader(fake),
	})
	if !errors.Is(err, storage.ErrInvalidThumbnail) {
		t.Fatalf("fake thumbnail: err = %v", err)
	}
}

// Replacing a thumbnail with the same format must publish a new object and remove the old
// one only after the row points at it; a failed upload must leave the current one in place.
func TestThumbnailReplacementKeepsOneObject(t *testing.T) {
	svc, store, teacherID, _ := newService(t)
	ctx := context.Background()

	activity, err := svc.Create(ctx, teacherID, baseMeta())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := svc.SetThumbnail(ctx, teacherID, activity.ID, thumbnailUpload(t, 1200, 630))
	if err != nil {
		t.Fatalf("first thumbnail: %v", err)
	}

	second, err := svc.SetThumbnail(ctx, teacherID, activity.ID, thumbnailUpload(t, 1000, 525))
	if err != nil {
		t.Fatalf("second thumbnail: %v", err)
	}
	if *first.ThumbnailKey == *second.ThumbnailKey {
		t.Fatalf("replacement reused key %q", *second.ThumbnailKey)
	}
	if _, ok := store.Get(*first.ThumbnailKey); ok {
		t.Errorf("object %q of the replaced thumbnail still exists", *first.ThumbnailKey)
	}
	if keys := store.Keys(); len(keys) != 1 || keys[0] != *second.ThumbnailKey {
		t.Errorf("stored keys = %v, want only %q", keys, *second.ThumbnailKey)
	}

	// A failing upload is reported and must not touch the stored object.
	store.FailPut = errors.New("storage down")
	_, err = svc.SetThumbnail(ctx, teacherID, activity.ID, thumbnailUpload(t, 900, 470))
	if err == nil {
		t.Fatal("failing upload returned no error")
	}
	if keys := store.Keys(); len(keys) != 1 || keys[0] != *second.ThumbnailKey {
		t.Errorf("stored keys after failed upload = %v", keys)
	}
	current, err := svc.Get(ctx, teacherID, activity.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if current.ThumbnailKey == nil || *current.ThumbnailKey != *second.ThumbnailKey {
		t.Errorf("row thumbnail = %v, want %q", current.ThumbnailKey, *second.ThumbnailKey)
	}
}

func TestValidationRejectsNonWordwallLinks(t *testing.T) {
	svc, _, teacherID, _ := newService(t)
	ctx := context.Background()

	for _, raw := range []string{
		"http://wordwall.net/embed/629ec0e8ad7649f5b530f95fd81df87e",
		"https://evil.example/embed/629ec0e8ad7649f5b530f95fd81df87e",
		"https://wordwall.net.evil.example/embed/629ec0e8ad7649f5b530f95fd81df87e",
		"https://wordwall.net/game/629ec0e8ad7649f5b530f95fd81df87e",
		"https://wordwall.net/embed/",
		"https://wordwall.net/embed/629ec0e8ad7649f5b530f95fd81df87e?themeId=abc",
		"javascript:alert(1)",
	} {
		meta := baseMeta()
		meta.EmbedURL = raw
		if _, err := svc.Create(ctx, teacherID, meta); !errors.Is(err, activities.ErrValidation) {
			t.Errorf("%q: err = %v, want ErrValidation", raw, err)
		}
	}
}

// testPool guards that the migration and its constraints exist even before the service
// is exercised; the shared schema is migrated by dbtest.
func TestSchemaConstraints(t *testing.T) {
	pool := dbtest.Migrated(t)
	ctx := t.Context()

	var teacherID int64
	if err := pool.QueryRowContext(ctx,
		`INSERT INTO users (email, password_hash, role) VALUES ('schema@example.com', 'x', 'teacher') RETURNING id`,
	).Scan(&teacherID); err != nil {
		t.Fatalf("insert teacher: %v", err)
	}

	_, err := pool.ExecContext(ctx,
		`INSERT INTO activities (owner_id, slug, title, embed_url, game_type, status, published_at)
		 VALUES ($1, 'bad-status', 'Bad', 'https://wordwall.net/embed/abcdefgh', 'Quiz', 'live', NULL)`,
		teacherID)
	if err == nil {
		t.Error("unknown status was accepted")
	}

	_, err = pool.ExecContext(ctx,
		`INSERT INTO activities (owner_id, slug, title, embed_url, game_type, status)
		 VALUES ($1, 'no-published-at', 'Bad', 'https://wordwall.net/embed/abcdefgh', 'Quiz', 'published')`,
		teacherID)
	if err == nil {
		t.Error("published row without published_at was accepted")
	}

	_, err = pool.ExecContext(ctx,
		`INSERT INTO activities (owner_id, slug, title, embed_url, game_type, age_min, age_max)
		 VALUES ($1, 'bad-ages', 'Bad', 'https://wordwall.net/embed/abcdefgh', 'Quiz', 9, 4)`,
		teacherID)
	if err == nil {
		t.Error("inverted age range was accepted")
	}
}

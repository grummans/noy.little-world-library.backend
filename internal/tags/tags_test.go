package tags_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"littleworld/internal/dbtest"
	"littleworld/internal/tags"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func fixture(t *testing.T) (pool *sql.DB, teacher, other int64) {
	t.Helper()
	pool = dbtest.Migrated(t)
	for _, row := range []struct {
		email string
		dest  *int64
	}{{"teacher@example.com", &teacher}, {"other@example.com", &other}} {
		if err := pool.QueryRowContext(t.Context(),
			`INSERT INTO users (email, password_hash, role) VALUES ($1, 'x', 'teacher') RETURNING id`,
			row.email).Scan(row.dest); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}
	return pool, teacher, other
}

func TestCreateDerivesSlugAndRejectsDuplicates(t *testing.T) {
	pool, teacher, _ := fixture(t)
	ctx := context.Background()

	tag, err := tags.Create(ctx, pool, teacher, "  Động   vật quanh bé  ")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tag.Slug != "dong-vat-quanh-be" {
		t.Errorf("slug = %q", tag.Slug)
	}
	if tag.Name != "Động vật quanh bé" {
		t.Errorf("name = %q, want collapsed whitespace", tag.Name)
	}
	if tag.CreatedBy == nil || *tag.CreatedBy != teacher {
		t.Errorf("created_by = %v, want %d", tag.CreatedBy, teacher)
	}

	if _, err := tags.Create(ctx, pool, teacher, "Dong vat quanh be"); !errors.Is(err, tags.ErrTagExists) {
		t.Errorf("duplicate slug: err = %v, want ErrTagExists", err)
	}
}

func TestCreateValidatesName(t *testing.T) {
	pool, teacher, _ := fixture(t)
	ctx := context.Background()

	valid := strings.Repeat("a", 50)
	if _, err := tags.Create(ctx, pool, teacher, valid); err != nil {
		t.Errorf("50 runes rejected: %v", err)
	}

	// Newlines collapse into a single space instead of reaching the public list.
	wrapped, err := tags.Create(ctx, pool, teacher, "line\n  break")
	if err != nil {
		t.Fatalf("newline name rejected: %v", err)
	}
	if wrapped.Name != "line break" {
		t.Errorf("name = %q, want %q", wrapped.Name, "line break")
	}

	for name, claim := range map[string]string{
		"":                      "empty",
		"   ":                   "blank",
		"!!!":                   "no slug",
		"😀😀":                    "no latin letters",
		strings.Repeat("b", 51): "51 runes",
		"co\x00ntrol":           "NUL byte",
	} {
		if _, err := tags.Create(ctx, pool, teacher, name); !errors.Is(err, tags.ErrInvalidTag) {
			t.Errorf("%s (%q): err = %v, want ErrInvalidTag", claim, name, err)
		}
	}
}

func TestDeleteIsOwnerOnlyAndBlocksAssignedTags(t *testing.T) {
	pool, teacher, other := fixture(t)
	ctx := context.Background()

	tag, err := tags.Create(ctx, pool, teacher, "Gia đình")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := tags.Delete(ctx, pool, tag.ID, other); !errors.Is(err, tags.ErrNotFound) {
		t.Errorf("other teacher delete: err = %v, want ErrNotFound", err)
	}

	var bookID int64
	if err := pool.QueryRowContext(ctx,
		`INSERT INTO books (owner_id, slug, title, access_type) VALUES ($1, 'b', 'B', 'free') RETURNING id`,
		teacher,
	).Scan(&bookID); err != nil {
		t.Fatalf("insert book: %v", err)
	}
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO book_tags (book_id, tag_id) VALUES ($1, $2)`, bookID, tag.ID); err != nil {
		t.Fatalf("assign to book: %v", err)
	}
	if err := tags.Delete(ctx, pool, tag.ID, teacher); !errors.Is(err, tags.ErrTagInUse) {
		t.Errorf("book assignment: err = %v, want ErrTagInUse", err)
	}

	if _, err := pool.ExecContext(ctx, `DELETE FROM book_tags WHERE book_id = $1`, bookID); err != nil {
		t.Fatalf("clear assignment: %v", err)
	}

	var bundleID int64
	if err := pool.QueryRowContext(ctx,
		`INSERT INTO bundles (owner_id, slug, title, price_vnd) VALUES ($1, 'bundle', 'Bundle', 1000) RETURNING id`,
		teacher,
	).Scan(&bundleID); err != nil {
		t.Fatalf("insert bundle: %v", err)
	}
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO bundle_tags (bundle_id, tag_id) VALUES ($1, $2)`, bundleID, tag.ID); err != nil {
		t.Fatalf("assign to bundle: %v", err)
	}
	if err := tags.Delete(ctx, pool, tag.ID, teacher); !errors.Is(err, tags.ErrTagInUse) {
		t.Errorf("bundle assignment: err = %v, want ErrTagInUse", err)
	}

	if _, err := pool.ExecContext(ctx, `DELETE FROM bundle_tags WHERE bundle_id = $1`, bundleID); err != nil {
		t.Fatalf("clear bundle assignment: %v", err)
	}
	if err := tags.Delete(ctx, pool, tag.ID, teacher); err != nil {
		t.Errorf("delete unused tag: %v", err)
	}
	if err := tags.Delete(ctx, pool, tag.ID, teacher); !errors.Is(err, tags.ErrNotFound) {
		t.Errorf("delete twice: err = %v, want ErrNotFound", err)
	}
}

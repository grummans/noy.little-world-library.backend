package books

// SQL for the books table lives here so the service reads as business rules.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

const selectBook = `SELECT b.id, b.owner_id, b.slug, b.title, b.description, b.cover_object_key,
	b.epub_object_key, b.access_type, b.price_vnd, b.age_min, b.age_max, b.status,
	b.page_count, b.has_audio, b.has_video, b.published_at, b.updated_at,
	COALESCE((SELECT array_agg(bt.tag_id ORDER BY bt.tag_id)
		FROM book_tags bt WHERE bt.book_id = b.id), '{}') AS tag_ids
	FROM books b`

type scanner interface {
	Scan(dest ...any) error
}

func scanBook(row scanner) (Book, error) {
	var b Book
	err := row.Scan(&b.ID, &b.OwnerID, &b.Slug, &b.Title, &b.Description, &b.CoverKey,
		&b.EpubKey, &b.AccessType, &b.PriceVND, &b.AgeMin, &b.AgeMax, &b.Status,
		&b.PageCount, &b.HasAudio, &b.HasVideo, &b.PublishedAt, &b.UpdatedAt, &b.Tags)
	if err != nil {
		return Book{}, fmt.Errorf("scan book: %w", err)
	}
	return b, nil
}

func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrSlugTaken
	}
	return fmt.Errorf("save book: %w", err)
}

// inPublishedBundle reports whether the book is currently sold inside a published
// bundle. FOR SHARE waits for a concurrent bundle publish to commit, so the answer is
// never stale; the book row itself is locked by the caller.
func inPublishedBundle(ctx context.Context, db querier, bookID int64) (bool, error) {
	var found bool
	err := db.QueryRowContext(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM bundle_books bb
			JOIN bundles b ON b.id = bb.bundle_id
			WHERE bb.book_id = $1 AND b.status = 'published'
			FOR SHARE OF b
		)`, bookID).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("check published bundles: %w", err)
	}
	return found, nil
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// execer is the transaction-or-pool side of the same idea: facts are written either
// alone or inside the replace transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// lockBook pins the book row so a concurrent bundle publish cannot slip between the
// "is it in a bundle?" check and the update.
func lockBook(ctx context.Context, db querier, bookID, ownerID int64) (status, accessType string, err error) {
	err = db.QueryRowContext(ctx,
		`SELECT status, access_type FROM books WHERE id = $1 AND owner_id = $2 FOR UPDATE`,
		bookID, ownerID,
	).Scan(&status, &accessType)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("lock book: %w", err)
	}
	return status, accessType, nil
}

package bundles

// SQL for the bundles tables; the service keeps only business rules.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

const selectBundle = `SELECT b.id, b.owner_id, b.slug, b.title, b.description, b.cover_object_key,
	b.price_vnd, b.status, b.published_at, b.updated_at,
	COALESCE((SELECT array_agg(bt.tag_id ORDER BY bt.tag_id)
		FROM bundle_tags bt WHERE bt.bundle_id = b.id), '{}') AS tag_ids,
	COALESCE((SELECT array_agg(bb.book_id ORDER BY bb.position)
		FROM bundle_books bb WHERE bb.bundle_id = b.id), '{}') AS book_ids
	FROM bundles b`

type scanner interface {
	Scan(dest ...any) error
}

func scanBundle(row scanner) (Bundle, error) {
	var b Bundle
	err := row.Scan(&b.ID, &b.OwnerID, &b.Slug, &b.Title, &b.Description, &b.CoverKey,
		&b.PriceVND, &b.Status, &b.PublishedAt, &b.UpdatedAt, &b.Tags, &b.BookIDs)
	if err != nil {
		return Bundle{}, fmt.Errorf("scan bundle: %w", err)
	}
	return b, nil
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// bookIDsEligible counts how many of the ids the teacher may include. FOR SHARE keeps
// those book rows locked until the caller commits, so a concurrent unpublish cannot
// invalidate a bundle that is being published right now.
func bookIDsEligible(ctx context.Context, db querier, ownerID int64, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	rows, err := db.QueryContext(ctx,
		`SELECT id FROM books
		 WHERE id = ANY($1) AND owner_id = $2 AND status = 'published'
		   AND access_type = 'paid' AND price_vnd > 0
		 FOR SHARE`, ids, ownerID)
	if err != nil {
		return 0, fmt.Errorf("check bundle books: %w", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("check bundle books: %w", err)
	}
	return count, nil
}

func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrSlugTaken
	}
	return fmt.Errorf("save bundle: %w", err)
}

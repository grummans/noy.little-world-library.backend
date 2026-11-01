// Package tags manages teacher-authored topic tags shared across the catalog.
package tags

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgconn"

	"littleworld/internal/slug"
)

var (
	ErrUnknownTag = errors.New("unknown tag")
	ErrNotFound   = errors.New("tag not found")
)

type Tag struct {
	ID        int64
	Slug      string
	Name      string
	CreatedBy *int64
}

// Execer is satisfied by both *sql.DB and *sql.Tx so callers can write tags in the
// same transaction as the row they belong to.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func List(ctx context.Context, db Execer) ([]Tag, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, slug, name, created_by FROM tags ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	list := []Tag{}
	for rows.Next() {
		var tag Tag
		if err := rows.Scan(&tag.ID, &tag.Slug, &tag.Name, &tag.CreatedBy); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		list = append(list, tag)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	return list, nil
}

// SetForBook and SetForBundle require a transaction: the delete-then-insert swap is
// only atomic inside one.
//
// A book has one topic, so this is a clear-or-set rather than a list. The unique index
// on book_tags(book_id) is the real guarantee; this keeps the row in step with it.
func SetForBook(ctx context.Context, tx *sql.Tx, bookID int64, tagID *int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM book_tags WHERE book_id = $1`, bookID); err != nil {
		return fmt.Errorf("clear book tags: %w", err)
	}
	if tagID == nil {
		return nil
	}

	var found int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tags WHERE id = $1`, *tagID).Scan(&found); err != nil {
		return err
	}
	if found == 0 {
		return ErrUnknownTag
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO book_tags (book_id, tag_id) VALUES ($1, $2)`, bookID, *tagID,
	); err != nil {
		return fmt.Errorf("set book tag: %w", err)
	}
	return nil
}

func SetForBundle(ctx context.Context, tx *sql.Tx, bundleID int64, tagIDs []int64) error {
	if err := replace(ctx, tx, "bundle_tags", "bundle_id", bundleID, tagIDs); err != nil {
		return fmt.Errorf("set bundle tags: %w", err)
	}
	return nil
}

// replace validates the ids in one round trip, then swaps the join rows. Table and
// column names come from this file, never from a request.
func replace(ctx context.Context, tx *sql.Tx, table, column string, id int64, tagIDs []int64) error {
	unique := dedupe(tagIDs)

	if len(unique) > 0 {
		var found int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM tags WHERE id = ANY($1)`, unique,
		).Scan(&found); err != nil {
			return err
		}
		if found != len(unique) {
			return ErrUnknownTag
		}
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+table+` WHERE `+column+` = $1`, id,
	); err != nil {
		return err
	}

	for _, tagID := range unique {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+table+` (`+column+`, tag_id) VALUES ($1, $2)`, id, tagID,
		); err != nil {
			return err
		}
	}
	return nil
}

func dedupe(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

var (
	ErrTagExists  = errors.New("tag already exists")
	ErrTagInUse   = errors.New("tag is used by a book or bundle")
	ErrInvalidTag = errors.New("tag name must be 1-50 characters")
)

const maxNameLen = 50

// Create adds a teacher-authored topic. Slugs are global and unique, so the second
// teacher to type an existing name gets ErrTagExists and picks it from the list
// instead of creating a near-duplicate.
func Create(ctx context.Context, db Execer, createdBy int64, name string) (Tag, error) {
	name, err := cleanName(name)
	if err != nil {
		return Tag{}, err
	}

	var tag Tag
	err = db.QueryRowContext(ctx,
		`INSERT INTO tags (slug, name, created_by) VALUES ($1, $2, $3)
		 RETURNING id, slug, name, created_by`,
		slug.Make(name), name, createdBy,
	).Scan(&tag.ID, &tag.Slug, &tag.Name, &tag.CreatedBy)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Tag{}, ErrTagExists
		}
		return Tag{}, fmt.Errorf("insert tag: %w", err)
	}
	return tag, nil
}

// Delete removes a tag the caller created and that nothing references. The RESTRICT
// foreign key decides "in use", so a tag cannot be assigned between a check and the
// delete. Someone else's tag reports ErrNotFound: not found and not yours look alike.
func Delete(ctx context.Context, db Execer, id, callerID int64) error {
	result, err := db.ExecContext(ctx,
		`DELETE FROM tags WHERE id = $1 AND created_by = $2`, id, callerID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrTagInUse
		}
		return fmt.Errorf("delete tag: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// cleanName trims, collapses inner whitespace and rejects control characters before
// they reach the database or the public tag list.
func cleanName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || len([]rune(name)) > maxNameLen {
		return "", ErrInvalidTag
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", ErrInvalidTag
		}
	}
	if slug.Make(name) == "" {
		return "", ErrInvalidTag
	}
	return name, nil
}

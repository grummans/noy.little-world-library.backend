package bundles

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"littleworld/internal/storage"
	"littleworld/internal/tags"
)

func New(db *sql.DB, store Storage) *Service {
	return &Service{db: db, store: store}
}

func (s *Service) Create(ctx context.Context, ownerID int64, in CreateInput) (Bundle, error) {
	meta, err := normalize(in.Metadata)
	if err != nil {
		return Bundle{}, err
	}
	bookIDs, err := validateBookIDs(in.BookIDs)
	if err != nil {
		return Bundle{}, err
	}

	var bundleID int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := validateBundleBooks(ctx, tx, ownerID, bookIDs); err != nil {
			return err
		}

		if err := tx.QueryRowContext(ctx,
			`INSERT INTO bundles (owner_id, slug, title, description, price_vnd)
			 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			ownerID, meta.Slug, meta.Title, meta.Description, *meta.PriceVND,
		).Scan(&bundleID); err != nil {
			return mapWriteError(err)
		}

		if err := s.replaceBooks(ctx, tx, bundleID, bookIDs); err != nil {
			return err
		}
		return tags.SetForBundle(ctx, tx, bundleID, meta.TagIDs)
	})
	if err != nil {
		return Bundle{}, err
	}

	if in.Cover != nil {
		coverKey, err := s.attachCover(ctx, bundleID, in.Cover)
		if err != nil {
			return Bundle{}, errors.Join(err, s.rollback(compensationContext(ctx), bundleID, coverKey))
		}
	}
	return s.Get(ctx, ownerID, bundleID)
}

// Update replaces metadata and the book list; the cover has its own endpoint.
func (s *Service) Update(ctx context.Context, ownerID, bundleID int64, in CreateInput) (Bundle, error) {
	if _, err := s.Get(ctx, ownerID, bundleID); err != nil {
		return Bundle{}, err
	}

	bookIDs, err := validateBookIDs(in.BookIDs)
	if err != nil {
		return Bundle{}, err
	}

	var meta Metadata
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var status string
		if err := tx.QueryRowContext(ctx,
			`SELECT status FROM bundles WHERE id = $1 AND owner_id = $2 FOR UPDATE`,
			bundleID, ownerID,
		).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("lock bundle: %w", err)
		}

		// An edit that does not mention the slug keeps the current one: regenerating it
		// from a renamed title would break every link to a bundle already on sale.
		if strings.TrimSpace(in.Metadata.Slug) == "" {
			if err := tx.QueryRowContext(ctx,
				`SELECT slug FROM bundles WHERE id = $1 AND owner_id = $2`, bundleID, ownerID,
			).Scan(&in.Metadata.Slug); err != nil {
				return fmt.Errorf("read slug: %w", err)
			}
		}
		// Tags are not edited from the studio UI, so an absent list means "leave them".
		if in.Metadata.TagIDs == nil {
			current, err := bundleTagIDs(ctx, tx, bundleID)
			if err != nil {
				return err
			}
			in.Metadata.TagIDs = current
		}

		var err error
		if meta, err = normalize(in.Metadata); err != nil {
			return err
		}

		currentBookIDs, err := bookIDsIn(ctx, tx, bundleID)
		if err != nil {
			return err
		}
		sameBooks := sameOrder(currentBookIDs, bookIDs)
		if !sameBooks {
			if status == StatusPublished {
				return fmt.Errorf("%w: a published bundle cannot change its books", ErrValidation)
			}
			sold, err := hasPaidOrders(ctx, tx, bundleID)
			if err != nil {
				return err
			}
			if sold {
				return fmt.Errorf("%w: this bundle has been sold, its books are fixed", ErrValidation)
			}
		}

		if err := validateBundleBooks(ctx, tx, ownerID, bookIDs); err != nil {
			return err
		}
		if status == StatusPublished && len(bookIDs) == 0 {
			return fmt.Errorf("%w: a published bundle needs at least one book", ErrNotPublishable)
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE bundles SET slug = $1, title = $2, description = $3, price_vnd = $4
			 WHERE id = $5 AND owner_id = $6`,
			meta.Slug, meta.Title, meta.Description, *meta.PriceVND, bundleID, ownerID,
		); err != nil {
			return mapWriteError(err)
		}

		if !sameBooks {
			if err := s.replaceBooks(ctx, tx, bundleID, bookIDs); err != nil {
				return err
			}
		}
		return tags.SetForBundle(ctx, tx, bundleID, meta.TagIDs)
	})
	if err != nil {
		return Bundle{}, err
	}
	return s.Get(ctx, ownerID, bundleID)
}

func (s *Service) ReplaceCover(ctx context.Context, ownerID, bundleID int64, up Upload) (Bundle, error) {
	if _, err := s.Get(ctx, ownerID, bundleID); err != nil {
		return Bundle{}, err
	}
	if _, err := s.attachCover(ctx, bundleID, &up); err != nil {
		return Bundle{}, err
	}
	return s.Get(ctx, ownerID, bundleID)
}

// Publish locks the bundle and re-checks its books inside one transaction, then flips
// the status before committing: the lock must still be held when it happens.
func (s *Service) Publish(ctx context.Context, ownerID, bundleID int64) (Bundle, error) {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var status string
		if err := tx.QueryRowContext(ctx,
			`SELECT status FROM bundles WHERE id = $1 AND owner_id = $2 FOR UPDATE`,
			bundleID, ownerID,
		).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("lock bundle: %w", err)
		}

		bookIDs, err := bookIDsIn(ctx, tx, bundleID)
		if err != nil {
			return err
		}
		if len(bookIDs) == 0 {
			return fmt.Errorf("%w: add at least one book", ErrNotPublishable)
		}
		if err := validateBundleBooks(ctx, tx, ownerID, bookIDs); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE bundles SET status = $1, published_at = now()
			 WHERE id = $2 AND owner_id = $3`,
			StatusPublished, bundleID, ownerID,
		); err != nil {
			return fmt.Errorf("publish bundle: %w", err)
		}
		return nil
	})
	if err != nil {
		return Bundle{}, err
	}
	return s.Get(ctx, ownerID, bundleID)
}

// bookIDsIn reads the ordered book ids as stored, without trusting the caller.
func bookIDsIn(ctx context.Context, db querier, bundleID int64) ([]int64, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT book_id FROM bundle_books WHERE bundle_id = $1 ORDER BY position`, bundleID)
	if err != nil {
		return nil, fmt.Errorf("read bundle books: %w", err)
	}
	defer rows.Close()

	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan bundle book: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read bundle books: %w", err)
	}
	return ids, nil
}

func (s *Service) Unpublish(ctx context.Context, ownerID, bundleID int64) (Bundle, error) {
	if _, err := s.Get(ctx, ownerID, bundleID); err != nil {
		return Bundle{}, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE bundles SET status = $1, published_at = NULL WHERE id = $2 AND owner_id = $3`,
		StatusDraft, bundleID, ownerID,
	); err != nil {
		return Bundle{}, fmt.Errorf("unpublish bundle: %w", err)
	}
	return s.Get(ctx, ownerID, bundleID)
}

func (s *Service) Get(ctx context.Context, ownerID, bundleID int64) (Bundle, error) {
	row := s.db.QueryRowContext(ctx, selectBundle+` WHERE b.id = $1 AND b.owner_id = $2`,
		bundleID, ownerID)

	bundle, err := scanBundle(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Bundle{}, ErrNotFound
	}
	if err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func (s *Service) ListByOwner(ctx context.Context, ownerID int64) ([]Bundle, error) {
	rows, err := s.db.QueryContext(ctx,
		selectBundle+` WHERE b.owner_id = $1 ORDER BY b.updated_at DESC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list bundles: %w", err)
	}
	defer rows.Close()

	list := []Bundle{}
	for rows.Next() {
		bundle, err := scanBundle(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, bundle)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bundles: %w", err)
	}
	return list, nil
}

// validateBundleBooks rejects books that are not the teacher's own published paid
// books, before any row is written.
func validateBundleBooks(ctx context.Context, tx *sql.Tx, ownerID int64, bookIDs []int64) error {
	if len(bookIDs) == 0 {
		return nil
	}

	eligible, err := bookIDsEligible(ctx, tx, ownerID, bookIDs)
	if err != nil {
		return err
	}
	if eligible != len(bookIDs) {
		return ErrInvalidBook
	}
	return nil
}

func (s *Service) replaceBooks(ctx context.Context, tx *sql.Tx, bundleID int64, bookIDs []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM bundle_books WHERE bundle_id = $1`, bundleID); err != nil {
		return fmt.Errorf("clear bundle books: %w", err)
	}
	for i, bookID := range bookIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO bundle_books (bundle_id, book_id, position) VALUES ($1, $2, $3)`,
			bundleID, bookID, i+1,
		); err != nil {
			return fmt.Errorf("add bundle book: %w", err)
		}
	}
	return nil
}

// attachCover stages and validates the image before promoting it to the bundle key.
func (s *Service) attachCover(ctx context.Context, bundleID int64, up *Upload) (string, error) {
	if s.store == nil {
		return "", storage.ErrNotConfigured
	}
	if err := storage.ValidateCover(up.ContentType, up.Size); err != nil {
		return "", err
	}
	finalKey, err := storage.BundleCoverKey(bundleID, up.ContentType)
	if err != nil {
		return "", err
	}

	stagedKey, err := s.stage(ctx, up)
	if err != nil {
		return "", err
	}

	oldKey, err := s.coverKey(ctx, bundleID)
	if err != nil {
		_ = s.store.Delete(ctx, stagedKey)
		return "", err
	}

	if err := s.store.Promote(ctx, stagedKey, finalKey); err != nil {
		return "", errors.Join(err, s.store.Delete(ctx, stagedKey))
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE bundles SET cover_object_key = $1 WHERE id = $2`, finalKey, bundleID,
	); err != nil {
		return finalKey, fmt.Errorf("store cover key: %w", err)
	}

	// A new extension leaves the previous cover object under its old key.
	if oldKey != "" && oldKey != finalKey {
		// shortcut: an orphaned old cover stays until a janitor runs; the new cover
		// is already live, so this must not fail the request.
		_ = s.store.Delete(ctx, oldKey)
	}
	return finalKey, nil
}

func (s *Service) stage(ctx context.Context, up *Upload) (string, error) {
	token := make([]byte, 12)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("staging token: %w", err)
	}
	stagedKey := storage.StagingKey(hex.EncodeToString(token))

	if err := s.store.Put(ctx, stagedKey, up.Body, up.Size, up.ContentType); err != nil {
		return "", err
	}

	obj, err := s.store.Open(ctx, stagedKey)
	if err != nil {
		return "", errors.Join(err, s.store.Delete(ctx, stagedKey))
	}
	validationErr := storage.ValidateCoverImage(obj, up.Size, up.ContentType)
	obj.Close()
	if validationErr != nil {
		return "", errors.Join(validationErr, s.store.Delete(ctx, stagedKey))
	}
	return stagedKey, nil
}

// rollback removes a bundle that could not be completed (draft only) plus the exact
// objects promoted for it.
func (s *Service) rollback(ctx context.Context, bundleID int64, keys ...string) error {
	var errs []error
	if s.store != nil {
		for _, key := range keys {
			if key == "" {
				continue
			}
			errs = append(errs, s.store.Delete(ctx, key))
		}
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM bundles WHERE id = $1 AND status = $2`, bundleID, StatusDraft,
	); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Service) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// compensationContext detaches cleanup from the request context, so a canceled
// client request cannot stop us from deleting objects we created.
func compensationContext(ctx context.Context) context.Context {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	_ = cancel // released when the timeout fires
	return cleanupCtx
}

func (s *Service) coverKey(ctx context.Context, bundleID int64) (string, error) {
	var key sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT cover_object_key FROM bundles WHERE id = $1`, bundleID,
	).Scan(&key); err != nil {
		return "", fmt.Errorf("read cover key: %w", err)
	}
	return key.String, nil
}

// bundleTagIDs reads the tags attached to a bundle so an edit can leave them untouched.
func bundleTagIDs(ctx context.Context, db querier, bundleID int64) ([]int64, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT tag_id FROM bundle_tags WHERE bundle_id = $1 ORDER BY tag_id`, bundleID)
	if err != nil {
		return nil, fmt.Errorf("read bundle tags: %w", err)
	}
	defer rows.Close()

	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan bundle tag: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// sameOrder compares two book lists position by position: the order is what the buyer
// sees, so a reorder is a real edit.
func sameOrder(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// hasPaidOrders reports whether anyone bought the bundle: from then on its contents are
// part of what they paid for.
func hasPaidOrders(ctx context.Context, db querier, bundleID int64) (bool, error) {
	var sold bool
	err := db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM orders WHERE bundle_id = $1 AND status = 'paid')`,
		bundleID).Scan(&sold)
	if err != nil {
		return false, fmt.Errorf("read bundle orders: %w", err)
	}
	return sold, nil
}

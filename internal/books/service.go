package books

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

func (s *Service) Create(ctx context.Context, ownerID int64, in CreateInput) (Book, error) {
	meta, err := normalize(in.Metadata)
	if err != nil {
		return Book{}, err
	}

	var bookID int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO books (owner_id, slug, title, description, access_type, price_vnd, age_min, age_max)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 RETURNING id`,
			ownerID, meta.Slug, meta.Title, meta.Description, meta.AccessType,
			meta.PriceVND, meta.AgeMin, meta.AgeMax,
		).Scan(&bookID); err != nil {
			return mapWriteError(err)
		}
		return tags.SetForBook(ctx, tx, bookID, meta.TagID)
	})
	if err != nil {
		return Book{}, err
	}

	// Nothing is promoted to a real book key until the file passed validation, so a
	// failed upload never replaces a published book's EPUB.
	keys, err := s.attachFiles(ctx, bookID, in)
	if err != nil {
		return Book{}, errors.Join(err, s.rollback(s.compensationContext(ctx), bookID, keys))
	}

	return s.Get(ctx, ownerID, bookID)
}

// withTx runs fn in a transaction, rolling back on any error.
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

func (s *Service) Update(ctx context.Context, ownerID, bookID int64, meta Metadata) (Book, error) {
	var normalized Metadata

	// Lock, check and write in one transaction: otherwise a bundle can be published
	// between the "is it in a bundle?" check and the update.
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		status, accessType, err := lockBook(ctx, tx, bookID, ownerID)
		if err != nil {
			return err
		}

		// An edit that does not mention the slug keeps the current one: regenerating it
		// from a renamed title would silently break every published link and bookmark.
		if strings.TrimSpace(meta.Slug) == "" {
			if err := tx.QueryRowContext(ctx,
				`SELECT slug FROM books WHERE id = $1 AND owner_id = $2`, bookID, ownerID,
			).Scan(&meta.Slug); err != nil {
				return fmt.Errorf("read slug: %w", err)
			}
		}
		if normalized, err = normalize(meta); err != nil {
			return err
		}
		// The database enforces this too, but a clear message beats a constraint error.
		if status == StatusPublished && normalized.Description == "" {
			return fmt.Errorf("%w: a published book needs a description", ErrValidation)
		}
		if normalized.AccessType == AccessFree && accessType != AccessFree {
			inBundle, err := inPublishedBundle(ctx, tx, bookID)
			if err != nil {
				return err
			}
			// A book sold inside a published bundle cannot silently become free.
			if inBundle {
				return fmt.Errorf("%w: the book is inside a published bundle", ErrValidation)
			}
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE books
			 SET slug = $1, title = $2, description = $3, access_type = $4,
			     price_vnd = $5, age_min = $6, age_max = $7
			 WHERE id = $8 AND owner_id = $9`,
			normalized.Slug, normalized.Title, normalized.Description, normalized.AccessType,
			normalized.PriceVND, normalized.AgeMin, normalized.AgeMax, bookID, ownerID,
		); err != nil {
			return mapWriteError(err)
		}
		return tags.SetForBook(ctx, tx, bookID, normalized.TagID)
	})
	if err != nil {
		return Book{}, err
	}
	return s.Get(ctx, ownerID, bookID)
}

func (s *Service) Unpublish(ctx context.Context, ownerID, bookID int64) (Book, error) {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := lockBook(ctx, tx, bookID, ownerID); err != nil {
			return err
		}
		inBundle, err := inPublishedBundle(ctx, tx, bookID)
		if err != nil {
			return err
		}
		if inBundle {
			return fmt.Errorf("%w: the book is inside a published bundle", ErrValidation)
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE books SET status = $1, published_at = NULL WHERE id = $2 AND owner_id = $3`,
			StatusDraft, bookID, ownerID,
		); err != nil {
			return fmt.Errorf("unpublish book: %w", err)
		}
		return nil
	})
	if err != nil {
		return Book{}, err
	}
	return s.Get(ctx, ownerID, bookID)
}

// ReplaceFiles swaps the EPUB and/or cover of an existing book after validating them.
func (s *Service) ReplaceFiles(ctx context.Context, ownerID, bookID int64, in CreateInput) (Book, error) {
	if _, err := s.Get(ctx, ownerID, bookID); err != nil {
		return Book{}, err
	}
	if _, err := s.attachFiles(ctx, bookID, in); err != nil {
		return Book{}, err
	}
	return s.Get(ctx, ownerID, bookID)
}

// Publish locks the book and re-checks the publish rules inside one transaction, so a
// concurrent unpublish cannot pass the checks and leave a half-published book.
func (s *Service) Publish(ctx context.Context, ownerID, bookID int64) (Book, error) {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := lockBook(ctx, tx, bookID, ownerID); err != nil {
			return err
		}

		var (
			description string
			hasCover    bool
			hasEPUB     bool
		)
		if err := tx.QueryRowContext(ctx,
			`SELECT description, cover_object_key IS NOT NULL, epub_object_key IS NOT NULL
			 FROM books WHERE id = $1`,
			bookID,
		).Scan(&description, &hasCover, &hasEPUB); err != nil {
			return fmt.Errorf("read book: %w", err)
		}
		if !hasCover || !hasEPUB || description == "" {
			return fmt.Errorf("%w: needs cover, EPUB and description", ErrNotPublishable)
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE books SET status = $1, published_at = now() WHERE id = $2 AND owner_id = $3`,
			StatusPublished, bookID, ownerID,
		); err != nil {
			return fmt.Errorf("publish book: %w", err)
		}
		return nil
	})
	if err != nil {
		return Book{}, err
	}
	return s.Get(ctx, ownerID, bookID)
}

func (s *Service) Get(ctx context.Context, ownerID, bookID int64) (Book, error) {
	row := s.db.QueryRowContext(ctx, selectBook+` WHERE b.id = $1 AND b.owner_id = $2`, bookID, ownerID)

	book, err := scanBook(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Book{}, ErrNotFound
	}
	if err != nil {
		return Book{}, err
	}
	return book, nil
}

func (s *Service) ListByOwner(ctx context.Context, ownerID int64) ([]Book, error) {
	rows, err := s.db.QueryContext(ctx,
		selectBook+` WHERE b.owner_id = $1 ORDER BY b.updated_at DESC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list books: %w", err)
	}
	defer rows.Close()

	books := []Book{}
	for rows.Next() {
		book, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		books = append(books, book)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list books: %w", err)
	}
	return books, nil
}

// attachFiles stores new book files. It returns the final keys it promoted so the
// caller can compensate with the exact keys on later failure.
func (s *Service) attachFiles(ctx context.Context, bookID int64, in CreateInput) ([]string, error) {
	if s.store == nil {
		if in.Epub == nil && in.Cover == nil {
			return nil, nil
		}
		return nil, storage.ErrNotConfigured
	}

	type promoted struct {
		stagedKey string
		finalKey  string
		column    string
	}

	var pending []promoted
	discardStaged := func() error {
		var errs []error
		for _, p := range pending {
			// shortcut: a failed cleanup leaves an uploads/ orphan; add a janitor with
			// an age sweep once uploads are frequent.
			errs = append(errs, s.store.Delete(ctx, p.stagedKey))
		}
		return errors.Join(errs...)
	}

	// Phase 1: stage and validate every file. Nothing reaches a real book key until
	// all of them are valid, so a bad cover cannot leave a book with a new EPUB.
	if in.Epub != nil {
		if err := storage.ValidateEPUB(in.Epub.ContentType, in.Epub.Size); err != nil {
			return nil, err
		}
		stagedKey, err := s.stage(ctx, in.Epub, storage.EpubContentType, func(obj storage.Object) error {
			return storage.ValidateEPUBArchive(obj, in.Epub.Size)
		})
		if err != nil {
			return nil, errors.Join(err, discardStaged())
		}
		pending = append(pending, promoted{stagedKey, storage.EpubKey(bookID), "epub_object_key"})
	}

	if in.Cover != nil {
		if err := storage.ValidateCover(in.Cover.ContentType, in.Cover.Size); err != nil {
			return nil, errors.Join(err, discardStaged())
		}
		finalKey, err := storage.BookCoverKey(bookID, in.Cover.ContentType)
		if err != nil {
			return nil, errors.Join(err, discardStaged())
		}
		stagedKey, err := s.stage(ctx, in.Cover, in.Cover.ContentType, func(obj storage.Object) error {
			return storage.ValidateCoverImage(obj, in.Cover.Size, in.Cover.ContentType)
		})
		if err != nil {
			return nil, errors.Join(err, discardStaged())
		}
		pending = append(pending, promoted{stagedKey, finalKey, "cover_object_key"})
	}
	if len(pending) == 0 {
		return nil, nil
	}

	oldCoverKey, err := s.coverKey(ctx, bookID)
	if err != nil {
		return nil, err
	}

	// Phase 2: swap the files in. Column names come from this function, not input.
	keys := make([]string, 0, len(pending))

	// The row lock is taken before promoting: otherwise a second replace could overwrite
	// an object while this request is still deriving page facts from it.
	//
	// shortcut: a copy that succeeds but whose commit then fails leaves the new bytes at
	// the fixed key with stale facts; upgrading needs a backup key and a restore step.
	if err := s.withTx(ctx, func(tx *sql.Tx) error {
		var locked int64
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM books WHERE id = $1 FOR UPDATE`, bookID,
		).Scan(&locked); err != nil {
			return fmt.Errorf("lock book: %w", err)
		}

		for _, p := range pending {
			if err := s.store.Promote(ctx, p.stagedKey, p.finalKey); err != nil {
				return errors.Join(err, discardStaged())
			}
			keys = append(keys, p.finalKey)
			if _, err := tx.ExecContext(ctx,
				`UPDATE books SET `+p.column+` = $1 WHERE id = $2`, p.finalKey, bookID,
			); err != nil {
				return fmt.Errorf("store %s: %w", p.column, err)
			}
		}

		// Page count and media flags come from the file itself, so they always match
		// what a reader will actually see.
		if in.Epub != nil {
			return s.recordFacts(ctx, tx, bookID)
		}
		return nil
	}); err != nil {
		return keys, err
	}

	// A cover with a new extension leaves the old file behind under its old key.
	if newCoverKey, err := coverFinalKey(bookID, in); err == nil && oldCoverKey != "" && oldCoverKey != newCoverKey {
		// shortcut: an orphaned old cover stays until a janitor runs; the new cover
		// is already live, so this must not fail the request.
		_ = s.store.Delete(ctx, oldCoverKey)
	}
	return keys, nil
}

func (s *Service) coverKey(ctx context.Context, bookID int64) (string, error) {
	var key sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT cover_object_key FROM books WHERE id = $1`, bookID,
	).Scan(&key); err != nil {
		return "", fmt.Errorf("read cover key: %w", err)
	}
	return key.String, nil
}

// rollback removes a newly created draft and the objects promoted for it.
func (s *Service) rollback(ctx context.Context, bookID int64, keys []string) error {
	var errs []error
	if s.store != nil {
		for _, key := range keys {
			errs = append(errs, s.store.Delete(ctx, key))
		}
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM books WHERE id = $1 AND status = $2`,
		bookID, StatusDraft); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func stagingToken() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("staging token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// compensationContext detaches cleanup from the request context: a canceled client
// request must not stop us from deleting objects we already created.
func (s *Service) compensationContext(ctx context.Context) context.Context {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	_ = cancel // released when the timeout fires
	return cleanupCtx
}

// stage uploads one file to a throwaway key and validates the stored bytes; it
// never touches the final key, so callers can validate everything before promoting.
func (s *Service) stage(ctx context.Context, up *Upload, contentType string, validate func(storage.Object) error) (string, error) {
	token, err := stagingToken()
	if err != nil {
		return "", err
	}
	stagedKey := storage.StagingKey(token)

	if err := s.store.Put(ctx, stagedKey, up.Body, up.Size, contentType); err != nil {
		return "", err
	}

	obj, err := s.store.Open(ctx, stagedKey)
	if err != nil {
		return "", errors.Join(err, s.store.Delete(ctx, stagedKey))
	}
	validationErr := validate(obj)
	obj.Close()
	if validationErr != nil {
		return "", errors.Join(validationErr, s.store.Delete(ctx, stagedKey))
	}

	return stagedKey, nil
}

// coverFinalKey is the cover key for this request, or "" when no cover was sent.
func coverFinalKey(bookID int64, in CreateInput) (string, error) {
	if in.Cover == nil {
		return "", nil
	}
	key, err := storage.BookCoverKey(bookID, in.Cover.ContentType)
	if err != nil {
		return "", err
	}
	return key, nil
}

// recordFacts inspects the stored EPUB and saves what it contains. A book whose package
// cannot be read keeps NULL page count: the UI then hides the chip rather than lying.
func (s *Service) recordFacts(ctx context.Context, db execer, bookID int64) error {
	key := storage.EpubKey(bookID)

	info, err := s.store.Stat(ctx, key)
	if err != nil {
		return fmt.Errorf("read epub size: %w", err)
	}

	obj, err := s.store.Open(ctx, key)
	if err != nil {
		return fmt.Errorf("open epub: %w", err)
	}
	facts, err := storage.InspectEPUB(obj, info)
	obj.Close()
	if err != nil {
		return fmt.Errorf("%w: cannot read package document", storage.ErrInvalidEpub)
	}

	// smallint column: clamp so a pathological spine cannot overflow the cast.
	pageCount := int16(min(facts.PageCount, 32767))

	if _, err := db.ExecContext(ctx,
		`UPDATE books SET page_count = $1, has_audio = $2, has_video = $3 WHERE id = $4`,
		pageCount, facts.HasAudio, facts.HasVideo, bookID,
	); err != nil {
		return fmt.Errorf("store book facts: %w", err)
	}
	return nil
}

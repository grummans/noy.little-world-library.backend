// Package reading decides who may open a book's EPUB and mints the URL that serves it.
// A book's object key never leaves the server: clients get a short-lived signed URL.
package reading

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"littleworld/internal/catalog"
	"littleworld/internal/storage"
)

var (
	ErrNotFound    = errors.New("book not found")
	ErrForbidden   = errors.New("you do not own this book")
	ErrUnavailable = errors.New("this book has no readable file")
)

// Presigner mints the signed URL; *storage.Client satisfies it.
type Presigner interface {
	PresignGet(ctx context.Context, key string) (string, error)
}

type Service struct {
	db        *sql.DB
	presigner Presigner
}

func New(db *sql.DB, presigner Presigner) *Service {
	return &Service{db: db, presigner: presigner}
}

// Session is what the reader needs: a URL plus why access was granted.
type Session struct {
	URL       string
	Source    string // free | purchase | bundle
	ExpiresIn int
}

// Open returns a signed EPUB URL when userID (0 = anonymous) may read the book.
//
// Rules: a free published book is readable by anyone; a paid book needs a paid order
// for that book, or a paid order for a bundle that contains it. Drafts and unpublished
// books are invisible here even if a URL is guessed.
func (s *Service) Open(ctx context.Context, userID int64, bookSlug string) (Session, error) {
	var (
		bookID     int64
		accessType string
		epubKey    sql.NullString
		bought     bool
		inBundle   bool
	)
	err := s.db.QueryRowContext(ctx, accessQuery, bookSlug, userID).Scan(
		&bookID, &accessType, &epubKey, &bought, &inBundle,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("check reading access: %w", err)
	}

	source := ""
	switch {
	case accessType == catalog.AccessFree:
		source = "free"
	case bought:
		source = "purchase"
	case inBundle:
		source = "bundle"
	default:
		return Session{}, ErrForbidden
	}

	if !epubKey.Valid || epubKey.String == "" {
		return Session{}, ErrUnavailable
	}
	if s.presigner == nil {
		return Session{}, ErrUnavailable
	}

	url, err := s.presigner.PresignGet(ctx, epubKey.String)
	if err != nil {
		// Signing is a storage problem, not an access problem: callers get 503.
		return Session{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return Session{URL: url, Source: source, ExpiresIn: int(storage.PresignTTL.Seconds())}, nil
}

// Package bundles owns book bundles: a paid product made of one teacher's own
// published, paid books. Same file layout as the books package.
package bundles

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"time"

	"littleworld/internal/storage"
)

const (
	StatusDraft     = "draft"
	StatusPublished = "published"

	maxSlugLen        = 80
	maxTitleLen       = 200
	maxDescriptionLen = 5000
	maxBooks          = 50
)

var (
	ErrValidation     = errors.New("validation failed")
	ErrNotFound       = errors.New("bundle not found")
	ErrSlugTaken      = errors.New("slug already used")
	ErrNotPublishable = errors.New("bundle is not ready to publish")
	ErrInvalidBook    = errors.New("every book must be a published paid book of the same teacher")
)

// Storage is the slice of object storage bundles need; *storage.Client satisfies it.
type Storage interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Open(ctx context.Context, key string) (storage.Object, error)
	Promote(ctx context.Context, fromKey, toKey string) error
	Delete(ctx context.Context, key string) error
}

type Service struct {
	db    *sql.DB
	store Storage
}

type Upload struct {
	ContentType string
	Size        int64
	Body        io.Reader
}

type Metadata struct {
	Slug        string
	Title       string
	Description string
	PriceVND    *int64
	TagIDs      []int64
}

type CreateInput struct {
	Metadata
	// BookIDs are ordered; position 1 is the first book shown in the bundle.
	BookIDs []int64
	Cover   *Upload
}

type Bundle struct {
	ID          int64
	OwnerID     int64
	Slug        string
	Title       string
	Description string
	CoverKey    *string
	PriceVND    int64
	Status      string
	Tags        []int64
	BookIDs     []int64
	PublishedAt *time.Time
	UpdatedAt   time.Time
}

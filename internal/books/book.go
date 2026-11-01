// Package books owns teacher book drafts, publishing rules and, later, the public
// catalog. Object bytes live in storage; the database only holds object keys.
//
// One package, several files (book.go, validate.go, store.go, service.go): Go has no
// Controller/Service/Repository layers — the package is the boundary, files are not.
package books

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"time"

	"littleworld/internal/storage"
)

const (
	AccessFree = "free"
	AccessPaid = "paid"

	StatusDraft     = "draft"
	StatusPublished = "published"

	maxSlugLen        = 80
	maxTitleLen       = 200
	maxDescriptionLen = 5000
	maxAge            = 18
)

var (
	ErrValidation     = errors.New("validation failed")
	ErrNotFound       = errors.New("book not found")
	ErrSlugTaken      = errors.New("slug already used")
	ErrNotPublishable = errors.New("book is not ready to publish")
)

// Storage is the slice of object storage the service needs; *storage.Client
// satisfies it and tests use an in-memory fake.
type Storage interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Stat(ctx context.Context, key string) (int64, error)
	Open(ctx context.Context, key string) (storage.Object, error)
	Promote(ctx context.Context, fromKey, toKey string) error
	Delete(ctx context.Context, key string) error
	PresignGet(ctx context.Context, key string) (string, error)
}

type Service struct {
	db    *sql.DB
	store Storage
}

func New(db *sql.DB, store Storage) *Service {
	return &Service{db: db, store: store}
}

// Upload is a file arriving with a request; the service validates and stores it.
type Upload struct {
	ContentType string
	Size        int64
	Body        io.Reader
}

type Metadata struct {
	Slug        string
	Title       string
	Description string
	AccessType  string
	PriceVND    *int64
	AgeMin      *int16
	AgeMax      *int16
	TagID       *int64 // one topic per book; bundles keep a list
}

type CreateInput struct {
	Metadata
	Epub  *Upload
	Cover *Upload
}

type Book struct {
	ID          int64
	OwnerID     int64
	Slug        string
	Title       string
	Description string
	CoverKey    *string
	EpubKey     *string
	AccessType  string
	PriceVND    *int64
	AgeMin      *int16
	AgeMax      *int16
	Status      string
	PageCount   *int16
	HasAudio    bool
	HasVideo    bool
	Tags        []int64
	PublishedAt *time.Time
	UpdatedAt   time.Time
}

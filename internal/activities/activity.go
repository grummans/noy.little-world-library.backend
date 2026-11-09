// Package activities owns teacher Wordwall game records: metadata, the verified embed
// URL, an optional thumbnail and the draft/hidden/published state. The game itself is
// authored on Wordwall; this package never stores or renders embed HTML.
//
// One package, several files (activity.go, validate.go, store.go, service.go): the
// package is the boundary, files are not layers.
package activities

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"time"
)

const (
	StatusDraft     = "draft"
	StatusPublished = "published"
	StatusHidden    = "hidden"

	maxSlugLen        = 80
	maxTitleLen       = 120
	maxDescriptionLen = 400
	maxGameTypeLen    = 60
	maxTopicLen       = 80
	maxSkillLen       = 40
	maxSkills         = 8
	maxAge            = 18

	// DefaultLimit is the public catalog page size.
	DefaultLimit = 12
	maxPage      = 1000
)

var (
	ErrValidation     = errors.New("validation failed")
	ErrNotFound       = errors.New("activity not found")
	ErrSlugTaken      = errors.New("slug already used")
	ErrNotPublishable = errors.New("activity is not ready to publish")
)

// Storage is the slice of object storage the service needs; *storage.Client satisfies
// it and tests use an in-memory fake.
type Storage interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
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

// Upload is a thumbnail arriving with a request.
type Upload struct {
	ContentType string
	Size        int64
	Body        io.Reader
}

type Metadata struct {
	Slug        string
	Title       string
	Description string
	EmbedURL    string
	GameType    string
	Topic       string
	Skills      []string
	AgeMin      *int16
	AgeMax      *int16
}

type Activity struct {
	ID           int64
	OwnerID      int64
	Slug         string
	Title        string
	Description  string
	EmbedURL     string
	GameType     string
	Topic        string
	Skills       []string
	AgeMin       *int16
	AgeMax       *int16
	ThumbnailKey *string
	ThumbnailURL string
	Status       string
	PublishedAt  *time.Time
	UpdatedAt    time.Time
}

// Stats is the Teacher Studio summary row; one query fills all four counts.
type Stats struct {
	Total     int `json:"total"`
	Published int `json:"published"`
	Draft     int `json:"draft"`
	Hidden    int `json:"hidden"`
}

type Filter struct {
	Query    string
	GameType string
	Skill    string
	Age      *int16
	Page     int
	Limit    int
}

// Facets lists the filter values that actually exist among published games.
type Facets struct {
	GameTypes []string `json:"game_types"`
	Skills    []string `json:"skills"`
}

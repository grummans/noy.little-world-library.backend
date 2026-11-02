// Package catalog serves the public storefront: published books and bundles only.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"littleworld/internal/slug"
)

const (
	KindBook   = "book"
	KindBundle = "bundle"
	KindAll    = "all"

	AccessFree = "free"
	AccessPaid = "paid"

	MaxLimit     = 50
	DefaultLimit = 12
)

var (
	ErrValidation = errors.New("validation failed")
	ErrNotFound   = errors.New("not found")
)

// Presigner mints a short-lived URL for a private object; *storage.Client satisfies it.
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

// Filter drives the public library. Zero values mean "no filter".
type Filter struct {
	Kind    string
	Query   string
	TagSlug string
	Age     *int16
	Page    int
	Limit   int
}

type Card struct {
	Kind        string
	ID          int64
	Slug        string
	Title       string
	CoverKey    string
	CoverURL    string
	AccessType  string
	PriceVND    *int64
	AgeMin      *int16
	AgeMax      *int16
	TagSlugs    []string
	PageCount   *int16
	HasAudio    bool
	HasVideo    bool
	BookCount   int
	PublishedAt time.Time
}

type BookDetail struct {
	Card
	Description string
	HasEPUB     bool
}

type BundleDetail struct {
	Card
	Description string
	Books       []Card
}

// List returns one page of published items, newest first, plus whether more exist.
func (s *Service) List(ctx context.Context, filter Filter) ([]Card, bool, error) {
	normalized, err := normalize(filter)
	if err != nil {
		return nil, false, err
	}

	rows, err := s.db.QueryContext(ctx, listQuery, normalized.Kind, slug.Make(normalized.Query),
		escapeLike(normalized.Query), normalized.TagSlug, normalized.Age,
		normalized.Limit+1, (normalized.Page-1)*normalized.Limit)
	if err != nil {
		return nil, false, fmt.Errorf("list catalog: %w", err)
	}
	defer rows.Close()

	cards := []Card{}
	for rows.Next() {
		card, err := ScanCard(rows)
		if err != nil {
			return nil, false, err
		}
		cards = append(cards, card)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list catalog: %w", err)
	}

	hasMore := len(cards) > normalized.Limit
	if hasMore {
		cards = cards[:normalized.Limit]
	}
	s.AttachCovers(ctx, cards)
	return cards, hasMore, nil
}

func (s *Service) Book(ctx context.Context, bookSlug string) (BookDetail, error) {
	var (
		card        Card
		description string
		coverKey    sql.NullString
		pageCount   sql.NullInt64
		hasEPUB     bool
	)
	err := s.db.QueryRowContext(ctx, bookDetailQuery, bookSlug).Scan(
		&card.ID, &card.Slug, &card.Title, &description, &coverKey, &card.AccessType, &card.PriceVND,
		&card.AgeMin, &card.AgeMax, &pageCount, &card.HasAudio, &card.HasVideo,
		&card.PublishedAt, &card.TagSlugs, &hasEPUB,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return BookDetail{}, ErrNotFound
	}
	if err != nil {
		return BookDetail{}, fmt.Errorf("book detail: %w", err)
	}

	card.Kind = KindBook
	card.CoverKey = coverKey.String
	if pageCount.Valid {
		value := int16(pageCount.Int64)
		card.PageCount = &value
	}
	card = attachCard(ctx, s, card)

	return BookDetail{Card: card, Description: description, HasEPUB: hasEPUB}, nil
}

func (s *Service) Bundle(ctx context.Context, bundleSlug string) (BundleDetail, error) {
	var (
		card        Card
		description string
		coverKey    sql.NullString
	)
	err := s.db.QueryRowContext(ctx, bundleDetailQuery, bundleSlug).Scan(
		&card.ID, &card.Slug, &card.Title, &description, &coverKey, &card.PriceVND,
		&card.PublishedAt, &card.TagSlugs, &card.BookCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return BundleDetail{}, ErrNotFound
	}
	if err != nil {
		return BundleDetail{}, fmt.Errorf("bundle detail: %w", err)
	}

	card.Kind = KindBundle
	card.AccessType = AccessPaid
	card.CoverKey = coverKey.String
	card = attachCard(ctx, s, card)

	books, err := s.bundleBooks(ctx, card.ID)
	if err != nil {
		return BundleDetail{}, err
	}
	return BundleDetail{Card: card, Description: description, Books: books}, nil
}

// bundleBooks lists the published books of a bundle in the teacher's order.
func (s *Service) bundleBooks(ctx context.Context, bundleID int64) ([]Card, error) {
	rows, err := s.db.QueryContext(ctx, bundleBooksQuery, bundleID)
	if err != nil {
		return nil, fmt.Errorf("bundle books: %w", err)
	}
	defer rows.Close()

	books := []Card{}
	for rows.Next() {
		card, err := ScanCard(rows)
		if err != nil {
			return nil, err
		}
		books = append(books, card)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bundle books: %w", err)
	}

	s.AttachCovers(ctx, books)
	return books, nil
}

// AttachCovers fills CoverURL on cards; other packages reuse it for their own lists.
func (s *Service) AttachCovers(ctx context.Context, cards []Card) {
	if s.presigner == nil {
		return
	}
	for i := range cards {
		if cards[i].CoverKey == "" {
			continue
		}
		url, err := s.presigner.PresignGet(ctx, cards[i].CoverKey)
		if err != nil {
			continue
		}
		cards[i].CoverURL = url
	}
}

func attachCard(ctx context.Context, svc *Service, card Card) Card {
	cards := []Card{card}
	svc.AttachCovers(ctx, cards)
	return cards[0]
}

const maxPage = 1000

func normalize(filter Filter) (Filter, error) {
	switch filter.Kind {
	case "", KindAll:
		filter.Kind = KindAll
	case KindBook, KindBundle:
	default:
		return Filter{}, fmt.Errorf("%w: kind must be all, book or bundle", ErrValidation)
	}

	filter.Query = strings.TrimSpace(filter.Query)
	if len(filter.Query) > 100 {
		return Filter{}, fmt.Errorf("%w: search is too long", ErrValidation)
	}

	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.Page > maxPage {
		return Filter{}, fmt.Errorf("%w: page is too large", ErrValidation)
	}

	switch {
	case filter.Limit <= 0:
		filter.Limit = DefaultLimit
	case filter.Limit > MaxLimit:
		filter.Limit = MaxLimit
	}
	return filter, nil
}

// escapeLike makes user input literal inside ILIKE ... ESCAPE '\': a search for
// "100%" must not become "everything".
func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

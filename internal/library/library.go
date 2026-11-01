// Package library is the parent's shelf: what they bought and what they can keep
// reading. Access is derived from paid orders, exactly like internal/reading.
package library

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"littleworld/internal/catalog"
)

// Covers fills in cover URLs; catalog.Service implements it.
type Covers interface {
	AttachCovers(ctx context.Context, cards []catalog.Card)
}

type Service struct {
	db     *sql.DB
	covers Covers
}

func New(db *sql.DB, covers Covers) *Service {
	return &Service{db: db, covers: covers}
}

// Origin explains how a book reached the shelf.
type Origin struct {
	Type       string // purchase | bundle
	BundleSlug string
	BundleName string
}

type ReadableItem struct {
	Book   catalog.Card
	Origin Origin
}

// BundleShelf keeps the bundle as the purchased product. Books is internal: it feeds the
// readable list and is not serialised on the bundle card.
type BundleShelf struct {
	Bundle      catalog.Card
	PurchasedAt time.Time
	Books       []catalog.Card
}

type Shelf struct {
	Bundles  []BundleShelf
	Books    []catalog.Card // bought individually, not through a bundle
	Readable []ReadableItem
}

// Load builds the shelf for one parent. Unpaid orders and unpublished books never appear:
// a failed or refunded payment grants nothing. There is no reading-progress table yet, so
// Readable lists everything the parent owns rather than claiming "continue reading".
func (s *Service) Load(ctx context.Context, parentID int64) (Shelf, error) {
	bundles, err := s.purchasedBundles(ctx, parentID)
	if err != nil {
		return Shelf{}, err
	}
	direct, err := s.directBooks(ctx, parentID)
	if err != nil {
		return Shelf{}, err
	}

	seen := make(map[int64]bool, len(direct))
	shelf := Shelf{Bundles: []BundleShelf{}, Books: direct, Readable: []ReadableItem{}}
	for _, book := range direct {
		seen[book.ID] = true
		shelf.Readable = append(shelf.Readable, ReadableItem{
			Book:   book,
			Origin: Origin{Type: "purchase"},
		})
	}

	for _, bundle := range bundles {
		shelf.Bundles = append(shelf.Bundles, bundle)
		for _, book := range bundle.Books {
			// A book bought on its own stays a purchase; only the rest are labelled as
			// belonging to a bundle.
			if seen[book.ID] {
				continue
			}
			seen[book.ID] = true
			shelf.Readable = append(shelf.Readable, ReadableItem{
				Book: book,
				Origin: Origin{
					Type:       "bundle",
					BundleSlug: bundle.Bundle.Slug,
					BundleName: bundle.Bundle.Title,
				},
			})
		}
	}

	return shelf, nil
}

func (s *Service) purchasedBundles(ctx context.Context, parentID int64) ([]BundleShelf, error) {
	rows, err := s.db.QueryContext(ctx, purchasedBundlesQuery, parentID)
	if err != nil {
		return nil, fmt.Errorf("load purchased bundles: %w", err)
	}
	defer rows.Close()

	bundles := []BundleShelf{}
	byID := map[int64]int{}
	for rows.Next() {
		var (
			card   catalog.Card
			paidAt sql.NullTime
		)
		if err := scanCard(rows, &card, &paidAt); err != nil {
			return nil, err
		}

		item := BundleShelf{Bundle: card}
		if paidAt.Valid {
			item.PurchasedAt = paidAt.Time
		}
		bundles = append(bundles, item)
		byID[card.ID] = len(bundles) - 1
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load purchased bundles: %w", err)
	}

	books, err := s.bundleBooks(ctx, parentID)
	if err != nil {
		return nil, err
	}
	for bundleID, list := range books {
		if index, ok := byID[bundleID]; ok {
			bundles[index].Books = list
		}
	}

	for i := range bundles {
		cards := []catalog.Card{bundles[i].Bundle}
		s.covers.AttachCovers(ctx, cards)
		bundles[i].Bundle = cards[0]
	}
	return bundles, nil
}

func (s *Service) bundleBooks(ctx context.Context, parentID int64) (map[int64][]catalog.Card, error) {
	rows, err := s.db.QueryContext(ctx, bundleBooksQuery, parentID)
	if err != nil {
		return nil, fmt.Errorf("load bundle books: %w", err)
	}
	defer rows.Close()

	byBundle := map[int64][]catalog.Card{}
	for rows.Next() {
		var (
			bundleID int64
			card     catalog.Card
			position int16
			paidAt   sql.NullTime
		)
		if err := scanCardWithPrefix(rows, &card, &bundleID, &position, &paidAt); err != nil {
			return nil, err
		}
		byBundle[bundleID] = append(byBundle[bundleID], card)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load bundle books: %w", err)
	}

	for bundleID, cards := range byBundle {
		s.covers.AttachCovers(ctx, cards)
		byBundle[bundleID] = cards
	}
	return byBundle, nil
}

func (s *Service) directBooks(ctx context.Context, parentID int64) ([]catalog.Card, error) {
	rows, err := s.db.QueryContext(ctx, directBooksQuery, parentID)
	if err != nil {
		return nil, fmt.Errorf("load purchased books: %w", err)
	}
	defer rows.Close()

	books := []catalog.Card{}
	for rows.Next() {
		var (
			card   catalog.Card
			paidAt sql.NullTime
		)
		if err := scanCard(rows, &card, &paidAt); err != nil {
			return nil, err
		}
		books = append(books, card)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load purchased books: %w", err)
	}

	s.covers.AttachCovers(ctx, books)
	return books, nil
}

// rowScanner is satisfied by *sql.Rows; the shelf queries share catalog's card order
// and append their own columns, so the extra destinations are appended here.
type rowScanner interface{ Scan(dest ...any) error }

type withExtra struct {
	row   rowScanner
	extra []any
}

func (w withExtra) Scan(dest ...any) error {
	all := make([]any, 0, len(dest)+len(w.extra))
	all = append(all, dest...)
	all = append(all, w.extra...)
	return w.row.Scan(all...)
}

type withPrefix struct {
	row    rowScanner
	prefix []any
}

func (w withPrefix) Scan(dest ...any) error {
	all := make([]any, 0, len(dest)+len(w.prefix))
	all = append(all, w.prefix...)
	all = append(all, dest...)
	return w.row.Scan(all...)
}

func scanCard(row rowScanner, card *catalog.Card, paidAt *sql.NullTime) error {
	scanned, err := catalog.ScanCard(withExtra{row: row, extra: []any{paidAt}})
	if err != nil {
		return fmt.Errorf("scan shelf card: %w", err)
	}
	*card = scanned
	return nil
}

func scanCardWithPrefix(row rowScanner, card *catalog.Card, bundleID *int64, position *int16, paidAt *sql.NullTime) error {
	scanned, err := catalog.ScanCard(withPrefix{row: row, prefix: []any{bundleID, position}})
	if err != nil {
		return fmt.Errorf("scan shelf card: %w", err)
	}
	_ = paidAt
	*card = scanned
	return nil
}

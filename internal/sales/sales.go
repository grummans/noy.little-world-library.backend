// Package sales reports what a teacher's paid books and bundles have actually earned.
package sales

import (
	"context"
	"database/sql"
	"fmt"
)

// Service answers revenue questions from the orders table; it writes nothing.
type Service struct {
	db *sql.DB
}

func New(db *sql.DB) *Service {
	return &Service{db: db}
}

// Item is one book's or bundle's paid sales.
type Item struct {
	ID         int64
	Sold       int64
	RevenueVND int64
}

// Summary is a teacher's whole revenue picture. Totals are the sums of the rows, so the
// screen can never show totals that disagree with the tables below them.
type Summary struct {
	Orders           int64
	RevenueVND       int64
	BookRevenueVND   int64
	BundleRevenueVND int64
	Books            []Item
	Bundles          []Item
}

// Paid order counting, grouped per item. An order counts whatever the item costs now: a
// book sold while paid and later switched to free still collected real money, and a report
// that hid it would stop matching the money actually received. A bundle is only ever
// credited with its own paid orders, never with the books inside it.
const booksQuery = `
SELECT b.id, COALESCE(sold.sold, 0), COALESCE(sold.revenue, 0)
FROM books b
LEFT JOIN (
	SELECT o.book_id, COUNT(*) AS sold, SUM(o.amount_vnd) AS revenue
	FROM orders o
	JOIN books own ON own.id = o.book_id AND own.owner_id = $1
	WHERE o.status = 'paid'
	GROUP BY o.book_id
) sold ON sold.book_id = b.id
WHERE b.owner_id = $1
ORDER BY b.updated_at DESC, b.id DESC`

const bundlesQuery = `
SELECT bu.id, COALESCE(sold.sold, 0), COALESCE(sold.revenue, 0)
FROM bundles bu
LEFT JOIN (
	SELECT o.bundle_id, COUNT(*) AS sold, SUM(o.amount_vnd) AS revenue
	FROM orders o
	JOIN bundles own ON own.id = o.bundle_id AND own.owner_id = $1
	WHERE o.status = 'paid'
	GROUP BY o.bundle_id
) sold ON sold.bundle_id = bu.id
WHERE bu.owner_id = $1
ORDER BY bu.updated_at DESC, bu.id DESC`

// Summary reports paid orders for one teacher. Free books have no paid orders by design,
// so they appear with zero rows instead of being hidden: the table stays a list of the
// teacher's books, not a list of the ones that happen to have sold.
func (s *Service) Summary(ctx context.Context, ownerID int64) (Summary, error) {
	books, err := s.items(ctx, booksQuery, ownerID)
	if err != nil {
		return Summary{}, err
	}
	bundles, err := s.items(ctx, bundlesQuery, ownerID)
	if err != nil {
		return Summary{}, err
	}

	summary := Summary{Books: books, Bundles: bundles}
	for _, item := range books {
		summary.Orders += item.Sold
		summary.BookRevenueVND += item.RevenueVND
	}
	for _, item := range bundles {
		summary.Orders += item.Sold
		summary.BundleRevenueVND += item.RevenueVND
	}
	summary.RevenueVND = summary.BookRevenueVND + summary.BundleRevenueVND
	return summary, nil
}

func (s *Service) items(ctx context.Context, query string, ownerID int64) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, query, ownerID)
	if err != nil {
		return nil, fmt.Errorf("query sales: %w", err)
	}
	defer rows.Close()

	items := []Item{}
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.Sold, &item.RevenueVND); err != nil {
			return nil, fmt.Errorf("scan sales: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read sales: %w", err)
	}
	return items, nil
}

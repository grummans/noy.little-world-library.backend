// Package sales reports what a teacher's paid books and bundles have actually earned.
package sales

import (
	"context"
	"database/sql"
	"fmt"
	"time"
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
	Title      string
	Sold       int64
	RevenueVND int64
}

// Week is one calendar week of net revenue (weeks start on Monday, as Postgres counts them).
type Week struct {
	WeekStart  time.Time
	RevenueVND int64
}

// Day is one calendar day of net revenue. The dashboard charts the last week day by day;
// a weekly bucket would hide which day actually sold.
type Day struct {
	Date       time.Time
	RevenueVND int64
}

// Order is a recently paid order, used for the "latest transactions" list.
type Order struct {
	PaidAt    time.Time
	Title     string
	AmountVND int64
}

// Summary is a teacher's whole revenue picture. Totals are the sums of the rows, so the
// screen can never show totals that disagree with the tables below them.
type Summary struct {
	Orders           int64
	RevenueVND       int64
	BookRevenueVND   int64
	BundleRevenueVND int64
	BooksSold        int64
	BundlesSold      int64
	Books            []Item
	Bundles          []Item
	Trend            []Week
	Days             []Day
	Recent           []Order
}

// recentOrders is how many transactions the dashboard shows; the list is a glance, not a ledger.
const recentOrders = 5

// trendWeeks bounds the chart when the caller asks for all-time revenue.
const trendWeeks = 12

// dayTrendDays is the dashboard's daily window: today and the six days before it.
const dayTrendDays = 7

// reportZone is the calendar this report speaks in: Vietnam time, UTC+7 all year (no DST).
// The SQL below spells out 'Asia/Ho_Chi_Minh'; that is the same offset, and Postgres needs
// the IANA name rather than a fixed offset for AT TIME ZONE.
var reportZone = time.FixedZone("ICT", 7*60*60)

// Cutoff is the first instant of the oldest day in a window of `days` calendar days ending
// today, in the report calendar: 30d covers today and the 29 days before it, no more.
func Cutoff(now time.Time, days int) time.Time {
	day := now.In(reportZone).AddDate(0, 0, -(days - 1))
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, reportZone)
}

// Paid order counting, grouped per item. An order counts whatever the item costs now: a
// book sold while paid and later switched to free still collected real money, and a report
// that hid it would stop matching the money actually received. A bundle is only ever
// credited with its own paid orders, never with the books inside it.
const booksQuery = `
SELECT b.id, b.title, COALESCE(sold.sold, 0), COALESCE(sold.revenue, 0)
FROM books b
LEFT JOIN (
	SELECT o.book_id, COUNT(*) AS sold, SUM(o.amount_vnd) AS revenue
	FROM orders o
	JOIN books own ON own.id = o.book_id AND own.owner_id = $1
	WHERE o.status = 'paid' AND ($2::timestamptz IS NULL OR o.paid_at >= $2)
	GROUP BY o.book_id
) sold ON sold.book_id = b.id
WHERE b.owner_id = $1
ORDER BY b.updated_at DESC, b.id DESC`

const bundlesQuery = `
SELECT bu.id, bu.title, COALESCE(sold.sold, 0), COALESCE(sold.revenue, 0)
FROM bundles bu
LEFT JOIN (
	SELECT o.bundle_id, COUNT(*) AS sold, SUM(o.amount_vnd) AS revenue
	FROM orders o
	JOIN bundles own ON own.id = o.bundle_id AND own.owner_id = $1
	WHERE o.status = 'paid' AND ($2::timestamptz IS NULL OR o.paid_at >= $2)
	GROUP BY o.bundle_id
) sold ON sold.bundle_id = bu.id
WHERE bu.owner_id = $1
ORDER BY bu.updated_at DESC, bu.id DESC`

// Weeks are generated so a week without a sale still has a zero bar instead of vanishing
// and silently shortening the axis. The chart always holds exactly trendWeeks buckets
// ending on the current week; an order older than the requested range falls into no bucket,
// so the bars stop early rather than pretending to cover a period the totals exclude.
const trendQuery = `
WITH weeks AS (
	SELECT generate_series(
		date_trunc('week', $4::timestamptz AT TIME ZONE 'Asia/Ho_Chi_Minh') - make_interval(days => 7 * ($2::int - 1)),
		date_trunc('week', $4::timestamptz AT TIME ZONE 'Asia/Ho_Chi_Minh'),
		interval '7 days'
	) AS week_start
)
SELECT w.week_start::date, COALESCE(SUM(o.amount_vnd), 0)::bigint
FROM weeks w
LEFT JOIN orders o
	ON o.status = 'paid'
	AND o.paid_at >= GREATEST(
		w.week_start AT TIME ZONE 'Asia/Ho_Chi_Minh',
		COALESCE($3::timestamptz, '-infinity'::timestamptz)
	)
	AND o.paid_at < (w.week_start + interval '7 days') AT TIME ZONE 'Asia/Ho_Chi_Minh'
	AND (o.book_id IN (SELECT id FROM books WHERE owner_id = $1)
		OR o.bundle_id IN (SELECT id FROM bundles WHERE owner_id = $1))
GROUP BY w.week_start
ORDER BY w.week_start`

// dayTrendQuery is trendQuery at day granularity, with the same rule: a day without a sale
// keeps a zero bar, and the window ends today in the report calendar.
const dayTrendQuery = `
WITH days AS (
	SELECT generate_series(
		date_trunc('day', $3::timestamptz AT TIME ZONE 'Asia/Ho_Chi_Minh') - make_interval(days => $2::int - 1),
		date_trunc('day', $3::timestamptz AT TIME ZONE 'Asia/Ho_Chi_Minh'),
		interval '1 day'
	) AS day_start
)
SELECT d.day_start::date, COALESCE(SUM(o.amount_vnd), 0)::bigint
FROM days d
LEFT JOIN orders o
	ON o.status = 'paid'
	AND o.paid_at >= d.day_start AT TIME ZONE 'Asia/Ho_Chi_Minh'
	AND o.paid_at < (d.day_start + interval '1 day') AT TIME ZONE 'Asia/Ho_Chi_Minh'
	AND (o.book_id IN (SELECT id FROM books WHERE owner_id = $1)
		OR o.bundle_id IN (SELECT id FROM bundles WHERE owner_id = $1))
GROUP BY d.day_start
ORDER BY d.day_start`
const recentQuery = `
SELECT o.paid_at, COALESCE(b.title, bu.title, ''), o.amount_vnd
FROM orders o
LEFT JOIN books b ON b.id = o.book_id
LEFT JOIN bundles bu ON bu.id = o.bundle_id
WHERE o.status = 'paid'
	AND (b.owner_id = $1 OR bu.owner_id = $1)
ORDER BY o.paid_at DESC, o.id DESC
LIMIT $2`

// queryer is the read surface the report needs, so all four queries can share one
// transaction instead of the bare pool.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Summary reports paid orders for one teacher. A nil since means all time; otherwise only
// orders paid after it count, in the items and in the chart alike. Free books have no paid
// orders by design, so they appear with zero rows instead of being hidden: the table stays
// a list of the teacher's books, not a list of the ones that happen to have sold.
func (s *Service) Summary(ctx context.Context, ownerID int64, since *time.Time, now time.Time) (Summary, error) {
	// One snapshot for all four queries: a payment landing between them would otherwise let
	// the chart and the transactions list describe orders the totals do not contain.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Summary{}, fmt.Errorf("begin sales snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	books, err := items(ctx, tx, booksQuery, ownerID, since)
	if err != nil {
		return Summary{}, err
	}
	bundles, err := items(ctx, tx, bundlesQuery, ownerID, since)
	if err != nil {
		return Summary{}, err
	}

	summary := Summary{Books: books, Bundles: bundles}
	for _, item := range books {
		summary.Orders += item.Sold
		summary.BooksSold += item.Sold
		summary.BookRevenueVND += item.RevenueVND
	}
	for _, item := range bundles {
		summary.Orders += item.Sold
		summary.BundlesSold += item.Sold
		summary.BundleRevenueVND += item.RevenueVND
	}
	summary.RevenueVND = summary.BookRevenueVND + summary.BundleRevenueVND

	// An all-time report still charts a bounded window, so the axis never grows forever.
	if summary.Trend, err = trend(ctx, tx, ownerID, trendWeeks, since, now); err != nil {
		return Summary{}, err
	}
	// The daily chart is its own fixed window: the dashboard always shows the last week,
	// whichever period the totals above cover.
	if summary.Days, err = dayTrend(ctx, tx, ownerID, dayTrendDays, now); err != nil {
		return Summary{}, err
	}
	if summary.Recent, err = recent(ctx, tx, ownerID); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func items(ctx context.Context, q queryer, query string, ownerID int64, since *time.Time) ([]Item, error) {
	rows, err := q.QueryContext(ctx, query, ownerID, since)
	if err != nil {
		return nil, fmt.Errorf("query sales: %w", err)
	}
	defer rows.Close()

	items := []Item{}
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.Title, &item.Sold, &item.RevenueVND); err != nil {
			return nil, fmt.Errorf("scan sales: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read sales: %w", err)
	}
	return items, nil
}

func trend(ctx context.Context, q queryer, ownerID int64, count int, since *time.Time, now time.Time) ([]Week, error) {
	rows, err := q.QueryContext(ctx, trendQuery, ownerID, count, since, now)
	if err != nil {
		return nil, fmt.Errorf("query sales trend: %w", err)
	}
	defer rows.Close()

	weeks := []Week{}
	for rows.Next() {
		var week Week
		if err := rows.Scan(&week.WeekStart, &week.RevenueVND); err != nil {
			return nil, fmt.Errorf("scan sales trend: %w", err)
		}
		weeks = append(weeks, week)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read sales trend: %w", err)
	}
	return weeks, nil
}

func dayTrend(ctx context.Context, q queryer, ownerID int64, count int, now time.Time) ([]Day, error) {
	rows, err := q.QueryContext(ctx, dayTrendQuery, ownerID, count, now)
	if err != nil {
		return nil, fmt.Errorf("query daily sales trend: %w", err)
	}
	defer rows.Close()

	days := []Day{}
	for rows.Next() {
		var day Day
		if err := rows.Scan(&day.Date, &day.RevenueVND); err != nil {
			return nil, fmt.Errorf("scan daily sales trend: %w", err)
		}
		days = append(days, day)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read daily sales trend: %w", err)
	}
	return days, nil
}

func recent(ctx context.Context, q queryer, ownerID int64) ([]Order, error) {
	rows, err := q.QueryContext(ctx, recentQuery, ownerID, recentOrders)
	if err != nil {
		return nil, fmt.Errorf("query recent sales: %w", err)
	}
	defer rows.Close()

	orders := []Order{}
	for rows.Next() {
		var order Order
		if err := rows.Scan(&order.PaidAt, &order.Title, &order.AmountVND); err != nil {
			return nil, fmt.Errorf("scan recent sales: %w", err)
		}
		orders = append(orders, order)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read recent sales: %w", err)
	}
	return orders, nil
}

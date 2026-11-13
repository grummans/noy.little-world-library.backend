package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
)

func insertPaidOrder(t *testing.T, pool *sql.DB, buyerID int64, bookID *int64, amount int64, ref string) {
	t.Helper()
	if _, err := pool.ExecContext(t.Context(),
		`INSERT INTO orders (buyer_id, book_id, amount_vnd, status, vnpay_txn_ref, paid_at)
		 VALUES ($1, $2, $3, 'paid', $4, now())`,
		buyerID, bookID, amount, ref); err != nil {
		t.Fatalf("insert order: %v", err)
	}
}

func parentID(t *testing.T, srv *Server, pool *sql.DB) int64 {
	t.Helper()
	cookie := loginCookie(t, srv, "sales-parent@example.com", "parent")
	me := do(t, srv.Router(), http.MethodGet, "/api/auth/me", "", cookie)
	var resp struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(me.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	return resp.ID
}

func TestSalesRequiresTeacher(t *testing.T) {
	srv, _, _ := newTeacherServerWithDB(t)
	parent := loginCookie(t, srv, "sales-parent-only@example.com", "parent")

	if rec := do(t, srv.Router(), http.MethodGet, "/api/teacher/sales", "", parent); rec.Code != http.StatusForbidden {
		t.Fatalf("parent sales status = %d, want 403", rec.Code)
	}

	if rec := do(t, srv.Router(), http.MethodGet, "/api/teacher/sales", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous sales status = %d, want 401", rec.Code)
	}
}

func TestSalesReportsPaidOrdersOnly(t *testing.T) {
	srv, pool, _ := newTeacherServerWithDB(t)
	h := srv.Router()
	teacher := loginCookie(t, srv, "sales-teacher@example.com", "teacher")

	// The helper returns the row as created; publishing already happened inside it.
	book := publishBook(t, h, teacher, "paid", "99000")

	buyer := parentID(t, srv, pool)
	bookID := book.ID
	insertPaidOrder(t, pool, buyer, &bookID, 99000, "sales-paid-1")
	if _, err := pool.ExecContext(t.Context(),
		`INSERT INTO orders (buyer_id, book_id, amount_vnd, status, vnpay_txn_ref)
		 VALUES ($1, $2, 99000, 'pending', 'sales-pending-1')`,
		buyer, bookID); err != nil {
		t.Fatalf("insert pending order: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/teacher/sales", "", teacher)
	if rec.Code != http.StatusOK {
		t.Fatalf("sales status = %d: %s", rec.Code, rec.Body)
	}

	// Revenue must not sit in a shared cache.
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("cache-control = %q, want private, no-store", got)
	}

	var resp salesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode sales: %v", err)
	}

	// A pending order is not revenue yet.
	if resp.Totals.Orders != 1 || resp.Totals.RevenueVND != 99000 {
		t.Errorf("totals = %+v, want 1 order and 99000", resp.Totals)
	}
	if resp.Totals.BookRevenueVND != 99000 || resp.Totals.BundleRevenueVND != 0 {
		t.Errorf("split = %+v, want all of it on books", resp.Totals)
	}
	if len(resp.Books) != 1 {
		t.Fatalf("books = %d, want 1", len(resp.Books))
	}
	if resp.Books[0].ID != bookID || resp.Books[0].Sold != 1 || resp.Books[0].RevenueVND != 99000 {
		t.Errorf("book row = %+v, want sold 1 for 99000", resp.Books[0])
	}
	if resp.Bundles == nil {
		t.Error("bundles = null, want an empty array")
	}
}

func TestSalesRejectsUnknownRange(t *testing.T) {
	srv, _, _ := newTeacherServerWithDB(t)
	teacher := loginCookie(t, srv, "sales-bad-range@example.com", "teacher")

	rec := do(t, srv.Router(), http.MethodGet, "/api/teacher/sales?range=7d", "", teacher)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Code != "invalid_range" {
		t.Errorf("code = %q, want invalid_range", body.Code)
	}
}

func TestSalesRangeFiltersOlderOrders(t *testing.T) {
	srv, pool, _ := newTeacherServerWithDB(t)
	h := srv.Router()
	teacher := loginCookie(t, srv, "sales-range-teacher@example.com", "teacher")
	book := publishBook(t, h, teacher, "paid", "70000")
	buyer := parentID(t, srv, pool)

	bookID := book.ID
	insertPaidOrder(t, pool, buyer, &bookID, 70000, "range-recent")
	// Older than the 30-day window: it must still count for all time, not for the range.
	if _, err := pool.ExecContext(t.Context(),
		`INSERT INTO orders (buyer_id, book_id, amount_vnd, status, vnpay_txn_ref, paid_at)
		 VALUES ($1, $2, 70000, 'paid', 'range-old', now() - interval '60 days')`,
		buyer, bookID); err != nil {
		t.Fatalf("insert old order: %v", err)
	}

	decode := func(path string) salesResponse {
		t.Helper()
		rec := do(t, h, http.MethodGet, path, "", teacher)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", path, rec.Code, rec.Body)
		}
		var resp salesResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return resp
	}

	all := decode("/api/teacher/sales")
	if all.Totals.Orders != 2 || all.Totals.RevenueVND != 140000 {
		t.Errorf("all-time totals = %+v, want 2 orders and 140000", all.Totals)
	}

	last30 := decode("/api/teacher/sales?range=30d")
	if last30.Totals.Orders != 1 || last30.Totals.RevenueVND != 70000 {
		t.Errorf("30-day totals = %+v, want 1 order and 70000", last30.Totals)
	}

	// The screen reads titles, the weekly chart and the transaction list from the same call.
	if len(all.Books) != 1 || all.Books[0].Title != book.Title {
		t.Errorf("books = %+v, want one row titled %q", all.Books, book.Title)
	}
	if all.Totals.BooksSold != 2 || all.Totals.BundlesSold != 0 {
		t.Errorf("units = %+v, want 2 books and 0 bundles", all.Totals)
	}
	if len(all.Trend) == 0 || len(all.Recent) != 2 {
		t.Errorf("trend = %d weeks, recent = %d rows, want both populated",
			len(all.Trend), len(all.Recent))
	}
	if all.Trend[len(all.Trend)-1].WeekStart == "" {
		t.Error("trend week_start is empty, want a date for the axis label")
	}
	// The dashboard's daily chart is a fixed seven-day window ending today, independent of
	// the period the totals cover; the 60-day-old order must not appear in it.
	if len(all.Days) != 7 || len(last30.Days) != 7 {
		t.Errorf("days = %d / %d buckets, want 7 for both ranges", len(all.Days), len(last30.Days))
	}
	var dailyTotal int64
	for _, entry := range last30.Days {
		if entry.Date == "" {
			t.Error("day date is empty, want a date for the axis label")
		}
		dailyTotal += entry.RevenueVND
	}
	if dailyTotal != 70000 {
		t.Errorf("daily total = %d, want the last week's 70000 only", dailyTotal)
	}
	if len(last30.Recent) != 2 {
		t.Errorf("recent = %d rows, want the ledger history regardless of range", len(last30.Recent))
	}
}

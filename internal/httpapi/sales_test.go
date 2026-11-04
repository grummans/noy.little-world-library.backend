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

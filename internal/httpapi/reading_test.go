package httpapi

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"littleworld/internal/auth"
	"littleworld/internal/books"
	"littleworld/internal/bundles"
	"littleworld/internal/catalog"
	"littleworld/internal/dbtest"
	"littleworld/internal/library"
	"littleworld/internal/orders"
	"littleworld/internal/reading"
	"littleworld/internal/sales"
	"littleworld/internal/storage"
	"littleworld/internal/storagetest"
	"littleworld/internal/vnpay"
)

// newTeacherServerWithDB is newTeacherServer plus the pool, for tests that need to
// insert rows the API cannot create yet (orders).
func newTeacherServerWithDB(t *testing.T) (*Server, *sql.DB, *storagetest.Mem) {
	t.Helper()
	pool := dbtest.Migrated(t)
	store := storagetest.New()

	return New(Deps{
		DB:             pool,
		Auth:           auth.New(pool),
		Books:          books.New(pool, store),
		Bundles:        bundles.New(pool, store),
		Catalog:        catalog.New(pool, store),
		Reading:        reading.New(pool, store),
		Library:        library.New(pool, catalog.New(pool, store)),
		Orders:         orders.New(pool, vnpay.New(testVnpayConfig())),
		Sales:          sales.New(pool),
		Log:            slog.New(slog.DiscardHandler),
		SecureCookies:  false,
		VnpayReturnURL: "https://example.com/return",
	}), pool, store
}

// publishBook creates and publishes a book through the API.
func publishBook(t *testing.T, h http.Handler, cookie *http.Cookie, access, price string) bookResponse {
	t.Helper()

	fields := map[string]string{"title": "Sách " + access, "description": "Mô tả", "access_type": access}
	if price != "" {
		fields["price_vnd"] = price
	}
	body, contentType := multipartBody(t, fields, map[string]struct {
		contentType string
		data        []byte
	}{
		"epub":  {storage.EpubContentType, epubFixture(t)},
		"cover": {"image/png", pngFixture(t)},
	})
	created := doUpload(t, h, "/api/teacher/books", cookie, body, contentType)
	if created.Code != http.StatusCreated {
		t.Fatalf("create book: %d: %s", created.Code, created.Body)
	}

	var book bookResponse
	if err := json.Unmarshal(created.Body.Bytes(), &book); err != nil {
		t.Fatalf("decode book: %v", err)
	}
	if rec := do(t, h, http.MethodPost, "/api/teacher/books/"+itoa(book.ID)+"/publish", "", cookie); rec.Code != http.StatusOK {
		t.Fatalf("publish book: %d: %s", rec.Code, rec.Body)
	}
	return book
}

// A real paid order plus a valid cookie must let the buyer open the book.
func TestReadEndpointPaidBookWithOrder(t *testing.T) {
	srv, pool, _ := newTeacherServerWithDB(t)
	h := srv.Router()
	teacher := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)
	book := publishBook(t, h, teacher, books.AccessPaid, "99000")

	parent := loginCookie(t, srv, "parent@example.com", auth.RoleParent)
	var parentID int64
	if err := pool.QueryRowContext(t.Context(),
		`SELECT id FROM users WHERE email = 'parent@example.com'`).Scan(&parentID); err != nil {
		t.Fatalf("read parent id: %v", err)
	}
	if _, err := pool.ExecContext(t.Context(),
		`INSERT INTO orders (buyer_id, book_id, amount_vnd, status, vnpay_txn_ref, paid_at)
		 VALUES ($1, $2, 99000, 'paid', 'txn-1', now())`, parentID, book.ID); err != nil {
		t.Fatalf("insert order: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/books/"+book.Slug+"/read", "", parent)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q, want private, no-store", got)
	}

	var session struct {
		Source    string `json:"source"`
		URL       string `json:"url"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if session.Source != "purchase" || session.URL == "" || session.ExpiresIn <= 0 {
		t.Errorf("session = %+v", session)
	}
}

// A forged cookie must not become an authentication error: the endpoint is public.
func TestReadEndpointTreatsBadCookieAsAnonymous(t *testing.T) {
	srv, _, _ := newTeacherServerWithDB(t)
	h := srv.Router()
	teacher := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)
	book := publishBook(t, h, teacher, books.AccessFree, "")

	forged := &http.Cookie{Name: sessionCookie, Value: "forged-token"}
	if rec := do(t, h, http.MethodGet, "/api/books/"+book.Slug+"/read", "", forged); rec.Code != http.StatusOK {
		t.Errorf("forged cookie status = %d, want 200 (anonymous): %s", rec.Code, rec.Body)
	}
}

func TestParentLibraryEndpoint(t *testing.T) {
	srv, pool, _ := newTeacherServerWithDB(t)
	h := srv.Router()
	teacher := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)
	parent := loginCookie(t, srv, "parent@example.com", auth.RoleParent)

	book := publishBook(t, h, teacher, books.AccessPaid, "50000")

	// A teacher has no shelf.
	if rec := do(t, h, http.MethodGet, "/api/parent/library", "", teacher); rec.Code != http.StatusForbidden {
		t.Errorf("teacher status = %d, want 403", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/parent/library", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous status = %d, want 401", rec.Code)
	}

	var parentID int64
	if err := pool.QueryRowContext(t.Context(),
		`SELECT id FROM users WHERE email = 'parent@example.com'`).Scan(&parentID); err != nil {
		t.Fatalf("read parent id: %v", err)
	}
	if _, err := pool.ExecContext(t.Context(),
		`INSERT INTO orders (buyer_id, book_id, amount_vnd, status, vnpay_txn_ref, paid_at)
		 VALUES ($1, $2, 50000, 'paid', 'txn-lib', now())`, parentID, book.ID); err != nil {
		t.Fatalf("insert order: %v", err)
	}

	rec := do(t, h, http.MethodGet, "/api/parent/library", "", parent)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q, want private, no-store", got)
	}

	var shelf struct {
		Bundles  []map[string]any `json:"bundles"`
		Books    []cardResponse   `json:"books"`
		Readable []struct {
			Book   cardResponse   `json:"book"`
			Origin originResponse `json:"origin"`
		} `json:"readable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &shelf); err != nil {
		t.Fatalf("decode shelf: %v", err)
	}
	if len(shelf.Books) != 1 || shelf.Books[0].Slug != book.Slug {
		t.Fatalf("books = %+v", shelf.Books)
	}
	if shelf.Books[0].AccessType != books.AccessPaid || shelf.Books[0].PublishedAt.IsZero() {
		t.Errorf("card fields incomplete: %+v", shelf.Books[0])
	}
	if len(shelf.Readable) != 1 || shelf.Readable[0].Origin.Type != "purchase" {
		t.Errorf("continue = %+v", shelf.Readable)
	}
	if shelf.Readable[0].Book.CoverURL == "" {
		t.Error("cover url missing in library")
	}
	if len(shelf.Bundles) != 0 {
		t.Errorf("bundles = %+v, want an empty array", shelf.Bundles)
	}

	// Empty collections must serialise as [] and never as null.
	for _, field := range []string{`"bundles":[`, `"books":[`, `"readable":[`} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("body missing %s: %s", field, rec.Body)
		}
	}
}

func TestOrderAndIPNEndpoints(t *testing.T) {
	srv, pool, _ := newTeacherServerWithDB(t)
	h := srv.Router()
	teacher := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)
	parent := loginCookie(t, srv, "parent@example.com", auth.RoleParent)

	book := publishBook(t, h, teacher, books.AccessPaid, "99000")

	// Teachers cannot buy.
	if rec := do(t, h, http.MethodPost, "/api/orders", `{"book_slug":"`+book.Slug+`"}`, teacher); rec.Code != http.StatusForbidden {
		t.Errorf("teacher checkout status = %d, want 403", rec.Code)
	}

	created := do(t, h, http.MethodPost, "/api/orders", `{"book_slug":"`+book.Slug+`"}`, parent)
	if created.Code != http.StatusCreated {
		t.Fatalf("create order status = %d: %s", created.Code, created.Body)
	}
	var order orderResponse
	if err := json.Unmarshal(created.Body.Bytes(), &order); err != nil {
		t.Fatalf("decode order: %v", err)
	}
	if order.AmountVND != 99000 || order.Status != orders.StatusPending || order.PaymentURL == "" {
		t.Fatalf("order = %+v", order)
	}

	// The buyer can poll the order; the teacher cannot.
	if rec := do(t, h, http.MethodGet, "/api/orders/"+order.TxnRef, "", parent); rec.Code != http.StatusOK {
		t.Errorf("owner poll status = %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/orders/"+order.TxnRef, "", teacher); rec.Code != http.StatusForbidden {
		t.Errorf("teacher poll status = %d, want 403", rec.Code)
	}

	// IPN for an unknown order is rejected with VNPay code 01.
	if rec := do(t, h, http.MethodGet, "/api/payments/vnpay/ipn?vnp_TxnRef=none&vnp_SecureHash=bad", ""); rec.Code != http.StatusOK {
		t.Fatalf("ipn status = %d", rec.Code)
	} else if !strings.Contains(rec.Body.String(), "97") {
		t.Errorf("ipn body = %s, want code 97 for a bad signature", rec.Body)
	}

	// Buyer still cannot read the book before a verified IPN.
	if rec := do(t, h, http.MethodGet, "/api/books/"+book.Slug+"/read", "", parent); rec.Code != http.StatusForbidden {
		t.Errorf("read before IPN status = %d, want 403", rec.Code)
	}

	// Simulate a successful payment by granting it the way the IPN does.
	if _, err := pool.ExecContext(t.Context(),
		`UPDATE orders SET status = 'paid', paid_at = now(), vnpay_transaction_no = 'txn-http'
		 WHERE vnpay_txn_ref = $1`, order.TxnRef); err != nil {
		t.Fatalf("mark paid: %v", err)
	}

	if rec := do(t, h, http.MethodGet, "/api/books/"+book.Slug+"/read", "", parent); rec.Code != http.StatusOK {
		t.Errorf("read after payment status = %d: %s", rec.Code, rec.Body)
	}
}

// testVnpayConfig uses fake sandbox credentials: signing and verification are real,
// only the merchant account is not.
func testVnpayConfig() vnpay.Config {
	return vnpay.Config{
		TmnCode:    "TESTCODE",
		HashSecret: "SECRETKEY1234567890",
		PaymentURL: "https://sandbox.vnpayment.vn/paymentv2/vpcpay.html",
	}
}

// A second parent must not see the first parent's order, and only a signed IPN grants
// read access through HTTP.
func TestOrderIsolationAndIPNOverHTTP(t *testing.T) {
	srv, _, _ := newTeacherServerWithDB(t)
	h := srv.Router()
	teacher := loginCookie(t, srv, "teacher@example.com", auth.RoleTeacher)
	parent := loginCookie(t, srv, "parent@example.com", auth.RoleParent)
	other := loginCookie(t, srv, "other@example.com", auth.RoleParent)

	book := publishBook(t, h, teacher, books.AccessPaid, "99000")

	created := do(t, h, http.MethodPost, "/api/orders", `{"book_slug":"`+book.Slug+`"}`, parent)
	if created.Code != http.StatusCreated {
		t.Fatalf("create order: %d: %s", created.Code, created.Body)
	}
	var order orderResponse
	if err := json.Unmarshal(created.Body.Bytes(), &order); err != nil {
		t.Fatalf("decode order: %v", err)
	}

	// Another parent cannot read the order.
	if rec := do(t, h, http.MethodGet, "/api/orders/"+order.TxnRef, "", other); rec.Code != http.StatusNotFound {
		t.Errorf("other parent order status = %d, want 404", rec.Code)
	}

	// Only the signed IPN grants access: a forged one is rejected and changes nothing.
	forged := "/api/payments/vnpay/ipn?vnp_TxnRef=" + order.TxnRef + "&vnp_Amount=9900000&vnp_ResponseCode=00&vnp_TransactionStatus=00&vnp_TmnCode=TESTCODE&vnp_SecureHash=deadbeef"
	rec := do(t, h, http.MethodGet, forged, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "97") {
		t.Fatalf("forged ipn = %d %s, want code 97", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodGet, "/api/books/"+book.Slug+"/read", "", parent); rec.Code != http.StatusForbidden {
		t.Errorf("read after forged IPN = %d, want 403", rec.Code)
	}

	// A properly signed IPN for the right amount marks the order paid.
	signed := signedIPNQuery(t, order.TxnRef, 99000)
	if rec := do(t, h, http.MethodGet, "/api/payments/vnpay/ipn?"+signed.Encode(), ""); rec.Code != http.StatusOK {
		t.Fatalf("ipn status = %d", rec.Code)
	} else if !strings.Contains(rec.Body.String(), `"RspCode":"00"`) {
		t.Fatalf("ipn body = %s, want RspCode 00", rec.Body)
	}

	// The return page now reports success and reading works.
	ret := do(t, h, http.MethodGet, "/api/payments/vnpay/return?"+signed.Encode(), "")
	if ret.Code != http.StatusOK || !strings.Contains(ret.Body.String(), `"status":"paid"`) {
		t.Errorf("return = %d %s, want status paid", ret.Code, ret.Body)
	}
	if rec := do(t, h, http.MethodGet, "/api/books/"+book.Slug+"/read", "", parent); rec.Code != http.StatusOK {
		t.Errorf("read after paid IPN = %d, want 200: %s", rec.Code, rec.Body)
	}
}

// signedIPNQuery builds a callback VNPay would send for this order.
func signedIPNQuery(t *testing.T, txnRef string, amountVND int64) url.Values {
	t.Helper()

	values := url.Values{}
	values.Set("vnp_TmnCode", testVnpayConfig().TmnCode)
	values.Set("vnp_TxnRef", txnRef)
	values.Set("vnp_Amount", strconv.FormatInt(amountVND*100, 10))
	values.Set("vnp_ResponseCode", vnpay.SuccessCode)
	values.Set("vnp_TransactionStatus", vnpay.SuccessCode)
	values.Set("vnp_TransactionNo", "14000009")
	values.Set("vnp_PayDate", "20261101103000")
	values.Set("vnp_SecureHash", vnpay.New(testVnpayConfig()).SignValues(values))
	return values
}

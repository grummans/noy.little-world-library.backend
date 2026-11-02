package orders_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"testing"

	"littleworld/internal/dbtest"
	"littleworld/internal/orders"
	"littleworld/internal/vnpay"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type fixture struct {
	svc     *orders.Service
	db      *sql.DB
	gateway *vnpay.Client
	parent  int64
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := dbtest.Migrated(t)
	gateway := vnpay.New(vnpay.Config{
		TmnCode:    "TESTCODE",
		HashSecret: "SECRETKEY1234567890",
		PaymentURL: "https://sandbox.vnpayment.vn/paymentv2/vpcpay.html",
	})

	var parent int64
	if err := pool.QueryRowContext(t.Context(),
		`INSERT INTO users (display_name, email, password_hash, role) VALUES ('Test User', 'parent@example.com', 'x', 'parent') RETURNING id`,
	).Scan(&parent); err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	return fixture{svc: orders.New(pool, gateway), db: pool, gateway: gateway, parent: parent}
}

func insertBook(t *testing.T, db *sql.DB, ownerID int64, slug, access, status string, price *int64) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(t.Context(),
		`INSERT INTO books (owner_id, slug, title, description, cover_object_key, epub_object_key,
			access_type, price_vnd, status, published_at)
		 VALUES ($1, $2, $3, 'Mô tả', 'books/1/cover.png', 'books/1/book.epub', $4, $5, $6,
		         CASE WHEN $6 = 'published' THEN now() ELSE NULL END)
		 RETURNING id`,
		ownerID, slug, "Sách "+slug, access, price, status).Scan(&id); err != nil {
		t.Fatalf("insert book: %v", err)
	}
	return id
}

func teacher(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(t.Context(),
		`INSERT INTO users (display_name, email, password_hash, role) VALUES ('Test User', 'teacher@example.com', 'x', 'teacher') RETURNING id`,
	).Scan(&id); err != nil {
		t.Fatalf("insert teacher: %v", err)
	}
	return id
}

func price(v int64) *int64 { return &v }

// callbackFor builds a callback exactly like VNPay would send it.
func callbackFor(t *testing.T, client *vnpay.Client, txnRef string, amountVND int64, responseCode, status string) url.Values {
	t.Helper()

	values := url.Values{}
	values.Set("vnp_TmnCode", "TESTCODE")
	values.Set("vnp_TxnRef", txnRef)
	values.Set("vnp_Amount", itoa(amountVND*100))
	values.Set("vnp_ResponseCode", responseCode)
	values.Set("vnp_TransactionStatus", status)
	values.Set("vnp_TransactionNo", "14000001")
	values.Set("vnp_PayDate", "20261101103000")
	values.Set("vnp_SecureHash", client.SignValues(values))
	return values
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

func TestCreateOrderSnapshotsPriceFromDatabase(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := teacher(t, f.db)
	insertBook(t, f.db, owner, "tra-phi", "paid", "published", price(99000))

	checkout, err := f.svc.Create(ctx, f.parent, "tra-phi", "", "https://example.com/return", "203.0.113.5")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if checkout.Order.AmountVND != 99000 {
		t.Errorf("amount = %d, want the database price", checkout.Order.AmountVND)
	}
	if checkout.Order.Status != orders.StatusPending {
		t.Errorf("status = %q", checkout.Order.Status)
	}
	if checkout.PaymentURL == "" {
		t.Error("payment url missing")
	}
	if checkout.Order.TxnRef == "" {
		t.Error("txn ref missing")
	}
}

func TestCreateOrderRejectsUnbuyableItems(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := teacher(t, f.db)

	insertBook(t, f.db, owner, "mien-phi", "free", "published", nil)
	insertBook(t, f.db, owner, "con-nhap", "paid", "draft", price(1000))

	if _, err := f.svc.Create(ctx, f.parent, "mien-phi", "", "https://r", "1.1.1.1"); !errors.Is(err, orders.ErrNotPurchasable) {
		t.Errorf("free book: err = %v", err)
	}
	if _, err := f.svc.Create(ctx, f.parent, "con-nhap", "", "https://r", "1.1.1.1"); !errors.Is(err, orders.ErrNotPurchasable) {
		t.Errorf("draft book: err = %v", err)
	}
	if _, err := f.svc.Create(ctx, f.parent, "khong-ton-tai", "", "https://r", "1.1.1.1"); !errors.Is(err, orders.ErrNotFound) {
		t.Errorf("missing book: err = %v", err)
	}
	if _, err := f.svc.Create(ctx, f.parent, "a", "b", "https://r", "1.1.1.1"); !errors.Is(err, orders.ErrValidation) {
		t.Errorf("two slugs: err = %v", err)
	}
	if _, err := f.svc.Create(ctx, f.parent, "", "", "https://r", "1.1.1.1"); !errors.Is(err, orders.ErrValidation) {
		t.Errorf("no slug: err = %v", err)
	}
}

func TestGetOrderIsOwnerOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := teacher(t, f.db)
	insertBook(t, f.db, owner, "tra-phi", "paid", "published", price(5000))

	checkout, err := f.svc.Create(ctx, f.parent, "tra-phi", "", "https://r", "1.1.1.1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := f.svc.Get(ctx, f.parent, checkout.Order.TxnRef); err != nil {
		t.Errorf("owner read: %v", err)
	}
	if _, err := f.svc.Get(ctx, f.parent+1, checkout.Order.TxnRef); !errors.Is(err, orders.ErrNotFound) {
		t.Errorf("other buyer read: err = %v, want ErrNotFound", err)
	}
	if _, err := f.svc.Get(ctx, f.parent, "unknown-ref"); !errors.Is(err, orders.ErrNotFound) {
		t.Errorf("unknown ref: err = %v", err)
	}
}

// The return URL is signed by VNPay too, so a verified success settles the order even
// when the IPN never arrives (a merchant whose IPN URL is wrong still gets paid orders).
func TestReturnStateAppliesVerifiedSuccess(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := teacher(t, f.db)
	insertBook(t, f.db, owner, "tra-phi", "paid", "published", price(5000))

	checkout, err := f.svc.Create(ctx, f.parent, "tra-phi", "", "https://r", "1.1.1.1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The return query is exactly what VNPay redirects with (signed).
	state := f.svc.ReturnState(ctx, callbackFor(t, f.gateway, checkout.Order.TxnRef, 5000, "00", "00"))
	if state.Status != "paid" {
		t.Errorf("status = %q, want paid", state.Status)
	}

	order, err := f.svc.Get(ctx, f.parent, checkout.Order.TxnRef)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.Status != "paid" || order.PaidAt == nil {
		t.Errorf("order after return = %+v, want paid with paid_at", order)
	}

	// A forged callback is rejected without touching the order.
	tampered := callbackFor(t, f.gateway, checkout.Order.TxnRef, 5000, "00", "00")
	tampered.Set("vnp_Amount", "1")
	if state := f.svc.ReturnState(ctx, tampered); state.Status != "invalid" {
		t.Errorf("tampered return status = %q, want invalid", state.Status)
	}
}

func TestIPNGrantsAccessOnceAndOnlyOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := teacher(t, f.db)
	insertBook(t, f.db, owner, "tra-phi", "paid", "published", price(5000))

	checkout, err := f.svc.Create(ctx, f.parent, "tra-phi", "", "https://r", "1.1.1.1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	callback := callbackFor(t, f.gateway, checkout.Order.TxnRef, 5000, "00", "00")

	first := f.svc.HandleIPN(ctx, callback)
	if first.RspCode != orders.RspSuccess {
		t.Fatalf("first IPN = %+v, want 00", first)
	}

	order, err := f.svc.Get(ctx, f.parent, checkout.Order.TxnRef)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if order.Status != orders.StatusPaid || order.PaidAt == nil || order.TransactionNo == nil {
		t.Fatalf("order = %+v, want paid with transaction details", order)
	}

	// VNPay retries until it sees 00 or 02: the retry must not double-grant.
	second := f.svc.HandleIPN(ctx, callback)
	if second.RspCode != orders.RspAlreadyPaid {
		t.Errorf("second IPN = %+v, want 02", second)
	}
}

func TestIPNRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := teacher(t, f.db)
	insertBook(t, f.db, owner, "tra-phi", "paid", "published", price(5000))

	checkout, err := f.svc.Create(ctx, f.parent, "tra-phi", "", "https://r", "1.1.1.1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Wrong signature.
	forged := callbackFor(t, f.gateway, checkout.Order.TxnRef, 5000, "00", "00")
	forged.Set("vnp_Amount", "1")
	if got := f.svc.HandleIPN(ctx, forged); got.RspCode != orders.RspInvalidSign {
		t.Errorf("forged = %+v, want 97", got)
	}

	// Unknown order reference.
	unknown := callbackFor(t, f.gateway, "LW-unknown", 5000, "00", "00")
	if got := f.svc.HandleIPN(ctx, unknown); got.RspCode != orders.RspOrderNotFound {
		t.Errorf("unknown order = %+v, want 01", got)
	}

	// Amount mismatch: signed correctly but not what we charged.
	wrongAmount := callbackFor(t, f.gateway, checkout.Order.TxnRef, 1000, "00", "00")
	if got := f.svc.HandleIPN(ctx, wrongAmount); got.RspCode != orders.RspInvalidAmount {
		t.Errorf("wrong amount = %+v, want 04", got)
	}

	// The order must still be pending after every rejected callback.
	order, err := f.svc.Get(ctx, f.parent, checkout.Order.TxnRef)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if order.Status != orders.StatusPending {
		t.Errorf("status = %q, want pending", order.Status)
	}
}

func TestIPNMarksCancelledAndFailed(t *testing.T) {
	cases := map[string]struct {
		code   string
		status string
		want   string
	}{
		"cancelled by buyer": {code: "24", status: "02", want: orders.StatusCancelled},
		"insufficient funds": {code: "51", status: "02", want: orders.StatusFailed},
		"timeout":            {code: "11", status: "02", want: orders.StatusFailed},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			owner := teacher(t, f.db)
			insertBook(t, f.db, owner, "tra-phi", "paid", "published", price(5000))

			checkout, err := f.svc.Create(ctx, f.parent, "tra-phi", "", "https://r", "1.1.1.1")
			if err != nil {
				t.Fatalf("create: %v", err)
			}

			response := f.svc.HandleIPN(ctx, callbackFor(t, f.gateway, checkout.Order.TxnRef, 5000, tc.code, tc.status))
			if response.RspCode != orders.RspSuccess {
				t.Fatalf("IPN = %+v, want 00", response)
			}

			order, err := f.svc.Get(ctx, f.parent, checkout.Order.TxnRef)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if order.Status != tc.want {
				t.Errorf("status = %q, want %q", order.Status, tc.want)
			}
			if order.PaidAt != nil {
				t.Error("failed order must not have paid_at")
			}
		})
	}
}

// A paid order must be visible to the reading rule immediately.
func TestPaidIPNGrantsReadAccess(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := teacher(t, f.db)
	bookID := insertBook(t, f.db, owner, "tra-phi", "paid", "published", price(5000))

	checkout, err := f.svc.Create(ctx, f.parent, "tra-phi", "", "https://r", "1.1.1.1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := f.svc.HandleIPN(ctx, callbackFor(t, f.gateway, checkout.Order.TxnRef, 5000, "00", "00")); got.RspCode != orders.RspSuccess {
		t.Fatalf("IPN = %+v", got)
	}

	var paid bool
	if err := f.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM orders WHERE buyer_id = $1 AND book_id = $2 AND status = 'paid')`,
		f.parent, bookID).Scan(&paid); err != nil {
		t.Fatalf("check access: %v", err)
	}
	if !paid {
		t.Error("reading rule would not grant access after a paid IPN")
	}
}

// A browser-style callback (no vnp_TransactionStatus) must never confirm an order.
func TestIPNRequiresTransactionStatus(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := teacher(t, f.db)
	insertBook(t, f.db, owner, "tra-phi", "paid", "published", price(5000))

	checkout, err := f.svc.Create(ctx, f.parent, "tra-phi", "", "https://r", "1.1.1.1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	callback := callbackFor(t, f.gateway, checkout.Order.TxnRef, 5000, "00", "")
	callback.Del("vnp_TransactionStatus")
	callback.Set("vnp_SecureHash", f.gateway.SignValues(callback))

	response := f.svc.HandleIPN(ctx, callback)
	if response.RspCode != orders.RspUnknownError {
		t.Errorf("response = %+v, want 99 for a callback without transaction status", response)
	}

	order, err := f.svc.Get(ctx, f.parent, checkout.Order.TxnRef)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if order.Status != orders.StatusPending {
		t.Errorf("status = %q, want pending", order.Status)
	}
}

// Two IPNs arriving at once must grant access once: exactly one 00 and one 02.
func TestConcurrentIPNGrantsOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := teacher(t, f.db)
	insertBook(t, f.db, owner, "tra-phi", "paid", "published", price(5000))

	checkout, err := f.svc.Create(ctx, f.parent, "tra-phi", "", "https://r", "1.1.1.1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	callback := callbackFor(t, f.gateway, checkout.Order.TxnRef, 5000, "00", "00")

	responses := make(chan string, 2)
	for i := 0; i < 2; i++ {
		go func() {
			responses <- f.svc.HandleIPN(ctx, callback).RspCode
		}()
	}
	first, second := <-responses, <-responses

	successes, duplicates := 0, 0
	for _, code := range []string{first, second} {
		switch code {
		case orders.RspSuccess:
			successes++
		case orders.RspAlreadyPaid:
			duplicates++
		default:
			t.Errorf("unexpected RspCode %q", code)
		}
	}
	if successes != 1 || duplicates != 1 {
		t.Errorf("codes = %s,%s; want exactly one 00 and one 02", first, second)
	}
}

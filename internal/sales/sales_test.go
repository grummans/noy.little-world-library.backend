package sales_test

import (
	"database/sql"
	"os"
	"strconv"
	"testing"

	"littleworld/internal/dbtest"
	"littleworld/internal/sales"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func insertUser(t *testing.T, db *sql.DB, email, role string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(t.Context(),
		`INSERT INTO users (email, password_hash, role) VALUES ($1, 'x', $2) RETURNING id`,
		email, role).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func insertBook(t *testing.T, db *sql.DB, ownerID int64, slug string, price int64) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(t.Context(),
		`INSERT INTO books (owner_id, slug, title, description, cover_object_key, epub_object_key,
			access_type, price_vnd, status, published_at)
		 VALUES ($1, $2, $3, 'Mô tả', $4, $5, 'paid', $6, 'published', now())
		 RETURNING id`,
		ownerID, slug, "Sách "+slug, "books/"+slug+"/cover.png",
		"books/"+slug+"/book.epub", price).Scan(&id); err != nil {
		t.Fatalf("insert book: %v", err)
	}
	return id
}

func insertBundle(t *testing.T, db *sql.DB, ownerID int64, slug string, price int64, bookIDs ...int64) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(t.Context(),
		`INSERT INTO bundles (owner_id, slug, title, description, price_vnd, status, published_at)
		 VALUES ($1, $2, $3, 'Mô tả', $4, 'published', now()) RETURNING id`,
		ownerID, slug, "Gói "+slug, price).Scan(&id); err != nil {
		t.Fatalf("insert bundle: %v", err)
	}
	for i, bookID := range bookIDs {
		if _, err := db.ExecContext(t.Context(),
			`INSERT INTO bundle_books (bundle_id, book_id, position) VALUES ($1, $2, $3)`,
			id, bookID, i+1); err != nil {
			t.Fatalf("insert bundle book: %v", err)
		}
	}
	return id
}

var orderSeq int

func insertOrder(t *testing.T, db *sql.DB, buyerID int64, bookID, bundleID *int64, amount int64, status string) {
	t.Helper()
	orderSeq++
	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO orders (buyer_id, book_id, bundle_id, amount_vnd, status, vnpay_txn_ref, paid_at)
		 VALUES ($1, $2, $3, $4, $5, $6, CASE WHEN $5 = 'paid' THEN now() ELSE NULL END)`,
		buyerID, bookID, bundleID, amount, status, "txn-sales-"+strconv.Itoa(orderSeq)); err != nil {
		t.Fatalf("insert order: %v", err)
	}
}

func itemByID(items []sales.Item, id int64) (sales.Item, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return sales.Item{}, false
}

func TestSummaryBooksBundlesAndTotals(t *testing.T) {
	pool := dbtest.Migrated(t)
	svc := sales.New(pool)

	teacher := insertUser(t, pool, "sales-teacher@example.com", "teacher")
	other := insertUser(t, pool, "sales-other@example.com", "teacher")
	buyer := insertUser(t, pool, "sales-buyer@example.com", "parent")

	soldAlone := insertBook(t, pool, teacher, "sold-alone", 50000)
	inBundle := insertBook(t, pool, teacher, "in-bundle", 30000)
	unsold := insertBook(t, pool, teacher, "unsold", 20000)
	notMine := insertBook(t, pool, other, "not-mine", 40000)
	bundle := insertBundle(t, pool, teacher, "combo", 70000, inBundle, unsold)

	bookID := func(id int64) *int64 { return &id }

	insertOrder(t, pool, buyer, bookID(soldAlone), nil, 50000, "paid")
	insertOrder(t, pool, buyer, bookID(soldAlone), nil, 50000, "pending")
	insertOrder(t, pool, buyer, bookID(soldAlone), nil, 50000, "failed")
	insertOrder(t, pool, buyer, nil, bookID(bundle), 70000, "paid")
	insertOrder(t, pool, buyer, bookID(notMine), nil, 40000, "paid")

	summary, err := svc.Summary(t.Context(), teacher)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}

	// Only paid orders count, and only for this teacher's items.
	if summary.Orders != 2 {
		t.Errorf("orders = %d, want 2", summary.Orders)
	}
	if summary.BookRevenueVND != 50000 {
		t.Errorf("book revenue = %d, want 50000", summary.BookRevenueVND)
	}
	if summary.BundleRevenueVND != 70000 {
		t.Errorf("bundle revenue = %d, want 70000", summary.BundleRevenueVND)
	}
	if summary.RevenueVND != 120000 {
		t.Errorf("total revenue = %d, want 120000", summary.RevenueVND)
	}

	if len(summary.Books) != 3 {
		t.Fatalf("books = %d, want 3 (another teacher's book must not appear)", len(summary.Books))
	}

	alone, ok := itemByID(summary.Books, soldAlone)
	if !ok {
		t.Fatal("sold book missing from report")
	}
	if alone.Sold != 1 || alone.RevenueVND != 50000 {
		t.Errorf("sold book = %+v, want sold 1, revenue 50000", alone)
	}

	// A book bought through a bundle earns nothing of its own: the money is on the bundle,
	// so a bundled book reports no sales of its own no matter how often the bundle sold.
	bundled, ok := itemByID(summary.Books, inBundle)
	if !ok {
		t.Fatal("bundled book missing from report")
	}
	if bundled.Sold != 0 || bundled.RevenueVND != 0 {
		t.Errorf("bundled book = %+v, want no own sales", bundled)
	}

	empty, ok := itemByID(summary.Books, unsold)
	if !ok {
		t.Fatal("unsold book missing from report")
	}
	if empty.Sold != 0 || empty.RevenueVND != 0 {
		t.Errorf("unsold book = %+v, want zero sales", empty)
	}

	if len(summary.Bundles) != 1 {
		t.Fatalf("bundles = %d, want 1", len(summary.Bundles))
	}
	if summary.Bundles[0].Sold != 1 || summary.Bundles[0].RevenueVND != 70000 {
		t.Errorf("bundle row = %+v, want sold 1, revenue 70000", summary.Bundles[0])
	}
}

func TestSummaryEmptyOwner(t *testing.T) {
	pool := dbtest.Migrated(t)
	svc := sales.New(pool)

	lone := insertUser(t, pool, "sales-lone@example.com", "teacher")

	summary, err := svc.Summary(t.Context(), lone)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Orders != 0 || summary.RevenueVND != 0 {
		t.Errorf("summary = %+v, want zero totals", summary)
	}
	if summary.Books == nil || summary.Bundles == nil {
		t.Errorf("lists = %v/%v, want empty JSON arrays not null", summary.Books, summary.Bundles)
	}
}

func TestSummaryCountsEveryPaidOrderOnce(t *testing.T) {
	pool := dbtest.Migrated(t)
	svc := sales.New(pool)

	teacher := insertUser(t, pool, "sales-repeat@example.com", "teacher")
	other := insertUser(t, pool, "sales-repeat-other@example.com", "teacher")
	buyer := insertUser(t, pool, "sales-repeat-buyer@example.com", "parent")
	otherBuyer := insertUser(t, pool, "sales-repeat-buyer2@example.com", "parent")

	book := insertBook(t, pool, teacher, "repeat-book", 40000)
	foreignBook := insertBook(t, pool, other, "repeat-foreign", 40000)
	foreignBundle := insertBundle(t, pool, other, "repeat-foreign-bundle", 90000, foreignBook)
	myBundle := insertBundle(t, pool, teacher, "repeat-bundle", 60000, book)

	bookRef := &book
	foreignRef := &foreignBook
	bundleRef := &myBundle
	foreignBundleRef := &foreignBundle

	// Two buyers, same book: the row must show 2 sales, and the money must be the sum once.
	insertOrder(t, pool, buyer, bookRef, nil, 40000, "paid")
	insertOrder(t, pool, otherBuyer, bookRef, nil, 40000, "paid")
	// Another teacher's bundle and book: none of this may reach my report.
	insertOrder(t, pool, buyer, nil, foreignBundleRef, 90000, "paid")
	insertOrder(t, pool, buyer, foreignRef, nil, 40000, "paid")
	// Mine through a bundle.
	insertOrder(t, pool, buyer, nil, bundleRef, 60000, "paid")

	summary, err := svc.Summary(t.Context(), teacher)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}

	if summary.Orders != 3 || summary.RevenueVND != 140000 {
		t.Errorf("summary = %+v, want 3 orders and 140000", summary)
	}
	if summary.BookRevenueVND != 80000 || summary.BundleRevenueVND != 60000 {
		t.Errorf("split = %d/%d, want 80000/60000",
			summary.BookRevenueVND, summary.BundleRevenueVND)
	}
	if len(summary.Books) != 1 || len(summary.Bundles) != 1 {
		t.Fatalf("rows = %d books, %d bundles, want 1 and 1",
			len(summary.Books), len(summary.Bundles))
	}
	if summary.Books[0].Sold != 2 || summary.Books[0].RevenueVND != 80000 {
		t.Errorf("book row = %+v, want 2 sold for 80000", summary.Books[0])
	}
}

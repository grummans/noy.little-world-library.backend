// Package orders turns a chosen book or bundle into a pending order and a VNPay
// payment URL, then reconciles the verified IPN callback with that order.
package orders

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"time"

	"littleworld/internal/vnpay"
)

const (
	StatusPending   = "pending"
	StatusPaid      = "paid"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// VNPay IPN response codes (from the Pay API specification).
const (
	RspSuccess         = "00"
	RspOrderNotFound   = "01"
	RspAlreadyPaid     = "02"
	RspInvalidAmount   = "04"
	RspInvalidSign     = "97"
	RspUnknownError    = "99"
	codeCancelled      = "24"
	paymentExpireAfter = 15 * time.Minute
	// A pending order is reusable only for the few seconds a double click can span: after
	// that its txn_ref already reached VNPay, which rejects the same reference again.
	reusablePendingWindow = 60 * time.Second
)

var (
	ErrNotFound       = errors.New("order not found")
	ErrValidation     = errors.New("validation failed")
	ErrNotPurchasable = errors.New("this item is not for sale")
	ErrGateway        = errors.New("payment gateway unavailable")
)

// Gateway is the VNPay client; tests can drive the same crypto with a test secret.
type Gateway interface {
	BuildURL(req vnpay.PaymentRequest) (string, error)
	Verify(query url.Values) (vnpay.Result, error)
}

type Service struct {
	db      *sql.DB
	gateway Gateway
	now     func() time.Time
}

func New(db *sql.DB, gateway Gateway) *Service {
	return &Service{db: db, gateway: gateway, now: time.Now}
}

type Order struct {
	ID            int64
	BuyerID       int64
	BookID        *int64
	BundleID      *int64
	AmountVND     int64
	Status        string
	TxnRef        string
	TransactionNo *string
	ResponseCode  *string
	PaidAt        *time.Time
	CreatedAt     time.Time
	// ItemTitle and ItemSlug are filled by ListByBuyer for display; they are joins,
	// not columns on the order row.
	ItemTitle string
	ItemSlug  string
}

// Checkout is what the browser needs to continue to VNPay.
type Checkout struct {
	Order      Order
	PaymentURL string
}

// Create opens a pending order for one book or one bundle and signs the payment URL.
// The amount always comes from the database, never from the request.
func (s *Service) Create(ctx context.Context, buyerID int64, bookSlug, bundleSlug, returnURL, clientIP string) (Checkout, error) {
	if (bookSlug == "") == (bundleSlug == "") {
		return Checkout{}, fmt.Errorf("%w: send exactly one of book_slug or bundle_slug", ErrValidation)
	}

	item, err := s.resolveItem(ctx, bookSlug, bundleSlug)
	if err != nil {
		return Checkout{}, err
	}

	owned, err := s.ownedAlready(ctx, buyerID, item)
	if err != nil {
		return Checkout{}, err
	}
	if owned {
		return Checkout{}, fmt.Errorf("%w: the buyer already owns this item", ErrNotPurchasable)
	}

	created := s.now()

	// A pending order for this item already exists (double click, second tab): continue
	// with it and sign a URL for ITS txn_ref, or VNPay cannot match the payment back.
	if existing, found, err := s.pendingOrder(ctx, buyerID, item); err != nil {
		return Checkout{}, err
	} else if found {
		return s.checkoutFor(ctx, existing, item, returnURL, clientIP, created)
	}

	txnRef, err := newTxnRef(created)
	if err != nil {
		return Checkout{}, err
	}

	// Sign before inserting: a pending order with no payment URL is dead weight, and
	// retrying would leave a trail of unusable orders.
	paymentURL, err := s.signPayment(txnRef, item, returnURL, clientIP, created)
	if err != nil {
		return Checkout{}, err
	}

	order := Order{BuyerID: buyerID, AmountVND: item.amountVND, TxnRef: txnRef,
		Status: StatusPending, BookID: item.bookID, BundleID: item.bundleID, CreatedAt: created}
	if err := s.db.QueryRowContext(ctx,
		`INSERT INTO orders (buyer_id, book_id, bundle_id, amount_vnd, status, vnpay_txn_ref, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id`,
		buyerID, item.bookID, item.bundleID, item.amountVND, StatusPending, txnRef, created,
	).Scan(&order.ID); err != nil {
		// A pending order for the same item can already exist (double click, second tab).
		// The unique index turns that into one order and the client just continues with it.
		if existing, found, findErr := s.pendingOrder(ctx, buyerID, item); findErr == nil && found {
			return s.checkoutFor(ctx, existing, item, returnURL, clientIP, created)
		}
		return Checkout{}, fmt.Errorf("create order: %w", err)
	}

	owned, ownedErr := s.ownedAlready(ctx, buyerID, item)
	if ownedErr != nil {
		// Unknown ownership: drop the new pending row so it can never be paid twice.
		if _, cancelErr := s.db.ExecContext(ctx,
			`UPDATE orders SET status = 'cancelled' WHERE id = $1 AND status = 'pending'`, order.ID,
		); cancelErr != nil {
			return Checkout{}, errors.Join(ownedErr, fmt.Errorf("cancel duplicate order: %w", cancelErr))
		}
		return Checkout{}, ownedErr
	}
	if owned {
		if _, cancelErr := s.db.ExecContext(ctx,
			`UPDATE orders SET status = 'cancelled' WHERE id = $1 AND status = 'pending'`, order.ID,
		); cancelErr != nil {
			return Checkout{}, fmt.Errorf("cancel duplicate order: %w", cancelErr)
		}
		return Checkout{}, fmt.Errorf("%w: the buyer already owns this item", ErrNotPurchasable)
	}

	return Checkout{Order: order, PaymentURL: paymentURL}, nil
}

type item struct {
	bookID    *int64
	bundleID  *int64
	title     string
	amountVND int64
}

// resolveItem only accepts published, paid products: a draft or free book must never
// reach checkout.
func (s *Service) resolveItem(ctx context.Context, bookSlug, bundleSlug string) (item, error) {
	if bookSlug != "" {
		var (
			id     int64
			title  string
			price  sql.NullInt64
			access string
			status string
		)
		err := s.db.QueryRowContext(ctx,
			`SELECT id, title, price_vnd, access_type, status FROM books WHERE slug = $1`, bookSlug,
		).Scan(&id, &title, &price, &access, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return item{}, ErrNotFound
		}
		if err != nil {
			return item{}, fmt.Errorf("read book: %w", err)
		}
		if status != "published" || access != "paid" || !price.Valid || price.Int64 <= 0 {
			return item{}, ErrNotPurchasable
		}
		return item{bookID: &id, title: title, amountVND: price.Int64}, nil
	}

	var (
		id     int64
		title  string
		price  int64
		status string
		books  int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT b.id, b.title, b.price_vnd, b.status,
		        (SELECT count(*) FROM bundle_books bb
		         JOIN books cb ON cb.id = bb.book_id
		         WHERE bb.bundle_id = b.id AND cb.status = 'published'
		           AND cb.access_type = 'paid' AND cb.price_vnd > 0)
		 FROM bundles b WHERE b.slug = $1`, bundleSlug,
	).Scan(&id, &title, &price, &status, &books)
	if errors.Is(err, sql.ErrNoRows) {
		return item{}, ErrNotFound
	}
	if err != nil {
		return item{}, fmt.Errorf("read bundle: %w", err)
	}
	if status != "published" || price <= 0 || books == 0 {
		return item{}, ErrNotPurchasable
	}
	return item{bundleID: &id, title: title, amountVND: price}, nil
}

func newTxnRef(now time.Time) (string, error) {
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate vnpay txn ref: %w", err)
	}
	return fmt.Sprintf("LW%s%s", now.Format("060102150405"), hex.EncodeToString(raw)), nil
}

// ownedAlready reports a paid order for the same buyer and product: buying twice is a
// mistake, not a feature.
func (s *Service) ownedAlready(ctx context.Context, buyerID int64, item item) (bool, error) {
	var owned bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM orders
			WHERE buyer_id = $1 AND status = 'paid'
			  AND book_id IS NOT DISTINCT FROM $2
			  AND bundle_id IS NOT DISTINCT FROM $3
		)`, buyerID, item.bookID, item.bundleID).Scan(&owned)
	if err != nil {
		return false, fmt.Errorf("read ownership: %w", err)
	}
	return owned, nil
}

// pendingOrder returns a still-reusable pending order. Anything older than
// reusablePendingWindow is cancelled first: its txn_ref was already sent to VNPay, and
// VNPay refuses a repeated reference ("Invalid data format", code 03), so a retry needs a
// new order rather than the same link again.
func (s *Service) pendingOrder(ctx context.Context, buyerID int64, item item) (Order, bool, error) {
	var order Order
	err := s.db.QueryRowContext(ctx,
		`SELECT id, buyer_id, book_id, bundle_id, amount_vnd, status, vnpay_txn_ref, created_at
		 FROM orders
		 WHERE buyer_id = $1 AND status = 'pending'
		   AND book_id IS NOT DISTINCT FROM $2
		   AND bundle_id IS NOT DISTINCT FROM $3
		 ORDER BY id DESC LIMIT 1`,
		buyerID, item.bookID, item.bundleID,
	).Scan(&order.ID, &order.BuyerID, &order.BookID, &order.BundleID, &order.AmountVND,
		&order.Status, &order.TxnRef, &order.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, false, nil
	}
	if err != nil {
		return Order{}, false, err
	}

	if s.now().Sub(order.CreatedAt) > reusablePendingWindow {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE orders SET status = 'cancelled' WHERE id = $1 AND status = 'pending'`,
			order.ID,
		); err != nil {
			return Order{}, false, fmt.Errorf("cancel stale order: %w", err)
		}
		return Order{}, false, nil
	}
	return order, true, nil
}

// signPayment builds the VNPay URL for one txn_ref and amount.
func (s *Service) signPayment(txnRef string, item item, returnURL, clientIP string, created time.Time) (string, error) {
	url, err := s.gateway.BuildURL(vnpay.PaymentRequest{
		TxnRef:    txnRef,
		AmountVND: item.amountVND,
		// The reference is part of the order info so two attempts for the same product are
		// never identical: VNPay rejects a repeated order description (code 03).
		OrderInfo:   vnpay.SanitizeText("Thanh toan " + item.title + " " + txnRef),
		ReturnURL:   returnURL,
		ClientIP:    clientIP,
		CreateDate:  created,
		ExpireAfter: paymentExpireAfter,
	})
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrGateway, err)
	}
	return url, nil
}

// checkoutFor signs a URL for an order that already exists in the database. The amount
// comes from the order, so the link always matches what the buyer is charged.
func (s *Service) checkoutFor(ctx context.Context, order Order, item item, returnURL, clientIP string, created time.Time) (Checkout, error) {
	if order.AmountVND != item.amountVND {
		return Checkout{}, fmt.Errorf("%w: the pending order amount no longer matches", ErrValidation)
	}
	// Sign with the order's own timestamp so the VNPay window does not restart.
	url, err := s.signPayment(order.TxnRef, item, returnURL, clientIP, order.CreatedAt)
	if err != nil {
		return Checkout{}, err
	}
	return Checkout{Order: order, PaymentURL: url}, nil
}

-- +goose Up
-- Catalog browsing (published only) and Teacher Studio listing.
CREATE INDEX books_owner_id_status_updated_at_idx ON books (owner_id, status, updated_at DESC);
CREATE INDEX books_published_idx ON books (status, published_at DESC) WHERE status = 'published';
CREATE INDEX books_access_type_price_vnd_idx ON books (access_type, price_vnd) WHERE status = 'published';

CREATE INDEX bundles_owner_id_status_updated_at_idx ON bundles (owner_id, status, updated_at DESC);
CREATE INDEX bundles_published_idx ON bundles (status, published_at DESC) WHERE status = 'published';

-- Parent Library + access checks always filter buyer_id together with the item.
CREATE INDEX orders_buyer_id_status_created_at_idx ON orders (buyer_id, status, created_at DESC);
CREATE INDEX orders_paid_book_idx ON orders (buyer_id, book_id) WHERE status = 'paid';
CREATE INDEX orders_paid_bundle_idx ON orders (buyer_id, bundle_id) WHERE status = 'paid';

-- VNPay sends the same transaction number twice on retried IPN calls.
CREATE UNIQUE INDEX orders_vnpay_transaction_no_key ON orders (vnpay_transaction_no)
	WHERE vnpay_transaction_no IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS orders_vnpay_transaction_no_key;
DROP INDEX IF EXISTS orders_paid_bundle_idx;
DROP INDEX IF EXISTS orders_paid_book_idx;
DROP INDEX IF EXISTS orders_buyer_id_status_created_at_idx;
DROP INDEX IF EXISTS bundles_published_idx;
DROP INDEX IF EXISTS bundles_owner_id_status_updated_at_idx;
DROP INDEX IF EXISTS books_access_type_price_vnd_idx;
DROP INDEX IF EXISTS books_published_idx;
DROP INDEX IF EXISTS books_owner_id_status_updated_at_idx;

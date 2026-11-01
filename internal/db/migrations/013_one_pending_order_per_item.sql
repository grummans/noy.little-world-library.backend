-- +goose Up
-- One open checkout per buyer and item: a double click or a second tab must not create
-- two payable orders. Paid history is untouched; only pending rows are constrained.
CREATE UNIQUE INDEX orders_one_pending_book
    ON orders (buyer_id, book_id)
    WHERE status = 'pending' AND book_id IS NOT NULL;

CREATE UNIQUE INDEX orders_one_pending_bundle
    ON orders (buyer_id, bundle_id)
    WHERE status = 'pending' AND bundle_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS orders_one_pending_book;
DROP INDEX IF EXISTS orders_one_pending_bundle;

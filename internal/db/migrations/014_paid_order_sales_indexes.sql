-- +goose Up
-- The revenue report groups paid orders per item, so the item column has to lead the
-- index. The existing orders_paid_book_idx (buyer_id, book_id) cannot serve it. The
-- IS NOT NULL predicates keep rows of the other item type out of each index.
CREATE INDEX orders_paid_book_sales_idx ON orders (book_id)
	WHERE status = 'paid' AND book_id IS NOT NULL;
CREATE INDEX orders_paid_bundle_sales_idx ON orders (bundle_id)
	WHERE status = 'paid' AND bundle_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS orders_paid_bundle_sales_idx;
DROP INDEX IF EXISTS orders_paid_book_sales_idx;

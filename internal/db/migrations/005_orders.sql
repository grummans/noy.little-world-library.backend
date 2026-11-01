-- +goose Up
CREATE TABLE orders (
	id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	buyer_id bigint NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
	book_id bigint REFERENCES books (id) ON DELETE RESTRICT,
	bundle_id bigint REFERENCES bundles (id) ON DELETE RESTRICT,
	amount_vnd bigint NOT NULL,
	status text NOT NULL DEFAULT 'pending',
	vnpay_txn_ref text NOT NULL UNIQUE,
	vnpay_transaction_no text,
	vnpay_response_code text,
	paid_at timestamptz,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT orders_single_item_check CHECK (num_nonnulls(book_id, bundle_id) = 1),
	CONSTRAINT orders_amount_check CHECK (amount_vnd > 0),
	CONSTRAINT orders_status_check CHECK (status IN ('pending', 'paid', 'failed', 'cancelled'))
);

CREATE TRIGGER orders_set_updated_at
	BEFORE UPDATE ON orders
	FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE IF EXISTS orders;

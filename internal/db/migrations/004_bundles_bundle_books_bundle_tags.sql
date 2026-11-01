-- +goose Up
CREATE TABLE bundles (
	id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	owner_id bigint NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
	slug text NOT NULL UNIQUE,
	title text NOT NULL,
	description text NOT NULL DEFAULT '',
	cover_object_key text,
	price_vnd bigint NOT NULL,
	status text NOT NULL DEFAULT 'draft',
	published_at timestamptz,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT bundles_status_check CHECK (status IN ('draft', 'published')),
	CONSTRAINT bundles_price_check CHECK (price_vnd > 0)
);

CREATE TRIGGER bundles_set_updated_at
	BEFORE UPDATE ON bundles
	FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE bundle_books (
	bundle_id bigint NOT NULL REFERENCES bundles (id) ON DELETE CASCADE,
	book_id bigint NOT NULL REFERENCES books (id) ON DELETE RESTRICT,
	position smallint NOT NULL,
	PRIMARY KEY (bundle_id, book_id),
	CONSTRAINT bundle_books_position_unique UNIQUE (bundle_id, position),
	CONSTRAINT bundle_books_position_check CHECK (position >= 1)
);

CREATE INDEX bundle_books_book_id_idx ON bundle_books (book_id);

CREATE TABLE bundle_tags (
	bundle_id bigint NOT NULL REFERENCES bundles (id) ON DELETE CASCADE,
	tag_id bigint NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
	PRIMARY KEY (bundle_id, tag_id)
);

CREATE INDEX bundle_tags_tag_id_bundle_id_idx ON bundle_tags (tag_id, bundle_id);

-- +goose Down
DROP TABLE IF EXISTS bundle_tags;
DROP TABLE IF EXISTS bundle_books;
DROP TABLE IF EXISTS bundles;

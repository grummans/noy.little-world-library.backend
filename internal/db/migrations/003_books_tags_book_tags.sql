-- +goose Up
CREATE TABLE books (
	id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	owner_id bigint NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
	slug text NOT NULL UNIQUE,
	title text NOT NULL,
	description text NOT NULL DEFAULT '',
	cover_object_key text,
	epub_object_key text,
	age_min smallint,
	age_max smallint,
	access_type text NOT NULL,
	price_vnd bigint,
	status text NOT NULL DEFAULT 'draft',
	published_at timestamptz,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT books_access_type_check CHECK (access_type IN ('free', 'paid')),
	CONSTRAINT books_status_check CHECK (status IN ('draft', 'published')),
	CONSTRAINT books_price_access_check CHECK (
		(access_type = 'free' AND price_vnd IS NULL)
		-- price_vnd > 0 alone is NULL-safe-wrong: NULL > 0 is NULL, which a CHECK accepts.
		OR (access_type = 'paid' AND price_vnd IS NOT NULL AND price_vnd > 0)
	),
	CONSTRAINT books_age_range_check CHECK (
		age_min IS NULL OR age_max IS NULL OR age_min <= age_max
	)
);

CREATE TRIGGER books_set_updated_at
	BEFORE UPDATE ON books
	FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE tags (
	id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	slug text NOT NULL UNIQUE,
	name text NOT NULL UNIQUE,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER tags_set_updated_at
	BEFORE UPDATE ON tags
	FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE book_tags (
	book_id bigint NOT NULL REFERENCES books (id) ON DELETE CASCADE,
	tag_id bigint NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
	PRIMARY KEY (book_id, tag_id)
);

CREATE INDEX book_tags_tag_id_book_id_idx ON book_tags (tag_id, book_id);

-- +goose Down
DROP TABLE IF EXISTS book_tags;
DROP TABLE IF EXISTS tags;
DROP TABLE IF EXISTS books;

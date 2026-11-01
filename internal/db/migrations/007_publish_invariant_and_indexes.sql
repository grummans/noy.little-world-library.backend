-- +goose Up
-- A published book must keep the data the storefront needs; the service checks this
-- too, but the database is the last line of defence against a concurrent update.
ALTER TABLE books ADD CONSTRAINT books_published_assets_check CHECK (
	status <> 'published'
	OR (description <> '' AND cover_object_key IS NOT NULL AND epub_object_key IS NOT NULL)
);

-- Bound the age range per column, not just age_min <= age_max.
ALTER TABLE books ADD CONSTRAINT books_age_bounds_check CHECK (
	(age_min IS NULL OR (age_min >= 0 AND age_min <= 18))
	AND (age_max IS NULL OR (age_max >= 0 AND age_max <= 18))
);

-- Teacher Studio lists by owner only, newest first; the status-first index does not
-- serve that ordering.
CREATE INDEX books_owner_id_updated_at_id_idx ON books (owner_id, updated_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS books_owner_id_updated_at_id_idx;
ALTER TABLE books DROP CONSTRAINT IF EXISTS books_age_bounds_check;
ALTER TABLE books DROP CONSTRAINT IF EXISTS books_published_assets_check;

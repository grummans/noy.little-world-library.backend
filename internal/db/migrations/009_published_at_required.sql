-- +goose Up
-- A published row without published_at breaks ordering and makes scans inconsistent.
ALTER TABLE books ADD CONSTRAINT books_published_at_check CHECK (
	status <> 'published' OR published_at IS NOT NULL
);
ALTER TABLE bundles ADD CONSTRAINT bundles_published_at_check CHECK (
	status <> 'published' OR published_at IS NOT NULL
);

-- +goose Down
ALTER TABLE bundles DROP CONSTRAINT IF EXISTS bundles_published_at_check;
ALTER TABLE books DROP CONSTRAINT IF EXISTS books_published_at_check;

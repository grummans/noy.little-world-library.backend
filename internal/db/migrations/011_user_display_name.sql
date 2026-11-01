-- +goose Up
-- People get a name, not an inbox. The column stays nullable so historical rows and test
-- fixtures remain valid; reads fall back to the email local part, and the API refuses to
-- create an account without a name.
ALTER TABLE users ADD COLUMN display_name text;

-- Backfill must not fail the deploy: an empty local part or a very long one would trip
-- the constraint below, so it is normalised first.
UPDATE users
SET display_name = NULLIF(left(split_part(email, '@', 1), 80), '')
WHERE display_name IS NULL;

ALTER TABLE users ADD CONSTRAINT users_display_name_check CHECK (
	display_name IS NULL OR char_length(display_name) BETWEEN 1 AND 80
);

-- +goose Down
-- Destructive: names typed by users are dropped. Only roll this back on a throwaway
-- database; in production keep the column and ship a new migration instead.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_display_name_check;
ALTER TABLE users DROP COLUMN IF EXISTS display_name;

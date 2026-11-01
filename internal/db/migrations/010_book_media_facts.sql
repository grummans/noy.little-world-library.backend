-- +goose Up
-- Page count, audio and video are derived from the EPUB itself at upload time: Book
-- Creator exports fixed-layout EPUB, where one spine item is one fixed page. NULL page
-- count means the file had no usable spine, and the UI hides the chip instead of lying.
ALTER TABLE books
	ADD COLUMN page_count smallint,
	ADD COLUMN has_audio boolean NOT NULL DEFAULT false,
	ADD COLUMN has_video boolean NOT NULL DEFAULT false;

ALTER TABLE books ADD CONSTRAINT books_page_count_check CHECK (
	page_count IS NULL OR page_count > 0
);

-- +goose Down
ALTER TABLE books DROP CONSTRAINT IF EXISTS books_page_count_check;
ALTER TABLE books DROP COLUMN IF EXISTS has_video;
ALTER TABLE books DROP COLUMN IF EXISTS has_audio;
ALTER TABLE books DROP COLUMN IF EXISTS page_count;

-- +goose Up
-- A book belongs to exactly one topic; bundles keep several.
--
-- There is no safe automatic rule for which topic a teacher meant when two are attached,
-- so this migration refuses rather than deleting: if book_tags still holds more than one
-- row for a book, the unique index fails and nothing else changes. To find them:
--
--   SELECT book_id, array_agg(tag_id ORDER BY tag_id)
--   FROM book_tags GROUP BY book_id HAVING count(*) > 1;
--
-- Keep one row per book, delete the others, then migrate again.
CREATE UNIQUE INDEX book_tags_one_per_book ON book_tags (book_id);

-- +goose Down
DROP INDEX IF EXISTS book_tags_one_per_book;

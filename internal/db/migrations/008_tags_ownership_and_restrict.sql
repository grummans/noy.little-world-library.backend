-- +goose Up
-- Teachers author topics, so a tag needs an owner: another teacher must not be able
-- to delete it out from under them.
ALTER TABLE tags ADD COLUMN created_by bigint REFERENCES users (id) ON DELETE SET NULL;

-- Deletion must be atomic with the "is it in use?" check. RESTRICT makes PostgreSQL
-- refuse the delete, so the check-then-delete race disappears.
ALTER TABLE book_tags DROP CONSTRAINT book_tags_tag_id_fkey;
ALTER TABLE book_tags ADD CONSTRAINT book_tags_tag_id_fkey
	FOREIGN KEY (tag_id) REFERENCES tags (id) ON DELETE RESTRICT;

ALTER TABLE bundle_tags DROP CONSTRAINT bundle_tags_tag_id_fkey;
ALTER TABLE bundle_tags ADD CONSTRAINT bundle_tags_tag_id_fkey
	FOREIGN KEY (tag_id) REFERENCES tags (id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE bundle_tags DROP CONSTRAINT bundle_tags_tag_id_fkey;
ALTER TABLE bundle_tags ADD CONSTRAINT bundle_tags_tag_id_fkey
	FOREIGN KEY (tag_id) REFERENCES tags (id) ON DELETE CASCADE;

ALTER TABLE book_tags DROP CONSTRAINT book_tags_tag_id_fkey;
ALTER TABLE book_tags ADD CONSTRAINT book_tags_tag_id_fkey
	FOREIGN KEY (tag_id) REFERENCES tags (id) ON DELETE CASCADE;

ALTER TABLE tags DROP COLUMN IF EXISTS created_by;

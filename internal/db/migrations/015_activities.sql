-- +goose Up
-- Wordwall games: the game itself stays on Wordwall, so a row is metadata, an embed
-- URL and a thumbnail object key. Owner is always a teacher (enforced by the API).
CREATE TABLE activities (
	id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	owner_id bigint NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
	slug text NOT NULL UNIQUE,
	title text NOT NULL,
	description text NOT NULL DEFAULT '',
	embed_url text NOT NULL,
	game_type text NOT NULL,
	topic text NOT NULL DEFAULT '',
	skills text[] NOT NULL DEFAULT '{}',
	age_min smallint,
	age_max smallint,
	thumbnail_object_key text,
	status text NOT NULL DEFAULT 'draft',
	published_at timestamptz,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT activities_status_check CHECK (status IN ('draft', 'published', 'hidden')),
	CONSTRAINT activities_age_range_check CHECK (
		age_min IS NULL OR age_max IS NULL OR age_min <= age_max
	),
	CONSTRAINT activities_published_at_check CHECK (
		status <> 'published' OR published_at IS NOT NULL
	)
);

CREATE TRIGGER activities_set_updated_at
	BEFORE UPDATE ON activities
	FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Teacher Studio reads its own games newest-first; the public catalog reads published
-- games only, so the partial index has no owner column and stays small.
CREATE INDEX activities_owner_status_idx ON activities (owner_id, status, updated_at DESC);
CREATE INDEX activities_published_idx ON activities (published_at DESC)
	WHERE status = 'published';

-- +goose Down
DROP TABLE IF EXISTS activities;

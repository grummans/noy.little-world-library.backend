package activities

// SQL for the activities table lives here so the service reads as business rules.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

const activityColumns = `a.id, a.owner_id, a.slug, a.title, a.description, a.embed_url,
	a.game_type, a.topic, a.skills, a.age_min, a.age_max, a.thumbnail_object_key,
	a.status, a.published_at, a.updated_at`

const selectActivity = `SELECT ` + activityColumns + ` FROM activities a`

// $1 search as slug, $2 raw search, $3 game type, $4 skill, $5 age, $6 limit, $7 offset.
// Only published games are reachable from the public catalog.
const listPublicQuery = selectActivity + `
WHERE a.status = 'published'
  AND ($2 = '' OR a.title ILIKE '%' || $2 || '%' ESCAPE '\'
       OR ($1 <> '' AND a.slug LIKE '%' || $1 || '%' ESCAPE '\'))
  AND ($3 = '' OR a.game_type = $3)
  AND ($4 = '' OR $4 = ANY (a.skills))
  -- COALESCE keeps a game with only one bound visible in its real range.
  AND ($5::smallint IS NULL
       OR (COALESCE(a.age_min, 0) <= $5 AND COALESCE(a.age_max, 18) >= $5))
ORDER BY a.published_at DESC, a.id DESC
LIMIT $6 OFFSET $7`

const listByOwnerQuery = selectActivity + `
WHERE a.owner_id = $1
ORDER BY a.updated_at DESC, a.id DESC`

const statsQuery = `SELECT count(*),
	count(*) FILTER (WHERE status = 'published'),
	count(*) FILTER (WHERE status = 'draft'),
	count(*) FILTER (WHERE status = 'hidden')
	FROM activities WHERE owner_id = $1`

const gameTypesQuery = `SELECT DISTINCT game_type FROM activities
	WHERE status = 'published' ORDER BY game_type`

const skillsQuery = `SELECT DISTINCT skill FROM activities, unnest(skills) AS skill
	WHERE status = 'published' ORDER BY skill`

type scanner interface {
	Scan(dest ...any) error
}

func scanActivity(row scanner) (Activity, error) {
	var a Activity
	err := row.Scan(&a.ID, &a.OwnerID, &a.Slug, &a.Title, &a.Description, &a.EmbedURL,
		&a.GameType, &a.Topic, &a.Skills, &a.AgeMin, &a.AgeMax, &a.ThumbnailKey,
		&a.Status, &a.PublishedAt, &a.UpdatedAt)
	if err != nil {
		return Activity{}, fmt.Errorf("scan activity: %w", err)
	}
	if a.Skills == nil {
		a.Skills = []string{}
	}
	return a, nil
}

func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrSlugTaken
	}
	return fmt.Errorf("save activity: %w", err)
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// lockStatus pins the row the caller is about to change, so a concurrent status change
// cannot slip between the publish checks and the update.
func lockStatus(ctx context.Context, db querier, activityID, ownerID int64) (string, error) {
	var status string
	err := db.QueryRowContext(ctx,
		`SELECT status FROM activities WHERE id = $1 AND owner_id = $2 FOR UPDATE`,
		activityID, ownerID,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lock activity: %w", err)
	}
	return status, nil
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

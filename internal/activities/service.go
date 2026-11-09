package activities

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"littleworld/internal/slug"
	"littleworld/internal/storage"
)

// List returns the teacher's own games newest-first plus the Studio counters.
func (s *Service) List(ctx context.Context, ownerID int64) ([]Activity, Stats, error) {
	rows, err := s.db.QueryContext(ctx, listByOwnerQuery, ownerID)
	if err != nil {
		return nil, Stats{}, fmt.Errorf("list activities: %w", err)
	}
	defer rows.Close()

	list, err := scanActivities(rows)
	if err != nil {
		return nil, Stats{}, err
	}

	var stats Stats
	if err := s.db.QueryRowContext(ctx, statsQuery, ownerID).Scan(
		&stats.Total, &stats.Published, &stats.Draft, &stats.Hidden,
	); err != nil {
		return nil, Stats{}, fmt.Errorf("activity stats: %w", err)
	}

	s.attachThumbnails(ctx, list)
	return list, stats, nil
}

func (s *Service) Create(ctx context.Context, ownerID int64, meta Metadata) (Activity, error) {
	normalized, err := normalize(meta)
	if err != nil {
		return Activity{}, err
	}

	var id int64
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO activities
		 (owner_id, slug, title, description, embed_url, game_type, topic, skills, age_min, age_max)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id`,
		ownerID, normalized.Slug, normalized.Title, normalized.Description, normalized.EmbedURL,
		normalized.GameType, normalized.Topic, normalized.Skills, normalized.AgeMin, normalized.AgeMax,
	).Scan(&id)
	if err != nil {
		return Activity{}, mapWriteError(err)
	}
	return s.Get(ctx, ownerID, id)
}

func (s *Service) Get(ctx context.Context, ownerID, activityID int64) (Activity, error) {
	activity, err := scanActivity(s.db.QueryRowContext(ctx,
		selectActivity+` WHERE a.id = $1 AND a.owner_id = $2`, activityID, ownerID))
	if errors.Is(err, sql.ErrNoRows) {
		return Activity{}, ErrNotFound
	}
	if err != nil {
		return Activity{}, err
	}

	list := []Activity{activity}
	s.attachThumbnails(ctx, list)
	return list[0], nil
}

func (s *Service) Update(ctx context.Context, ownerID, activityID int64, meta Metadata) (Activity, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Activity{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Locking first means the publish rules below read the same row the update writes.
	status, err := lockStatus(ctx, tx, activityID, ownerID)
	if err != nil {
		return Activity{}, err
	}

	// An edit that does not mention the slug keeps the current one: regenerating it from
	// a renamed title would silently break every published link.
	if strings.TrimSpace(meta.Slug) == "" {
		if err := tx.QueryRowContext(ctx,
			`SELECT slug FROM activities WHERE id = $1 AND owner_id = $2`, activityID, ownerID,
		).Scan(&meta.Slug); err != nil {
			return Activity{}, fmt.Errorf("read slug: %w", err)
		}
	}

	normalized, err := normalize(meta)
	if err != nil {
		return Activity{}, err
	}
	// Clearing a description on a live game would leave the catalog without the text the
	// publish rule requires, so it is a validation error rather than a silent edit.
	if status == StatusPublished && normalized.Description == "" {
		return Activity{}, fmt.Errorf("%w: a published game needs a description", ErrValidation)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE activities
		 SET slug = $1, title = $2, description = $3, embed_url = $4,
		     game_type = $5, topic = $6, skills = $7, age_min = $8, age_max = $9
		 WHERE id = $10 AND owner_id = $11`,
		normalized.Slug, normalized.Title, normalized.Description, normalized.EmbedURL,
		normalized.GameType, normalized.Topic, normalized.Skills, normalized.AgeMin,
		normalized.AgeMax, activityID, ownerID,
	); err != nil {
		return Activity{}, mapWriteError(err)
	}
	if err := tx.Commit(); err != nil {
		return Activity{}, fmt.Errorf("commit transaction: %w", err)
	}
	return s.Get(ctx, ownerID, activityID)
}

// SetStatus moves a game between draft, hidden and published. A teacher publishes
// their own game immediately; there is no moderation step.
func (s *Service) SetStatus(ctx context.Context, ownerID, activityID int64, status string) (Activity, error) {
	status, err := normalizeStatus(status)
	if err != nil {
		return Activity{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Activity{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := lockStatus(ctx, tx, activityID, ownerID); err != nil {
		return Activity{}, err
	}

	if status == StatusPublished {
		var description, gameType, embedURL string
		if err := tx.QueryRowContext(ctx,
			`SELECT description, game_type, embed_url FROM activities WHERE id = $1`, activityID,
		).Scan(&description, &gameType, &embedURL); err != nil {
			return Activity{}, fmt.Errorf("read activity: %w", err)
		}
		// The catalog card and player need all three, so publishing without them is refused.
		if description == "" || gameType == "" || embedURL == "" {
			return Activity{}, fmt.Errorf("%w: description, game type and embed URL are required", ErrNotPublishable)
		}
	}

	// Leaving published clears the timestamp, so a later publish gets a fresh one and the
	// catalog ordering never shows a hidden game with a past publish date.
	if _, err := tx.ExecContext(ctx,
		`UPDATE activities
		 SET status = $1,
		     published_at = CASE WHEN $1 = 'published' THEN now() ELSE NULL END
		 WHERE id = $2 AND owner_id = $3`,
		status, activityID, ownerID,
	); err != nil {
		return Activity{}, mapWriteError(err)
	}
	if err := tx.Commit(); err != nil {
		return Activity{}, fmt.Errorf("commit transaction: %w", err)
	}
	return s.Get(ctx, ownerID, activityID)
}

func (s *Service) SetThumbnail(ctx context.Context, ownerID, activityID int64, upload Upload) (Activity, error) {
	if s.store == nil {
		return Activity{}, storage.ErrNotConfigured
	}
	if err := storage.ValidateThumbnail(upload.ContentType, upload.Size); err != nil {
		return Activity{}, err
	}

	data, err := io.ReadAll(io.LimitReader(upload.Body, storage.MaxThumbnailBytes+1))
	if err != nil {
		return Activity{}, fmt.Errorf("read thumbnail: %w", err)
	}
	if int64(len(data)) > storage.MaxThumbnailBytes {
		return Activity{}, storage.ErrInvalidThumbnail
	}
	if err := storage.ValidateThumbnailImage(bytes.NewReader(data), int64(len(data)), upload.ContentType); err != nil {
		return Activity{}, err
	}

	// A unique key per upload means the object being replaced stays readable until the
	// row points at the new one, even when both uploads are the same format.
	key, err := storage.ActivityThumbnailKey(activityID, upload.ContentType,
		strconv.FormatInt(time.Now().UnixNano(), 10))
	if err != nil {
		return Activity{}, err
	}

	// Upload before taking the row lock: object I/O must not hold a database connection,
	// and a failure here leaves the current thumbnail untouched.
	if err := s.store.Put(ctx, key, bytes.NewReader(data), int64(len(data)), upload.ContentType); err != nil {
		return Activity{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.discardThumbnail(ctx, key)
		return Activity{}, fmt.Errorf("begin transaction: %w", err)
	}
	// Every failure path below rolls back through fail(), so the deferred rollback is
	// only a safety net for a path added later.
	defer func() { _ = tx.Rollback() }()
	fail := func(err error) (Activity, error) {
		// Release the row lock before the cleanup round-trip to the object store.
		_ = tx.Rollback()
		s.discardThumbnail(ctx, key)
		return Activity{}, err
	}

	var previous sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT thumbnail_object_key FROM activities WHERE id = $1 AND owner_id = $2 FOR UPDATE`,
		activityID, ownerID,
	).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return fail(ErrNotFound)
	}
	if err != nil {
		return fail(fmt.Errorf("lock activity: %w", err))
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE activities SET thumbnail_object_key = $1 WHERE id = $2 AND owner_id = $3`,
		key, activityID, ownerID,
	); err != nil {
		return fail(fmt.Errorf("save thumbnail: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return fail(fmt.Errorf("commit transaction: %w", err))
	}

	// Only after the commit is the old object unreferenced; deleting it earlier would
	// remove a live thumbnail if the transaction rolled back.
	if previous.Valid && previous.String != key {
		s.discardThumbnail(ctx, previous.String)
	}
	return s.Get(ctx, ownerID, activityID)
}

// discardThumbnail removes an orphaned object. It uses a context detached from the
// request, because the usual reason for the cleanup is that the request context is done.
func (s *Service) discardThumbnail(ctx context.Context, key string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	// A leftover object is harmless; the caller is already returning the real error.
	_ = s.store.Delete(cleanupCtx, key)
}

// PublicList is the catalog query: published games only, filtered and paginated.
func (s *Service) PublicList(ctx context.Context, filter Filter) ([]Activity, bool, error) {
	filter, err := normalizeFilter(filter)
	if err != nil {
		return nil, false, err
	}

	rows, err := s.db.QueryContext(ctx, listPublicQuery,
		slug.Make(filter.Query), escapeLike(filter.Query), filter.GameType, filter.Skill,
		filter.Age, filter.Limit+1, (filter.Page-1)*filter.Limit)
	if err != nil {
		return nil, false, fmt.Errorf("list public activities: %w", err)
	}
	defer rows.Close()

	list, err := scanActivities(rows)
	if err != nil {
		return nil, false, err
	}

	hasMore := len(list) > filter.Limit
	if hasMore {
		list = list[:filter.Limit]
	}
	s.attachThumbnails(ctx, list)
	return list, hasMore, nil
}

func (s *Service) PublicGet(ctx context.Context, activitySlug string) (Activity, error) {
	activity, err := scanActivity(s.db.QueryRowContext(ctx,
		selectActivity+` WHERE a.slug = $1 AND a.status = 'published'`, activitySlug))
	if errors.Is(err, sql.ErrNoRows) {
		return Activity{}, ErrNotFound
	}
	if err != nil {
		return Activity{}, err
	}

	list := []Activity{activity}
	s.attachThumbnails(ctx, list)
	return list[0], nil
}

func (s *Service) Facets(ctx context.Context) (Facets, error) {
	facets := Facets{GameTypes: []string{}, Skills: []string{}}

	rows, err := s.db.QueryContext(ctx, gameTypesQuery)
	if err != nil {
		return Facets{}, fmt.Errorf("activity game types: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var gameType string
		if err := rows.Scan(&gameType); err != nil {
			return Facets{}, fmt.Errorf("scan game type: %w", err)
		}
		facets.GameTypes = append(facets.GameTypes, gameType)
	}
	if err := rows.Err(); err != nil {
		return Facets{}, fmt.Errorf("activity game types: %w", err)
	}

	skillRows, err := s.db.QueryContext(ctx, skillsQuery)
	if err != nil {
		return Facets{}, fmt.Errorf("activity skills: %w", err)
	}
	defer skillRows.Close()
	for skillRows.Next() {
		var skill string
		if err := skillRows.Scan(&skill); err != nil {
			return Facets{}, fmt.Errorf("scan skill: %w", err)
		}
		facets.Skills = append(facets.Skills, skill)
	}
	if err := skillRows.Err(); err != nil {
		return Facets{}, fmt.Errorf("activity skills: %w", err)
	}
	return facets, nil
}

func scanActivities(rows *sql.Rows) ([]Activity, error) {
	list := []Activity{}
	for rows.Next() {
		activity, err := scanActivity(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, activity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate activities: %w", err)
	}
	return list, nil
}

// attachThumbnails fills the signed URL; a game without a thumbnail keeps an empty one.
func (s *Service) attachThumbnails(ctx context.Context, list []Activity) {
	if s.store == nil {
		return
	}
	for i := range list {
		if list[i].ThumbnailKey == nil {
			continue
		}
		url, err := s.store.PresignGet(ctx, *list[i].ThumbnailKey)
		if err != nil {
			continue
		}
		list[i].ThumbnailURL = url
	}
}

func normalizeFilter(filter Filter) (Filter, error) {
	filter.Query = strings.TrimSpace(filter.Query)
	filter.GameType = strings.TrimSpace(filter.GameType)
	filter.Skill = strings.TrimSpace(filter.Skill)

	if len(filter.GameType) > maxGameTypeLen || len(filter.Skill) > maxSkillLen {
		return Filter{}, fmt.Errorf("%w: filter value is too long", ErrValidation)
	}
	if filter.Age != nil && (*filter.Age < 0 || *filter.Age > maxAge) {
		return Filter{}, fmt.Errorf("%w: age must be between 0 and %d", ErrValidation, maxAge)
	}
	if filter.Page < 1 {
		filter.Page = 1
	}
	// A huge page number would overflow the offset arithmetic; the public page never
	// reaches this depth, so it is clamped instead of rejected.
	if filter.Page > maxPage {
		filter.Page = maxPage
	}
	switch {
	case filter.Limit < 1:
		filter.Limit = DefaultLimit
	case filter.Limit > DefaultLimit*2:
		filter.Limit = DefaultLimit * 2
	}
	return filter, nil
}

package httpapi

import (
	"errors"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"littleworld/internal/activities"
	"littleworld/internal/storage"
)

type activityResponse struct {
	ID           int64      `json:"id"`
	Slug         string     `json:"slug"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	EmbedURL     string     `json:"embed_url"`
	GameType     string     `json:"game_type"`
	Topic        string     `json:"topic"`
	Skills       []string   `json:"skills"`
	AgeMin       *int16     `json:"age_min"`
	AgeMax       *int16     `json:"age_max"`
	ThumbnailURL string     `json:"thumbnail_url"`
	Status       string     `json:"status"`
	PublishedAt  *time.Time `json:"published_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// newActivityResponse never exposes the object key: the signed URL is what the client
// needs, and the key is an internal detail of the bucket layout.
func newActivityResponse(a activities.Activity) activityResponse {
	return activityResponse{
		ID:           a.ID,
		Slug:         a.Slug,
		Title:        a.Title,
		Description:  a.Description,
		EmbedURL:     a.EmbedURL,
		GameType:     a.GameType,
		Topic:        a.Topic,
		Skills:       a.Skills,
		AgeMin:       a.AgeMin,
		AgeMax:       a.AgeMax,
		ThumbnailURL: a.ThumbnailURL,
		Status:       a.Status,
		PublishedAt:  a.PublishedAt,
		UpdatedAt:    a.UpdatedAt,
	}
}

// GET /api/activities?q=&game_type=&skill=&age=&page=&limit=
func (s *Server) handlePublicActivities(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	var age *int16
	if raw := query.Get("age"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 16)
		if err != nil || value < 0 || value > 18 {
			writeError(w, http.StatusBadRequest, "validation", "Age must be between 0 and 18.")
			return
		}
		parsed := int16(value)
		age = &parsed
	}

	page, err := positiveInt(query.Get("page"), 1)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", "Page must be a positive number.")
		return
	}
	limit, err := positiveInt(query.Get("limit"), activities.DefaultLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", "Limit must be a positive number.")
		return
	}

	list, hasMore, err := s.Activities.PublicList(r.Context(), activities.Filter{
		Query:    query.Get("q"),
		GameType: query.Get("game_type"),
		Skill:    query.Get("skill"),
		Age:      age,
		Page:     page,
		Limit:    limit,
	})
	if err != nil {
		s.writeActivityError(w, err, "list activities")
		return
	}

	resp := make([]activityResponse, 0, len(list))
	for _, activity := range list {
		resp = append(resp, newActivityResponse(activity))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "has_more": hasMore})
}

// GET /api/activities/facets
func (s *Server) handleActivityFacets(w http.ResponseWriter, r *http.Request) {
	facets, err := s.Activities.Facets(r.Context())
	if err != nil {
		s.writeActivityError(w, err, "activity facets")
		return
	}
	writeJSON(w, http.StatusOK, facets)
}

// GET /api/activities/{slug}
func (s *Server) handlePublicActivity(w http.ResponseWriter, r *http.Request) {
	activity, err := s.Activities.PublicGet(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.writeActivityError(w, err, "get activity")
		return
	}
	writeJSON(w, http.StatusOK, newActivityResponse(activity))
}

// GET /api/teacher/activities
func (s *Server) handleListActivities(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	list, stats, err := s.Activities.List(r.Context(), user.ID)
	if err != nil {
		s.writeActivityError(w, err, "list teacher activities")
		return
	}

	resp := make([]activityResponse, 0, len(list))
	for _, activity := range list {
		resp = append(resp, newActivityResponse(activity))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "stats": stats})
}

type activityRequest struct {
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	EmbedURL    string   `json:"embed_url"`
	GameType    string   `json:"game_type"`
	Topic       string   `json:"topic"`
	Skills      []string `json:"skills"`
	AgeMin      *int16   `json:"age_min"`
	AgeMax      *int16   `json:"age_max"`
}

func (in activityRequest) metadata() activities.Metadata {
	return activities.Metadata{
		Slug:        in.Slug,
		Title:       in.Title,
		Description: in.Description,
		EmbedURL:    in.EmbedURL,
		GameType:    in.GameType,
		Topic:       in.Topic,
		Skills:      in.Skills,
		AgeMin:      in.AgeMin,
		AgeMax:      in.AgeMax,
	}
}

// POST /api/teacher/activities
func (s *Server) handleCreateActivity(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	var in activityRequest
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Malformed request body.")
		return
	}

	activity, err := s.Activities.Create(r.Context(), user.ID, in.metadata())
	if err != nil {
		s.writeActivityError(w, err, "create activity")
		return
	}
	writeJSON(w, http.StatusCreated, newActivityResponse(activity))
}

// GET /api/teacher/activities/{id}
func (s *Server) handleGetActivity(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	activityID, ok := pathID(w, r)
	if !ok {
		return
	}
	activity, err := s.Activities.Get(r.Context(), user.ID, activityID)
	if err != nil {
		s.writeActivityError(w, err, "get activity")
		return
	}
	writeJSON(w, http.StatusOK, newActivityResponse(activity))
}

// PUT /api/teacher/activities/{id}
func (s *Server) handleUpdateActivity(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	activityID, ok := pathID(w, r)
	if !ok {
		return
	}

	var in activityRequest
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Malformed request body.")
		return
	}

	activity, err := s.Activities.Update(r.Context(), user.ID, activityID, in.metadata())
	if err != nil {
		s.writeActivityError(w, err, "update activity")
		return
	}
	writeJSON(w, http.StatusOK, newActivityResponse(activity))
}

// PUT /api/teacher/activities/{id}/status  body: {"status":"draft|hidden|published"}
func (s *Server) handleSetActivityStatus(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	activityID, ok := pathID(w, r)
	if !ok {
		return
	}

	var in struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Malformed request body.")
		return
	}

	activity, err := s.Activities.SetStatus(r.Context(), user.ID, activityID, in.Status)
	if err != nil {
		s.writeActivityError(w, err, "set activity status")
		return
	}
	writeJSON(w, http.StatusOK, newActivityResponse(activity))
}

// PUT /api/teacher/activities/{id}/thumbnail  multipart field: "thumbnail"
func (s *Server) handleReplaceActivityThumbnail(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	activityID, ok := pathID(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, storage.MaxThumbnailBytes+1<<20)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Could not read the upload.")
		return
	}

	var opened []multipart.File
	defer func() {
		for _, file := range opened {
			file.Close()
		}
	}()

	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "A thumbnail image is required.")
		return
	}
	opened = append(opened, file)

	activity, err := s.Activities.SetThumbnail(r.Context(), user.ID, activityID, activities.Upload{
		ContentType: header.Header.Get("Content-Type"),
		Size:        header.Size,
		Body:        file,
	})
	if err != nil {
		s.writeActivityError(w, err, "replace activity thumbnail")
		return
	}
	writeJSON(w, http.StatusOK, newActivityResponse(activity))
}

func (s *Server) writeActivityError(w http.ResponseWriter, err error, action string) {
	switch {
	case errors.Is(err, activities.ErrValidation):
		writeError(w, http.StatusBadRequest, "validation", err.Error())
	case errors.Is(err, activities.ErrSlugTaken):
		writeError(w, http.StatusConflict, "slug_taken", "That URL slug is already used.")
	case errors.Is(err, activities.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Game not found.")
	case errors.Is(err, activities.ErrNotPublishable):
		writeError(w, http.StatusConflict, "not_publishable", "Add a description, a game type and an embed URL first.")
	case errors.Is(err, storage.ErrInvalidThumbnail):
		s.Log.Warn("rejected thumbnail", "err", err)
		writeError(w, http.StatusBadRequest, "invalid_file", err.Error())
	case errors.Is(err, storage.ErrNotConfigured):
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Uploads are not available.")
	default:
		s.Log.Error(action, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}

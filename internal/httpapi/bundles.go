package httpapi

import (
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"littleworld/internal/bundles"
	"littleworld/internal/storage"
	"littleworld/internal/tags"
)

type bundleResponse struct {
	ID          int64      `json:"id"`
	Slug        string     `json:"slug"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	PriceVND    int64      `json:"price_vnd"`
	Status      string     `json:"status"`
	BookIDs     []int64    `json:"book_ids"`
	TagIDs      []int64    `json:"tag_ids"`
	HasCover    bool       `json:"has_cover"`
	PublishedAt *time.Time `json:"published_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func newBundleResponse(b bundles.Bundle) bundleResponse {
	return bundleResponse{
		ID:          b.ID,
		Slug:        b.Slug,
		Title:       b.Title,
		Description: b.Description,
		PriceVND:    b.PriceVND,
		Status:      b.Status,
		BookIDs:     b.BookIDs,
		TagIDs:      b.Tags,
		HasCover:    b.CoverKey != nil,
		PublishedAt: b.PublishedAt,
		UpdatedAt:   b.UpdatedAt,
	}
}

func (s *Server) handleListBundles(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	list, err := s.Bundles.ListByOwner(r.Context(), user.ID)
	if err != nil {
		s.writeBooksError(w, err, "list bundles")
		return
	}

	resp := make([]bundleResponse, 0, len(list))
	for _, b := range list {
		resp = append(resp, newBundleResponse(b))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCreateBundle(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	input, closeFiles, err := parseBundleMultipart(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Could not read the upload.")
		return
	}
	defer closeFiles()

	bundle, err := s.Bundles.Create(r.Context(), user.ID, input)
	if err != nil {
		s.writeBooksError(w, err, "create bundle")
		return
	}
	writeJSON(w, http.StatusCreated, newBundleResponse(bundle))
}

func (s *Server) handleGetBundle(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	bundleID, ok := pathID(w, r)
	if !ok {
		return
	}
	bundle, err := s.Bundles.Get(r.Context(), user.ID, bundleID)
	if err != nil {
		s.writeBooksError(w, err, "get bundle")
		return
	}
	writeJSON(w, http.StatusOK, newBundleResponse(bundle))
}

func (s *Server) handleUpdateBundle(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	bundleID, ok := pathID(w, r)
	if !ok {
		return
	}

	input, closeFiles, err := parseBundleMultipart(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Could not read the upload.")
		return
	}
	defer closeFiles()
	if input.Cover != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Covers use PUT /api/teacher/bundles/{id}/cover.")
		return
	}

	bundle, err := s.Bundles.Update(r.Context(), user.ID, bundleID, input)
	if err != nil {
		s.writeBooksError(w, err, "update bundle")
		return
	}
	writeJSON(w, http.StatusOK, newBundleResponse(bundle))
}

func (s *Server) handleReplaceBundleCover(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	bundleID, ok := pathID(w, r)
	if !ok {
		return
	}

	input, closeFiles, err := parseBundleMultipart(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Could not read the upload.")
		return
	}
	defer closeFiles()
	if input.Cover == nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "A cover file is required.")
		return
	}

	bundle, err := s.Bundles.ReplaceCover(r.Context(), user.ID, bundleID, *input.Cover)
	if err != nil {
		s.writeBooksError(w, err, "replace bundle cover")
		return
	}
	writeJSON(w, http.StatusOK, newBundleResponse(bundle))
}

func (s *Server) handlePublishBundle(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	bundleID, ok := pathID(w, r)
	if !ok {
		return
	}
	bundle, err := s.Bundles.Publish(r.Context(), user.ID, bundleID)
	if err != nil {
		s.writeBooksError(w, err, "publish bundle")
		return
	}
	writeJSON(w, http.StatusOK, newBundleResponse(bundle))
}

func (s *Server) handleUnpublishBundle(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	bundleID, ok := pathID(w, r)
	if !ok {
		return
	}
	bundle, err := s.Bundles.Unpublish(r.Context(), user.ID, bundleID)
	if err != nil {
		s.writeBooksError(w, err, "unpublish bundle")
		return
	}
	writeJSON(w, http.StatusOK, newBundleResponse(bundle))
}

func (s *Server) handleCreateTag(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	var in struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Malformed request body.")
		return
	}

	tag, err := tags.Create(r.Context(), s.DB, user.ID, in.Name)
	switch {
	case errors.Is(err, tags.ErrInvalidTag):
		writeError(w, http.StatusBadRequest, "validation", "Tag name must be 1-50 characters.")
	case errors.Is(err, tags.ErrTagExists):
		writeError(w, http.StatusConflict, "tag_exists", "That topic already exists; pick it from the list.")
	case err != nil:
		s.Log.Error("create tag", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not create the topic.")
	default:
		writeJSON(w, http.StatusCreated, newTagResponse(tag))
	}
}

func (s *Server) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	tagID, ok := pathID(w, r)
	if !ok {
		return
	}

	switch err := tags.Delete(r.Context(), s.DB, tagID, user.ID); {
	case errors.Is(err, tags.ErrTagInUse):
		writeError(w, http.StatusConflict, "tag_in_use", "Remove the topic from every book and bundle first.")
	case errors.Is(err, tags.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Topic not found.")
	case err != nil:
		s.Log.Error("delete tag", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not delete the topic.")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	list, err := tags.List(r.Context(), s.DB)
	if err != nil {
		s.Log.Error("list tags", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not load tags.")
		return
	}

	resp := make([]tagResponse, 0, len(list))
	for _, tag := range list {
		resp = append(resp, newTagResponse(tag))
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseBundleMultipart reads metadata, the ordered book ids and an optional cover.
func parseBundleMultipart(w http.ResponseWriter, r *http.Request) (bundles.CreateInput, func(), error) {
	r.Body = http.MaxBytesReader(w, r.Body, storage.MaxCoverBytes+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		return bundles.CreateInput{}, func() {}, err
	}

	meta := bundles.Metadata{
		Slug:        r.FormValue("slug"),
		Title:       r.FormValue("title"),
		Description: r.FormValue("description"),
	}
	if err := parseOptionalInt64(r.FormValue("price_vnd"), func(v int64) { meta.PriceVND = &v }); err != nil {
		return bundles.CreateInput{}, func() {}, err
	}
	tagIDs, err := parseIDs(r.MultipartForm.Value["tag_ids"])
	if err != nil {
		return bundles.CreateInput{}, func() {}, err
	}
	meta.TagIDs = tagIDs

	var opened []multipart.File
	closeFiles := func() {
		for _, f := range opened {
			f.Close()
		}
	}

	cover, err := formUploadAs(w, r, "cover", &opened)
	if err != nil {
		closeFiles()
		return bundles.CreateInput{}, func() {}, err
	}

	bookIDs, err := parseIDs(r.MultipartForm.Value["book_ids"])
	if err != nil {
		closeFiles()
		return bundles.CreateInput{}, func() {}, err
	}

	return bundles.CreateInput{
		Metadata: meta,
		BookIDs:  bookIDs,
		Cover:    cover,
	}, closeFiles, nil
}

// parseIDs parses repeated numeric form fields. An invalid value rejects the whole
// request instead of silently emptying the list.
func parseIDs(values []string) ([]int64, error) {
	ids := make([]int64, 0, len(values))
	for _, raw := range values {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid id %q", raw)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

type tagResponse struct {
	ID        int64  `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	CreatedBy *int64 `json:"created_by"`
}

func newTagResponse(tag tags.Tag) tagResponse {
	return tagResponse{ID: tag.ID, Slug: tag.Slug, Name: tag.Name, CreatedBy: tag.CreatedBy}
}

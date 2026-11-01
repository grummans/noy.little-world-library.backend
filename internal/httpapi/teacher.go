package httpapi

import (
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"littleworld/internal/books"
	"littleworld/internal/bundles"
	"littleworld/internal/storage"
	"littleworld/internal/tags"
)

// One request carries metadata plus at most one EPUB and one cover.
const maxUploadBody = storage.MaxEpubBytes + storage.MaxCoverBytes + 1<<20

type bookResponse struct {
	ID          int64      `json:"id"`
	Slug        string     `json:"slug"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	AccessType  string     `json:"access_type"`
	PriceVND    *int64     `json:"price_vnd"`
	AgeMin      *int16     `json:"age_min"`
	AgeMax      *int16     `json:"age_max"`
	Status      string     `json:"status"`
	PageCount   *int16     `json:"page_count"`
	HasAudio    bool       `json:"has_audio"`
	HasVideo    bool       `json:"has_video"`
	TagIDs      []int64    `json:"tag_ids"`
	HasEpub     bool       `json:"has_epub"`
	HasCover    bool       `json:"has_cover"`
	PublishedAt *time.Time `json:"published_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func newBookResponse(b books.Book) bookResponse {
	resp := bookResponse{
		ID:          b.ID,
		Slug:        b.Slug,
		Title:       b.Title,
		Description: b.Description,
		AccessType:  b.AccessType,
		PriceVND:    b.PriceVND,
		AgeMin:      b.AgeMin,
		AgeMax:      b.AgeMax,
		Status:      b.Status,
		PageCount:   b.PageCount,
		HasAudio:    b.HasAudio,
		HasVideo:    b.HasVideo,
		TagIDs:      b.Tags,
		HasEpub:     b.EpubKey != nil,
		HasCover:    b.CoverKey != nil,
		PublishedAt: b.PublishedAt,
		UpdatedAt:   b.UpdatedAt,
	}
	return resp
}

func (s *Server) handleListBooks(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	list, err := s.Books.ListByOwner(r.Context(), user.ID)
	if err != nil {
		s.writeBooksError(w, err, "list books")
		return
	}

	resp := make([]bookResponse, 0, len(list))
	for _, b := range list {
		resp = append(resp, newBookResponse(b))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCreateBook(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	input, closeFiles, err := parseBookMultipart(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Could not read the upload.")
		return
	}
	defer closeFiles()

	book, err := s.Books.Create(r.Context(), user.ID, input)
	if err != nil {
		s.writeBooksError(w, err, "create book")
		return
	}
	writeJSON(w, http.StatusCreated, newBookResponse(book))
}

func (s *Server) handleGetBook(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	bookID, ok := pathID(w, r)
	if !ok {
		return
	}
	book, err := s.Books.Get(r.Context(), user.ID, bookID)
	if err != nil {
		s.writeBooksError(w, err, "get book")
		return
	}
	writeJSON(w, http.StatusOK, newBookResponse(book))
}

func (s *Server) handleUpdateBook(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	bookID, ok := pathID(w, r)
	if !ok {
		return
	}

	var in struct {
		Slug        string        `json:"slug"`
		Title       string        `json:"title"`
		Description string        `json:"description"`
		AccessType  string        `json:"access_type"`
		PriceVND    *int64        `json:"price_vnd"`
		AgeMin      *int16        `json:"age_min"`
		AgeMax      *int16        `json:"age_max"`
		TagID       optionalInt64 `json:"tag_id"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Malformed request body.")
		return
	}

	// A client that omits tag_id means "leave the topic alone"; an explicit null clears it.
	tagID := in.TagID.Value
	if !in.TagID.Set {
		current, err := s.Books.Get(r.Context(), user.ID, bookID)
		if err != nil {
			s.writeBooksError(w, err, "read book")
			return
		}
		if len(current.Tags) > 0 {
			tagID = &current.Tags[0]
		}
	}

	book, err := s.Books.Update(r.Context(), user.ID, bookID, books.Metadata{
		Slug:        in.Slug,
		Title:       in.Title,
		Description: in.Description,
		AccessType:  in.AccessType,
		PriceVND:    in.PriceVND,
		AgeMin:      in.AgeMin,
		AgeMax:      in.AgeMax,
		TagID:       tagID,
	})
	if err != nil {
		s.writeBooksError(w, err, "update book")
		return
	}
	writeJSON(w, http.StatusOK, newBookResponse(book))
}

func (s *Server) handleReplaceBookFiles(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	bookID, ok := pathID(w, r)
	if !ok {
		return
	}

	input, closeFiles, err := parseBookMultipart(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Could not read the upload.")
		return
	}
	defer closeFiles()

	book, err := s.Books.ReplaceFiles(r.Context(), user.ID, bookID, input)
	if err != nil {
		s.writeBooksError(w, err, "replace book files")
		return
	}
	writeJSON(w, http.StatusOK, newBookResponse(book))
}

func (s *Server) handlePublishBook(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	bookID, ok := pathID(w, r)
	if !ok {
		return
	}
	book, err := s.Books.Publish(r.Context(), user.ID, bookID)
	if err != nil {
		s.writeBooksError(w, err, "publish book")
		return
	}
	writeJSON(w, http.StatusOK, newBookResponse(book))
}

func (s *Server) handleUnpublishBook(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	bookID, ok := pathID(w, r)
	if !ok {
		return
	}
	book, err := s.Books.Unpublish(r.Context(), user.ID, bookID)
	if err != nil {
		s.writeBooksError(w, err, "unpublish book")
		return
	}
	writeJSON(w, http.StatusOK, newBookResponse(book))
}

// parseBookMultipart reads metadata fields plus optional "epub" and "cover" files.
func parseBookMultipart(w http.ResponseWriter, r *http.Request) (books.CreateInput, func(), error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		return books.CreateInput{}, func() {}, err
	}

	meta := books.Metadata{
		Slug:        r.FormValue("slug"),
		Title:       r.FormValue("title"),
		Description: r.FormValue("description"),
		AccessType:  r.FormValue("access_type"),
	}
	if err := parseOptionalInt64(r.FormValue("price_vnd"), func(v int64) { meta.PriceVND = &v }); err != nil {
		return books.CreateInput{}, func() {}, err
	}
	if err := parseOptionalInt16(r.FormValue("age_min"), func(v int16) { meta.AgeMin = &v }); err != nil {
		return books.CreateInput{}, func() {}, err
	}
	if err := parseOptionalInt16(r.FormValue("age_max"), func(v int16) { meta.AgeMax = &v }); err != nil {
		return books.CreateInput{}, func() {}, err
	}
	if err := parseOptionalInt64(r.FormValue("tag_id"), func(v int64) { meta.TagID = &v }); err != nil {
		return books.CreateInput{}, func() {}, err
	}

	var files []multipart.File
	closeFiles := func() {
		for _, f := range files {
			f.Close()
		}
	}

	epub, err := formUpload(w, r, "epub", &files)
	if err != nil {
		closeFiles()
		return books.CreateInput{}, func() {}, err
	}
	cover, err := formUpload(w, r, "cover", &files)
	if err != nil {
		closeFiles()
		return books.CreateInput{}, func() {}, err
	}

	return books.CreateInput{Metadata: meta, Epub: epub, Cover: cover}, closeFiles, nil
}

func formUpload(w http.ResponseWriter, r *http.Request, field string, opened *[]multipart.File) (*books.Upload, error) {
	file, header, err := r.FormFile(field)
	if errors.Is(err, http.ErrMissingFile) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	*opened = append(*opened, file)

	return &books.Upload{
		ContentType: header.Header.Get("Content-Type"),
		Size:        header.Size,
		Body:        file,
	}, nil
}

// formUploadAs is formUpload for packages with their own Upload type.
func formUploadAs(w http.ResponseWriter, r *http.Request, field string, opened *[]multipart.File) (*bundles.Upload, error) {
	file, header, err := r.FormFile(field)
	if errors.Is(err, http.ErrMissingFile) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	*opened = append(*opened, file)

	return &bundles.Upload{
		ContentType: header.Header.Get("Content-Type"),
		Size:        header.Size,
		Body:        file,
	}, nil
}

func parseOptionalInt64(raw string, set func(int64)) error {
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return err
	}
	set(value)
	return nil
}

func parseOptionalInt16(raw string, set func(int16)) error {
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseInt(raw, 10, 16)
	if err != nil {
		return err
	}
	set(int16(value))
	return nil
}

// pathID parses {id}; the message stays resource-neutral because books and bundles
// share it.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
		return 0, false
	}
	return id, true
}

// writeBooksError maps the domain errors of books, bundles and tags to HTTP codes.
func (s *Server) writeBooksError(w http.ResponseWriter, err error, action string) {
	switch {
	case errors.Is(err, books.ErrValidation), errors.Is(err, bundles.ErrValidation),
		errors.Is(err, bundles.ErrInvalidBook), errors.Is(err, tags.ErrUnknownTag):
		writeError(w, http.StatusBadRequest, "validation", err.Error())
	case errors.Is(err, books.ErrSlugTaken), errors.Is(err, bundles.ErrSlugTaken):
		writeError(w, http.StatusConflict, "slug_taken", "That URL slug is already used.")
	case errors.Is(err, books.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Book not found.")
	case errors.Is(err, bundles.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Bundle not found.")
	case errors.Is(err, books.ErrNotPublishable):
		writeError(w, http.StatusConflict, "not_publishable", "Add a cover, an EPUB and a description first.")
	case errors.Is(err, bundles.ErrNotPublishable):
		writeError(w, http.StatusConflict, "not_publishable", "Add at least one paid book first.")
	case errors.Is(err, storage.ErrInvalidEpub), errors.Is(err, storage.ErrInvalidImage):
		// A rejected file is a client error, but the reason must be visible in the log
		// because the wire response stays deliberately vague.
		s.Log.Warn("rejected upload", "err", err)
		writeError(w, http.StatusBadRequest, "invalid_file", err.Error())
	case errors.Is(err, storage.ErrNotConfigured):
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Uploads are not available.")
	default:
		s.Log.Error(action, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}

// optionalInt64 tells "field absent" apart from "field explicitly null", which matters
// for tag_id: absent keeps the current topic, null clears it.
type optionalInt64 struct {
	Set   bool
	Value *int64
}

func (o *optionalInt64) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}

	var v int64
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

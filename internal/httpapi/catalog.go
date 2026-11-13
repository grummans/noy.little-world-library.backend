package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"littleworld/internal/catalog"
	"littleworld/internal/reading"
)

type cardResponse struct {
	Kind        string    `json:"kind"`
	ID          int64     `json:"id"`
	Slug        string    `json:"slug"`
	Title       string    `json:"title"`
	CoverURL    string    `json:"cover_url"`
	AccessType  string    `json:"access_type"`
	PriceVND    *int64    `json:"price_vnd"`
	AgeMin      *int16    `json:"age_min"`
	AgeMax      *int16    `json:"age_max"`
	PageCount   *int16    `json:"page_count"`
	HasAudio    bool      `json:"has_audio"`
	HasVideo    bool      `json:"has_video"`
	TagSlugs    []string  `json:"tag_slugs"`
	BookCount   int       `json:"book_count,omitempty"`
	PublishedAt time.Time `json:"published_at"`
}

func newCardResponse(card catalog.Card) cardResponse {
	return cardResponse{
		Kind:        card.Kind,
		ID:          card.ID,
		Slug:        card.Slug,
		Title:       card.Title,
		CoverURL:    card.CoverURL,
		AccessType:  card.AccessType,
		PriceVND:    card.PriceVND,
		AgeMin:      card.AgeMin,
		AgeMax:      card.AgeMax,
		PageCount:   card.PageCount,
		HasAudio:    card.HasAudio,
		HasVideo:    card.HasVideo,
		TagSlugs:    card.TagSlugs,
		BookCount:   card.BookCount,
		PublishedAt: card.PublishedAt,
	}
}

// GET /api/books?kind=all|book|bundle&q=&tag=&age=&page=&limit=
func (s *Server) handleCatalogList(w http.ResponseWriter, r *http.Request) {
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
	limit, err := positiveInt(query.Get("limit"), catalog.DefaultLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", "Limit must be a positive number.")
		return
	}

	cards, hasMore, err := s.Catalog.List(r.Context(), catalog.Filter{
		Kind:    query.Get("kind"),
		Query:   query.Get("q"),
		TagSlug: query.Get("tag"),
		Age:     age,
		Page:    page,
		Limit:   limit,
	})
	if err != nil {
		s.writeCatalogError(w, err)
		return
	}

	resp := make([]cardResponse, 0, len(cards))
	for _, card := range cards {
		resp = append(resp, newCardResponse(card))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "has_more": hasMore})
}

// GET /api/books/{slug}
func (s *Server) handleCatalogBook(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	detail, err := s.Catalog.Book(r.Context(), slug)
	if err != nil {
		s.writeCatalogError(w, err)
		return
	}

	// access tells the page whether to offer "Đọc ngay" or checkout: a buyer must not be
	// asked to pay twice. Anonymous callers only ever get "free".
	access := ""
	if detail.Card.AccessType == catalog.AccessFree {
		access = "free"
	} else if user, ok := userFrom(r); ok {
		if source, err := s.Reading.CanRead(r.Context(), user.ID, slug); err == nil {
			access = source
		} else if !errors.Is(err, reading.ErrNotFound) {
			s.Log.Error("catalog access", "err", err)
		}
	}

	// access answers for one caller, so a shared cache must never replay it: a buyer's
	// "purchase" would otherwise be served to everyone else, or "" cached for the buyer.
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"book":        newCardResponse(detail.Card),
		"description": detail.Description,
		"has_epub":    detail.HasEPUB,
		"access":      access,
	})
}

// GET /api/bundles/{slug}
func (s *Server) handleCatalogBundle(w http.ResponseWriter, r *http.Request) {
	detail, err := s.Catalog.Bundle(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.writeCatalogError(w, err)
		return
	}

	books := make([]cardResponse, 0, len(detail.Books))
	for _, card := range detail.Books {
		books = append(books, newCardResponse(card))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bundle":      newCardResponse(detail.Card),
		"description": detail.Description,
		"books":       books,
	})
}

func (s *Server) writeCatalogError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, catalog.ErrValidation):
		writeError(w, http.StatusBadRequest, "validation", err.Error())
	case errors.Is(err, catalog.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
	default:
		s.Log.Error("catalog", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not load the library.")
	}
}

// positiveInt parses an optional positive query value; junk is a client error, not a
// silent fallback.
func positiveInt(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, errors.New("invalid positive integer")
	}
	return value, nil
}

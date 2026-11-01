package httpapi

import (
	"net/http"
	"time"
)

type originResponse struct {
	Type       string `json:"type"`
	BundleSlug string `json:"bundle_slug,omitempty"`
	BundleName string `json:"bundle_name,omitempty"`
}

type readableResponse struct {
	Book   cardResponse   `json:"book"`
	Origin originResponse `json:"origin"`
}

// shelfBundleResponse keeps the bundle as a product card. Its books are not repeated
// here: they show up in "continue" with the bundle as their origin (PLAN §5).
type shelfBundleResponse struct {
	cardResponse
	PurchasedAt *time.Time `json:"purchased_at"`
}

// GET /api/parent/library — bundles and books the parent bought, plus what they can
// keep reading and where each book came from.
func (s *Server) handleParentLibrary(w http.ResponseWriter, r *http.Request) {
	user, _ := userFrom(r)

	shelf, err := s.Library.Load(r.Context(), user.ID)
	if err != nil {
		s.Log.Error("load library", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not load your library.")
		return
	}

	bundles := make([]shelfBundleResponse, 0, len(shelf.Bundles))
	for _, item := range shelf.Bundles {
		entry := shelfBundleResponse{cardResponse: newCardResponse(item.Bundle)}
		if !item.PurchasedAt.IsZero() {
			purchased := item.PurchasedAt
			entry.PurchasedAt = &purchased
		}
		bundles = append(bundles, entry)
	}

	books := make([]cardResponse, 0, len(shelf.Books))
	for _, book := range shelf.Books {
		books = append(books, newCardResponse(book))
	}

	readable := make([]readableResponse, 0, len(shelf.Readable))
	for _, item := range shelf.Readable {
		readable = append(readable, readableResponse{
			Book: newCardResponse(item.Book),
			Origin: originResponse{
				Type:       item.Origin.Type,
				BundleSlug: item.Origin.BundleSlug,
				BundleName: item.Origin.BundleName,
			},
		})
	}

	// A private shelf must never be replayed by a shared cache.
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"bundles":  bundles,
		"books":    books,
		"readable": readable,
	})
}

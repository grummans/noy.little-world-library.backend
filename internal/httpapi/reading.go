package httpapi

import (
	"context"
	"errors"
	"net/http"

	"littleworld/internal/auth"
	"littleworld/internal/reading"
)

// GET /api/books/{slug}/read — free books work anonymously; paid ones need a purchase.
func (s *Server) handleReadBook(w http.ResponseWriter, r *http.Request) {
	session, err := s.Reading.Open(r.Context(), optionalUserID(r), r.PathValue("slug"))
	switch {
	case errors.Is(err, reading.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
	case errors.Is(err, reading.ErrForbidden):
		writeError(w, http.StatusForbidden, "not_purchased", "Buy this book to read it.")
	case errors.Is(err, reading.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "This book cannot be opened right now.")
	case err != nil:
		s.Log.Error("open book", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not open the book.")
	default:
		// The URL is a bearer token: shared caches must never replay it.
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{
			"url":        session.URL,
			"source":     session.Source,
			"expires_in": session.ExpiresIn,
		})
	}
}

// optionalUserID returns the signed-in user, or 0 for anonymous callers. An invalid
// session is not an error here: the endpoint is public for free books.
func optionalUserID(r *http.Request) int64 {
	user, ok := userFrom(r)
	if !ok {
		return 0
	}
	return user.ID
}

// withOptionalUser attaches the session user when a valid cookie is present.
func (s *Server) withOptionalUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			next(w, r)
			return
		}

		user, err := s.Auth.Authenticate(r.Context(), cookie.Value)
		switch {
		case err == nil:
			// fall through with the user attached
		case errors.Is(err, auth.ErrUnauthenticated):
			// Expired or forged cookie: the caller is anonymous.
			next(w, r)
			return
		default:
			s.Log.Error("optional authenticate", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "Could not verify your session.")
			return
		}

		ctx := context.WithValue(r.Context(), contextKey{}, auth.User{ID: user.ID, Email: user.Email, Role: user.Role})
		next(w, r.WithContext(ctx))
	}
}

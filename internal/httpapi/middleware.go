package httpapi

import (
	"context"
	"errors"
	"net/http"

	"littleworld/internal/auth"
)

const sessionCookie = "lw_session"

type contextKey struct{}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}

		user, err := s.Auth.Authenticate(r.Context(), cookie.Value)
		switch {
		case err == nil:
			next(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, user)))
		case errors.Is(err, auth.ErrUnauthenticated):
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Your session has expired.")
		default:
			// A database outage is not an auth failure; do not log the user out fake-ly.
			s.Log.Error("authenticate", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "Could not verify your session.")
		}
	}
}

func (s *Server) requireRole(role string, next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		user, ok := userFrom(r)
		if !ok || user.Role != role {
			writeError(w, http.StatusForbidden, "forbidden", "This area is not available for your account.")
			return
		}
		next(w, r)
	})
}

func userFrom(r *http.Request) (auth.User, bool) {
	user, ok := r.Context().Value(contextKey{}).(auth.User)
	return user, ok
}

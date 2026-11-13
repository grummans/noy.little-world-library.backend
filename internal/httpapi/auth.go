package httpapi

import (
	"errors"
	"net/http"
	"time"

	"littleworld/internal/auth"
)

type userResponse struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

func newUserResponse(u auth.User) userResponse {
	return userResponse{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, Role: u.Role}
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
		Password    string `json:"password"`
		Role        string `json:"role"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Malformed request body.")
		return
	}

	user, err := s.Auth.Register(r.Context(), in.DisplayName, in.Email, in.Password, in.Role)
	switch {
	case errors.Is(err, auth.ErrValidation):
		writeError(w, http.StatusBadRequest, "validation", err.Error())
	case errors.Is(err, auth.ErrEmailTaken):
		writeError(w, http.StatusConflict, "email_taken", "That email is already registered.")
	case err != nil:
		s.Log.Error("register", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not create the account.")
	default:
		// Registering signs the person in: an account without a session would leave the
		// UI believing it is signed in while every protected call returns 401.
		sess, err := s.Auth.StartSession(r.Context(), user.ID)
		if err != nil {
			s.Log.Error("register session", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "Account created but the session failed.")
			return
		}
		s.setSessionCookie(w, sess)
		writeJSON(w, http.StatusCreated, newUserResponse(user))
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Malformed request body.")
		return
	}

	sess, user, err := s.Auth.Login(r.Context(), in.Email, in.Password, in.Role)
	switch {
	case errors.Is(err, auth.ErrValidation):
		writeError(w, http.StatusBadRequest, "validation", err.Error())
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect.")
	case err != nil:
		s.Log.Error("login", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not sign you in.")
	default:
		s.setSessionCookie(w, sess)
		writeJSON(w, http.StatusOK, newUserResponse(user))
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if err := s.Auth.Logout(r.Context(), cookie.Value); err != nil {
			// Keep the cookie: a stolen token stays valid otherwise, and the user
			// can retry the logout.
			s.Log.Error("logout", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "Could not sign you out.")
			return
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := userFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
		return
	}
	writeJSON(w, http.StatusOK, newUserResponse(user))
}

// setSessionCookie is shared by login and register so both create an identical session.
func (s *Server) setSessionCookie(w http.ResponseWriter, sess auth.Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sess.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  sess.ExpiresAt,
		MaxAge:   int(time.Until(sess.ExpiresAt).Seconds()),
	})
}

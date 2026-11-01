package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"littleworld/internal/auth"
	"littleworld/internal/dbtest"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	pool := dbtest.Migrated(t)
	return New(Deps{
		DB:            pool,
		Auth:          auth.New(pool),
		Log:           slog.New(slog.DiscardHandler),
		SecureCookies: false,
	})
}

func do(t *testing.T, h http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if len(cookies) > 0 {
		req.AddCookie(cookies[0])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionCookieFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("response has no %s cookie", sessionCookie)
	return nil
}

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }
func TestAuthFlow(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Router()

	register := do(t, h, http.MethodPost, "/api/auth/register",
		`{"display_name":"Bé Test","email":"parent@example.com","password":"password123","role":"parent"}`)
	if register.Code != http.StatusCreated {
		t.Fatalf("register status = %d, want 201: %s", register.Code, register.Body)
	}

	duplicate := do(t, h, http.MethodPost, "/api/auth/register",
		`{"display_name":"Bé Test","email":"PARENT@example.com","password":"password123","role":"parent"}`)
	if duplicate.Code != http.StatusConflict {
		t.Errorf("duplicate register status = %d, want 409", duplicate.Code)
	}

	login := do(t, h, http.MethodPost, "/api/auth/login",
		`{"email":"parent@example.com","password":"password123"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body)
	}
	cookie := sessionCookieFrom(t, login)
	if !cookie.HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}

	me := do(t, h, http.MethodGet, "/api/auth/me", "", cookie)
	if me.Code != http.StatusOK {
		t.Fatalf("me status = %d, want 200", me.Code)
	}
	var user userResponse
	if err := json.Unmarshal(me.Body.Bytes(), &user); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	if user.Email != "parent@example.com" || user.Role != auth.RoleParent {
		t.Errorf("me = %+v", user)
	}

	if anon := do(t, h, http.MethodGet, "/api/auth/me", ""); anon.Code != http.StatusUnauthorized {
		t.Errorf("me without cookie status = %d, want 401", anon.Code)
	}

	logout := do(t, h, http.MethodPost, "/api/auth/logout", "", cookie)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", logout.Code)
	}
	if after := do(t, h, http.MethodGet, "/api/auth/me", "", cookie); after.Code != http.StatusUnauthorized {
		t.Errorf("me after logout status = %d, want 401", after.Code)
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Router()

	_ = do(t, h, http.MethodPost, "/api/auth/register",
		`{"display_name":"Bé Test","email":"parent@example.com","password":"password123","role":"parent"}`)

	rec := do(t, h, http.MethodPost, "/api/auth/login",
		`{"email":"parent@example.com","password":"nope-nope-nope"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("failed login set a cookie")
	}
}

func TestRoleGuard(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Router()

	_ = do(t, h, http.MethodPost, "/api/auth/register",
		`{"display_name":"Bé Test","email":"teacher@example.com","password":"password123","role":"teacher"}`)
	_ = do(t, h, http.MethodPost, "/api/auth/register",
		`{"display_name":"Bé Test","email":"parent@example.com","password":"password123","role":"parent"}`)

	parentOnly := srv.requireRole(auth.RoleParent, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	parentCookie := sessionCookieFrom(t, do(t, h, http.MethodPost, "/api/auth/login",
		`{"email":"parent@example.com","password":"password123"}`))
	teacherCookie := sessionCookieFrom(t, do(t, h, http.MethodPost, "/api/auth/login",
		`{"email":"teacher@example.com","password":"password123"}`))

	if rec := do(t, parentOnly, http.MethodGet, "/api/parent/library", "", parentCookie); rec.Code != http.StatusOK {
		t.Errorf("parent status = %d, want 200", rec.Code)
	}
	if rec := do(t, parentOnly, http.MethodGet, "/api/parent/library", "", teacherCookie); rec.Code != http.StatusForbidden {
		t.Errorf("teacher status = %d, want 403", rec.Code)
	}
	if rec := do(t, parentOnly, http.MethodGet, "/api/parent/library", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous status = %d, want 401", rec.Code)
	}
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t)
	rec := do(t, srv.Router(), http.MethodGet, "/healthz", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Body.String(), "{\"status\":\"ok\"}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestRouterRejectsUnknownPath(t *testing.T) {
	srv := newTestServer(t)
	if rec := do(t, srv.Router(), http.MethodGet, "/nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// Registering must hand out a session: the UI treats a created account as signed in.
func TestRegisterCreatesSession(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Router()

	rec := do(t, h, http.MethodPost, "/api/auth/register",
		`{"display_name":"Bé Test","email":"fresh@example.com","password":"password123","role":"parent"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register status = %d: %s", rec.Code, rec.Body)
	}

	cookie := sessionCookieFrom(t, rec)
	if !cookie.HttpOnly {
		t.Error("register cookie is not HttpOnly")
	}

	me := do(t, h, http.MethodGet, "/api/auth/me", "", cookie)
	if me.Code != http.StatusOK {
		t.Fatalf("me after register status = %d: %s", me.Code, me.Body)
	}
}

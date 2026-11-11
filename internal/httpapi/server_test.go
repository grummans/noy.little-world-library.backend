package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"littleworld/internal/activities"
	"littleworld/internal/auth"
	"littleworld/internal/dbtest"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	pool := dbtest.Migrated(t)
	return New(Deps{
		DB:            pool,
		Auth:          auth.New(pool),
		Activities:    activities.New(pool, nil),
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
		`{"email":"parent@example.com","password":"password123","role":"parent"}`)
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
		`{"email":"parent@example.com","password":"nope-nope-nope","role":"parent"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("failed login set a cookie")
	}
}

func TestLoginRejectsWrongRoleTab(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Router()

	// The assertion below only means something if the account exists: an unknown email
	// would also answer 401 with the same code.
	registered := do(t, h, http.MethodPost, "/api/auth/register",
		`{"display_name":"Bé Test","email":"parent@example.com","password":"password123","role":"parent"}`)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status = %d, want 201: %s", registered.Code, registered.Body)
	}

	// Correct password, wrong sign-in tab: it must be indistinguishable from a wrong
	// password, otherwise the response tells an attacker that the account exists and
	// which site it belongs to.
	wrongTab := do(t, h, http.MethodPost, "/api/auth/login",
		`{"email":"parent@example.com","password":"password123","role":"teacher"}`)
	wrongPassword := do(t, h, http.MethodPost, "/api/auth/login",
		`{"email":"parent@example.com","password":"not-the-password","role":"parent"}`)

	// Compare the two replies field by field: a difference in status, body or headers is
	// exactly the signal an attacker would read to learn that the address is registered.
	for name, res := range map[string]*httptest.ResponseRecorder{
		"wrong tab":      wrongTab,
		"wrong password": wrongPassword,
	} {
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401: %s", name, res.Code, res.Body)
		}
		if code := errorCode(t, res.Body.Bytes()); code != "invalid_credentials" {
			t.Errorf("%s: error code = %q, want invalid_credentials", name, code)
		}
		if res.Header().Get("Set-Cookie") != "" || len(res.Result().Cookies()) != 0 {
			t.Errorf("%s: failed login set a cookie", name)
		}
		if got := res.Header().Get("WWW-Authenticate"); got != "" {
			t.Errorf("%s: WWW-Authenticate = %q, want empty", name, got)
		}
	}
	if wrongTab.Body.String() != wrongPassword.Body.String() {
		t.Errorf("wrong tab answers %q, wrong password answers %q", wrongTab.Body, wrongPassword.Body)
	}
	// A missing role is a client bug, not a credential failure.
	missing := do(t, h, http.MethodPost, "/api/auth/login",
		`{"email":"parent@example.com","password":"password123"}`)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing role: status = %d, want 400", missing.Code)
	}
	if code := errorCode(t, missing.Body.Bytes()); code != "validation" {
		t.Errorf("missing role: error code = %q, want validation", code)
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
		`{"email":"parent@example.com","password":"password123","role":"parent"}`))
	teacherCookie := sessionCookieFrom(t, do(t, h, http.MethodPost, "/api/auth/login",
		`{"email":"teacher@example.com","password":"password123","role":"teacher"}`))

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

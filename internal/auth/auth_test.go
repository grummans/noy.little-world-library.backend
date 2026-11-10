package auth_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"littleworld/internal/auth"
	"littleworld/internal/dbtest"
)

func newService(t *testing.T) (*sql.DB, *auth.Service) {
	t.Helper()
	pool := dbtest.Migrated(t)
	return pool, auth.New(pool)
}
func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func TestRegisterValidatesInput(t *testing.T) {
	_, svc := newService(t)
	ctx := t.Context()

	for _, tc := range []struct {
		name     string
		email    string
		password string
		role     string
	}{
		{"bad email", "not-an-email", "password123", auth.RoleParent},
		{"short password", "a@example.com", "short", auth.RoleParent},
		{"long password", "a@example.com", string(make([]byte, 73)), auth.RoleParent},
		{"unknown role", "a@example.com", "password123", "admin"},
	} {
		if _, err := svc.Register(ctx, "Bé Test", tc.email, tc.password, tc.role); !errors.Is(err, auth.ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", tc.name, err)
		}
	}
}

func TestRegisterNormalizesEmailAndRejectsDuplicates(t *testing.T) {
	_, svc := newService(t)
	ctx := t.Context()

	user, err := svc.Register(ctx, "Bé Test", "  Parent@Example.com ", "password123", auth.RoleParent)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if user.Email != "parent@example.com" {
		t.Errorf("email = %q, want lowercased and trimmed", user.Email)
	}
	if user.Role != auth.RoleParent {
		t.Errorf("role = %q", user.Role)
	}

	if _, err := svc.Register(ctx, "Bé Test", "PARENT@example.com", "password123", auth.RoleParent); !errors.Is(err, auth.ErrEmailTaken) {
		t.Errorf("duplicate email: err = %v, want ErrEmailTaken", err)
	}
}

func TestLoginCreatesUsableSession(t *testing.T) {
	_, svc := newService(t)
	ctx := t.Context()

	registered, err := svc.Register(ctx, "Bé Test", "teacher@example.com", "password123", auth.RoleTeacher)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	sess, user, err := svc.Login(ctx, "Teacher@Example.com", "password123", auth.RoleTeacher)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if user.ID != registered.ID {
		t.Errorf("login returned user %d, want %d", user.ID, registered.ID)
	}
	if sess.Token == "" {
		t.Fatal("login returned an empty token")
	}

	authenticated, err := svc.Authenticate(ctx, sess.Token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if authenticated.ID != registered.ID || authenticated.Role != auth.RoleTeacher {
		t.Errorf("authenticated user = %+v", authenticated)
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	_, svc := newService(t)
	ctx := t.Context()

	if _, err := svc.Register(ctx, "Bé Test", "parent@example.com", "password123", auth.RoleParent); err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, _, err := svc.Login(ctx, "parent@example.com", "wrong-password", auth.RoleParent); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("wrong password: err = %v, want ErrInvalidCredentials", err)
	}
	if _, _, err := svc.Login(ctx, "nobody@example.com", "password123", auth.RoleParent); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("unknown email: err = %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginRejectsWrongRole(t *testing.T) {
	pool, svc := newService(t)
	ctx := t.Context()

	if _, err := svc.Register(ctx, "Bé Test", "parent@example.com", "password123", auth.RoleParent); err != nil {
		t.Fatalf("register: %v", err)
	}

	before := countSessions(t, pool)

	// Right password, wrong tab: the mismatch is reported, but no session may be created.
	if _, _, err := svc.Login(ctx, "parent@example.com", "password123", auth.RoleTeacher); !errors.Is(err, auth.ErrWrongRole) {
		t.Errorf("wrong role: err = %v, want ErrWrongRole", err)
	}
	if after := countSessions(t, pool); after != before {
		t.Errorf("sessions after a wrong-role attempt = %d, want %d", after, before)
	}

	// A bad password stays indistinguishable from an unknown account, whatever the tab.
	if _, _, err := svc.Login(ctx, "parent@example.com", "wrong-password", auth.RoleTeacher); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("wrong password and role: err = %v, want ErrInvalidCredentials", err)
	}

	if _, _, err := svc.Login(ctx, "parent@example.com", "password123", ""); !errors.Is(err, auth.ErrValidation) {
		t.Errorf("missing role: err = %v, want ErrValidation", err)
	}
	if _, _, err := svc.Login(ctx, "parent@example.com", "password123", "admin"); !errors.Is(err, auth.ErrValidation) {
		t.Errorf("unknown role: err = %v, want ErrValidation", err)
	}
}

func countSessions(t *testing.T, pool *sql.DB) int {
	t.Helper()
	var n int
	if err := pool.QueryRowContext(t.Context(), `SELECT count(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	return n
}

func TestAuthenticateRejectsUnknownAndExpiredSessions(t *testing.T) {
	pool, svc := newService(t)
	ctx := t.Context()

	if _, err := svc.Authenticate(ctx, "not-a-real-token"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("unknown token: err = %v, want ErrUnauthenticated", err)
	}
	if _, err := svc.Authenticate(ctx, ""); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("empty token: err = %v, want ErrUnauthenticated", err)
	}

	user, err := svc.Register(ctx, "Bé Test", "parent@example.com", "password123", auth.RoleParent)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	const expiredToken = "expired-token"
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() - interval '1 minute')`,
		user.ID, hashTokenForTest(expiredToken),
	); err != nil {
		t.Fatalf("insert expired session: %v", err)
	}
	if _, err := svc.Authenticate(ctx, expiredToken); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("expired token: err = %v, want ErrUnauthenticated", err)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	_, svc := newService(t)
	ctx := t.Context()

	if _, err := svc.Register(ctx, "Bé Test", "parent@example.com", "password123", auth.RoleParent); err != nil {
		t.Fatalf("register: %v", err)
	}
	sess, _, err := svc.Login(ctx, "parent@example.com", "password123", auth.RoleParent)
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	if err := svc.Logout(ctx, sess.Token); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := svc.Authenticate(ctx, sess.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("token still valid after logout: %v", err)
	}
}

func TestCanManage(t *testing.T) {
	teacher := auth.User{ID: 7, Role: auth.RoleTeacher}
	parent := auth.User{ID: 7, Role: auth.RoleParent}

	if !auth.CanManage(teacher, 7) {
		t.Error("teacher cannot manage own content")
	}
	if auth.CanManage(teacher, 8) {
		t.Error("teacher can manage another teacher's content")
	}
	if auth.CanManage(parent, 7) {
		t.Error("parent can manage content")
	}
}

func hashTokenForTest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func TestRegisterStoresDisplayName(t *testing.T) {
	_, svc := newService(t)
	ctx := t.Context()

	user, err := svc.Register(ctx, "  Nguyễn   Thị Bé  ", "be@example.com", "password123", auth.RoleParent)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if user.DisplayName != "Nguyễn Thị Bé" {
		t.Errorf("display name = %q, want trimmed and collapsed", user.DisplayName)
	}

	// The name survives a login and a session lookup.
	sess, loggedIn, err := svc.Login(ctx, "be@example.com", "password123", auth.RoleParent)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if loggedIn.DisplayName != "Nguyễn Thị Bé" {
		t.Errorf("login display name = %q", loggedIn.DisplayName)
	}
	authenticated, err := svc.Authenticate(ctx, sess.Token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if authenticated.DisplayName != "Nguyễn Thị Bé" {
		t.Errorf("session display name = %q", authenticated.DisplayName)
	}
}

func TestRegisterValidatesDisplayName(t *testing.T) {
	_, svc := newService(t)
	ctx := t.Context()

	cases := map[string]string{
		"empty":    "",
		"spaces":   "   ",
		"control":  "Bé\x00Tên",
		"too long": strings.Repeat("a", 81),
	}
	for name, value := range cases {
		if _, err := svc.Register(ctx, value, name+"@example.com", "password123", auth.RoleParent); !errors.Is(err, auth.ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}
}

// Invisible characters would let a name render as blank or reversed in the header.
func TestRegisterRejectsInvisibleNameCharacters(t *testing.T) {
	_, svc := newService(t)
	ctx := t.Context()

	for name, value := range map[string]string{
		"zero width space": "Bé\u200bTên",
		"right-to-left":    "Bé\u202eTên",
		"byte order mark":  "\ufeffBé",
	} {
		if _, err := svc.Register(ctx, value, "rtl@example.com", "password123", auth.RoleParent); !errors.Is(err, auth.ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}

	// Normal Vietnamese with combining marks and emoji is still fine.
	if _, err := svc.Register(ctx, "Nguyễn Thị Bé 🌸", "unicode@example.com", "password123", auth.RoleParent); err != nil {
		t.Errorf("valid unicode name rejected: %v", err)
	}
}

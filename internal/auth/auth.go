// Package auth owns registration, password hashing and DB-backed sessions.
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

const (
	RoleParent  = "parent"
	RoleTeacher = "teacher"

	SessionTTL = 24 * time.Hour

	minPasswordLen = 8
	// bcrypt silently truncates anything past 72 bytes.
	maxPasswordLen = 72

	maxDisplayNameLen = 80
)

var (
	ErrValidation         = errors.New("validation failed")
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrUnauthenticated    = errors.New("unauthenticated")
)

// dummyPasswordHash is a valid bcrypt hash of a throwaway string.
var dummyPasswordHash = []byte("$2a$10$PbOR42TF9aXAkrJA9uKgK.qQWjGGwjV2L/8vY/KpgxQ9QWzZvQFbG")

type User struct {
	ID          int64
	Email       string
	DisplayName string
	Role        string
}

type Session struct {
	Token     string
	ExpiresAt time.Time
}

type Service struct {
	db *sql.DB
}

func New(db *sql.DB) *Service {
	return &Service{db: db}
}

// CanManage reports whether user may modify content owned by ownerID.
func CanManage(user User, ownerID int64) bool {
	return user.Role == RoleTeacher && user.ID == ownerID
}

func (s *Service) Register(ctx context.Context, displayName, email, password, role string) (User, error) {
	displayName, err := normalizeDisplayName(displayName)
	if err != nil {
		return User{}, err
	}

	normalizedEmail, err := normalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	if role != RoleParent && role != RoleTeacher {
		return User{}, fmt.Errorf("%w: role must be parent or teacher", ErrValidation)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, fmt.Errorf("hash password: %w", err)
	}

	var u User
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO users (display_name, email, password_hash, role) VALUES ($1, $2, $3, $4)
		 RETURNING id, email, display_name, role`,
		displayName, normalizedEmail, string(hash), role,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("insert user: %w", err)
	}
	return u, nil
}

// Sign-in is scoped to the role the visitor picks: the login screen has a parent tab and a
// teacher tab, and standing in the wrong one is a user mistake, not a login.
func (s *Service) Login(ctx context.Context, email, password, role string) (Session, User, error) {
	if role != RoleParent && role != RoleTeacher {
		return Session{}, User{}, fmt.Errorf("%w: role must be parent or teacher", ErrValidation)
	}
	email = strings.ToLower(strings.TrimSpace(email))

	var (
		u    User
		hash string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, COALESCE(display_name, split_part(email, '@', 1)) AS display_name, role, password_hash FROM users WHERE lower(email) = $1`, email,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		// Compare against a dummy hash so a missing email costs the same as a wrong
		// password; otherwise the response time reveals which emails exist.
		bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(password))
		return Session{}, User{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, User{}, fmt.Errorf("lookup user: %w", err)
	}

	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return Session{}, User{}, ErrInvalidCredentials
	}
	// A login aimed at the other site gets the same answer as a wrong password: revealing
	// that the account exists and which tab it belongs to would confirm an address to
	// anyone guessing, so the role check is deliberately indistinguishable.
	if u.Role != role {
		return Session{}, User{}, ErrInvalidCredentials
	}

	sess, err := s.createSession(ctx, u.ID)
	if err != nil {
		return Session{}, User{}, err
	}
	return sess, u, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (User, error) {
	if token == "" {
		return User{}, ErrUnauthenticated
	}

	var u User
	// shortcut: expired rows are filtered on lookup, not deleted; add a periodic
	// cleanup job once session volume grows.
	err := s.db.QueryRowContext(ctx,
		`SELECT u.id, u.email, COALESCE(u.display_name, split_part(u.email, '@', 1)) AS display_name, u.role
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = $1 AND s.expires_at > now()`,
		hashToken(token),
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUnauthenticated
	}
	if err != nil {
		return User{}, fmt.Errorf("lookup session: %w", err)
	}
	return u, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE token_hash = $1`, hashToken(token),
	); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func normalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > 254 {
		return "", fmt.Errorf("%w: invalid email", ErrValidation)
	}
	return email, nil
}

func validatePassword(password string) error {
	if len(password) < minPasswordLen || len(password) > maxPasswordLen {
		return fmt.Errorf("%w: password must be %d-%d bytes", ErrValidation, minPasswordLen, maxPasswordLen)
	}
	return nil
}

// normalizeDisplayName keeps a human name usable: trimmed, collapsed whitespace, no
// control or invisible formatting characters, and bounded to the same 80 characters the
// database enforces. Zero-width and bidi-override characters are rejected because they
// render as an empty or reversed header and can be used to spoof a display.
func normalizeDisplayName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || len([]rune(name)) > maxDisplayNameLen {
		return "", fmt.Errorf("%w: name must be 1-%d characters", ErrValidation, maxDisplayNameLen)
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", fmt.Errorf("%w: name contains invisible characters", ErrValidation)
		}
	}
	return name, nil
}

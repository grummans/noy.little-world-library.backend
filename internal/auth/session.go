package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
)

// Session tokens are random and only their hash is stored, so a database leak
// does not hand out usable cookies.
func newToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Service) createSession(ctx context.Context, userID int64) (Session, error) {
	token, err := newToken()
	if err != nil {
		return Session{}, err
	}

	sess := Session{Token: token, ExpiresAt: time.Now().Add(SessionTTL)}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, hashToken(token), sess.ExpiresAt,
	); err != nil {
		return Session{}, fmt.Errorf("insert session: %w", err)
	}
	return sess, nil
}

// StartSession opens a session for a user that is already authenticated (or was just
// registered). It avoids re-checking the password just to get a cookie.
func (s *Service) StartSession(ctx context.Context, userID int64) (Session, error) {
	return s.createSession(ctx, userID)
}

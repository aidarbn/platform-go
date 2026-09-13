package adminx

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrNoSession is returned when the token is unknown: it was never issued, or the
	// session has been revoked.
	ErrNoSession = errors.New("session not found")

	// ErrSessionExpired is returned when the session is too old.
	ErrSessionExpired = errors.New("the session has expired")
)

// Session is a signed in browser.
type Session struct {
	ID        string // hash of the token, not the token itself
	UserID    int64
	CreatedAt time.Time
	ExpiresAt time.Time
	IP        string
	UserAgent string
}

// SessionRepo stores the sessions.
type SessionRepo interface {
	Create(ctx context.Context, s Session) error
	ByID(ctx context.Context, id string) (Session, error)
	Delete(ctx context.Context, id string) error
	DeleteByUser(ctx context.Context, userID int64) error
	DeleteExpired(ctx context.Context, now time.Time) (int, error)
}

// NewSessionToken returns the token for the cookie and the id to store. Only the hash
// is stored, so a database dump does not hand out live sessions.
func NewSessionToken() (token, id string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("adminx: session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, SessionID(token), nil
}

// SessionID is the stored id of a token.
func SessionID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

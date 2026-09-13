package adminx

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DefaultSessionTTL is how long a session lives.
const DefaultSessionTTL = 12 * time.Hour

// ErrCodeRequired is returned when the account has two factor authentication and no
// code was given.
var ErrCodeRequired = errors.New("a one time code is required")

// Auth is the sign in of the admin panel: it checks the password and the one time code
// and issues sessions.
type Auth struct {
	users    UserRepo
	sessions SessionRepo
	ttl      time.Duration

	// Now is the clock. Tests replace it; production leaves it alone.
	Now func() time.Time
}

// NewAuth creates the service. A zero ttl means DefaultSessionTTL.
func NewAuth(users UserRepo, sessions SessionRepo, ttl time.Duration) *Auth {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	return &Auth{users: users, sessions: sessions, ttl: ttl, Now: time.Now}
}

func (a *Auth) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Login checks the credentials and returns the token for the cookie.
//
// An unknown address and a wrong password give the same error, so the answer does not
// tell whether an account exists.
func (a *Auth) Login(ctx context.Context, email, password, code, ip, userAgent string) (string, error) {
	user, err := a.users.ByEmail(ctx, NormalizeEmail(email))
	switch {
	case errors.Is(err, ErrNoUser):
		return "", ErrWrongPassword
	case err != nil:
		return "", err
	}
	if user.Disabled {
		return "", ErrUserDisabled
	}
	if err := VerifyPassword(user.PasswordHash, password); err != nil {
		return "", err
	}

	now := a.now()
	if user.TwoFactor() {
		if code == "" {
			return "", ErrCodeRequired
		}
		counter, err := VerifyTOTP(user.TOTPSecret, code, now, user.TOTPCounter)
		if err != nil {
			return "", err
		}
		// The used counter is stored before the session is issued: a code that let
		// someone in must not let anyone in a second time.
		user.TOTPCounter = counter
	}

	user.LastLoginAt = now
	if err := a.users.Update(ctx, user); err != nil {
		return "", err
	}

	token, id, err := NewSessionToken()
	if err != nil {
		return "", err
	}
	session := Session{
		ID:        id,
		UserID:    user.ID,
		CreatedAt: now,
		ExpiresAt: now.Add(a.ttl),
		IP:        ip,
		UserAgent: userAgent,
	}
	if err := a.sessions.Create(ctx, session); err != nil {
		return "", err
	}
	return token, nil
}

// Session returns the signed in user for a token. An expired session is deleted, so a
// stale cookie does not keep a row alive.
func (a *Auth) Session(ctx context.Context, token string) (User, Session, error) {
	if token == "" {
		return User{}, Session{}, ErrNoSession
	}

	session, err := a.sessions.ByID(ctx, SessionID(token))
	if err != nil {
		return User{}, Session{}, err
	}
	if !session.ExpiresAt.After(a.now()) {
		if err := a.sessions.Delete(ctx, session.ID); err != nil {
			return User{}, Session{}, err
		}
		return User{}, Session{}, ErrSessionExpired
	}

	user, err := a.users.ByID(ctx, session.UserID)
	if err != nil {
		return User{}, Session{}, err
	}
	// An account disabled while signed in loses access at once.
	if user.Disabled {
		if err := a.sessions.DeleteByUser(ctx, user.ID); err != nil {
			return User{}, Session{}, err
		}
		return User{}, Session{}, ErrUserDisabled
	}
	return user, session, nil
}

// Logout revokes one session.
func (a *Auth) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	err := a.sessions.Delete(ctx, SessionID(token))
	if errors.Is(err, ErrNoSession) {
		return nil
	}
	return err
}

// CreateUser adds an account and returns it together with the TOTP secret to show
// once. With twoFactor off the secret is empty.
func (a *Auth) CreateUser(ctx context.Context, email, password string, roles []string, twoFactor bool) (User, string, error) {
	email = NormalizeEmail(email)
	if email == "" {
		return User{}, "", errors.New("an empty address")
	}

	hash, err := HashPassword(password)
	if err != nil {
		return User{}, "", err
	}

	var secret string
	if twoFactor {
		if secret, err = NewTOTPSecret(); err != nil {
			return User{}, "", err
		}
	}

	user, err := a.users.Create(ctx, User{
		Email:        email,
		PasswordHash: hash,
		TOTPSecret:   secret,
		Roles:        roles,
		CreatedAt:    a.now(),
	})
	if err != nil {
		return User{}, "", err
	}
	return user, secret, nil
}

// SetPassword changes a password and revokes every session of that user: after a
// password change the old browsers must sign in again.
func (a *Auth) SetPassword(ctx context.Context, id int64, password string) error {
	user, err := a.users.ByID(ctx, id)
	if err != nil {
		return err
	}
	if user.PasswordHash, err = HashPassword(password); err != nil {
		return err
	}
	if err := a.users.Update(ctx, user); err != nil {
		return err
	}
	return a.sessions.DeleteByUser(ctx, id)
}

// Cleanup deletes the sessions that have expired.
func (a *Auth) Cleanup(ctx context.Context) (int, error) {
	n, err := a.sessions.DeleteExpired(ctx, a.now())
	if err != nil {
		return 0, fmt.Errorf("adminx: clean up sessions: %w", err)
	}
	return n, nil
}

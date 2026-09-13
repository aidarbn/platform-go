package adminx

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

// RoleAdmin may do everything, including managing users. Other roles are the project's
// own business: the admin UI checks a page's roles against the ones a user has.
const RoleAdmin = "admin"

// ErrNoUser is returned by a repository when there is no such user.
var ErrNoUser = errors.New("user not found")

// ErrUserDisabled is returned when a disabled account tries to log in.
var ErrUserDisabled = errors.New("the account is disabled")

// User is an admin panel account.
type User struct {
	ID           int64
	Email        string
	PasswordHash string
	TOTPSecret   string // empty when two factor authentication is off
	TOTPCounter  int64  // the last used code, so it cannot be used again
	Roles        []string
	Disabled     bool
	CreatedAt    time.Time
	LastLoginAt  time.Time
}

// Has reports whether the user has a role. An admin has every role.
func (u User) Has(role string) bool {
	return slices.Contains(u.Roles, RoleAdmin) || slices.Contains(u.Roles, role)
}

// HasAny reports whether the user has at least one of the roles. No roles means the
// page is open to any signed in user.
func (u User) HasAny(roles []string) bool {
	if len(roles) == 0 {
		return true
	}
	for _, role := range roles {
		if u.Has(role) {
			return true
		}
	}
	return false
}

// TwoFactor reports whether the account has two factor authentication.
func (u User) TwoFactor() bool { return u.TOTPSecret != "" }

// UserRepo stores the accounts.
type UserRepo interface {
	ByEmail(ctx context.Context, email string) (User, error)
	ByID(ctx context.Context, id int64) (User, error)
	Create(ctx context.Context, u User) (User, error)
	Update(ctx context.Context, u User) error
	List(ctx context.Context) ([]User, error)
	Delete(ctx context.Context, id int64) error
}

// NormalizeEmail is how an address is stored and looked up, so a capital letter in the
// login form does not create a second account.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

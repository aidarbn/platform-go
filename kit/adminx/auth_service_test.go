package adminx_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/adminx"
)

// Hashing a password is deliberately slow, so the tests that do not check hashing
// itself share one hash.
var testHash = sync.OnceValue(func() string {
	hash, err := adminx.HashPassword("hunter2")
	if err != nil {
		panic(err)
	}
	return hash
})

type fixture struct {
	store *adminx.MemoryStore
	auth  *adminx.Auth
	now   time.Time
}

// The fixture is returned by pointer: the clock closure and the test must see the
// same time, otherwise moving the clock forward changes nothing.
func newFixture(t *testing.T, twoFactor bool) (*fixture, adminx.User) {
	t.Helper()

	store := adminx.NewMemoryStore()
	auth := adminx.NewAuth(store.Users(), store.Sessions(), time.Hour)
	f := &fixture{store: store, auth: auth, now: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)}
	auth.Now = func() time.Time { return f.now }

	user := adminx.User{Email: "Admin@Example.com", PasswordHash: testHash(), Roles: []string{adminx.RoleAdmin}}
	if twoFactor {
		secret, err := adminx.NewTOTPSecret()
		if err != nil {
			t.Fatalf("NewTOTPSecret: %v", err)
		}
		user.TOTPSecret = secret
	}

	created, err := store.Create(context.Background(), user)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return f, created
}

func TestLogin(t *testing.T) {
	ctx := context.Background()
	f, user := newFixture(t, false)

	// The address is normalised, so a capital letter in the form still logs in.
	token, err := f.auth.Login(ctx, "  ADMIN@example.com ", "hunter2", "", "10.0.0.1", "curl")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if token == "" {
		t.Fatal("no token")
	}

	got, session, err := f.auth.Session(ctx, token)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if got.ID != user.ID || got.Email != "admin@example.com" {
		t.Errorf("user = %+v", got)
	}
	if session.IP != "10.0.0.1" || session.UserAgent != "curl" {
		t.Errorf("session = %+v", session)
	}
	if !session.ExpiresAt.Equal(f.now.Add(time.Hour)) {
		t.Errorf("expires at %s", session.ExpiresAt)
	}

	// Only the hash of the token is stored, so the stored value cannot be replayed.
	if strings.Contains(session.ID, token) || session.ID == token {
		t.Error("the token itself is stored")
	}
	if _, _, err := f.auth.Session(ctx, session.ID); !errors.Is(err, adminx.ErrNoSession) {
		t.Errorf("the stored id worked as a token: %v", err)
	}
}

// A wrong password and an unknown address answer the same, so the form does not tell
// which accounts exist.
func TestLoginFailures(t *testing.T) {
	ctx := context.Background()
	f, user := newFixture(t, false)

	for name, email := range map[string]string{"wrong password": "admin@example.com", "no such user": "nobody@example.com"} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.auth.Login(ctx, email, "wrong", "", "", ""); !errors.Is(err, adminx.ErrWrongPassword) {
				t.Fatalf("err = %v", err)
			}
		})
	}

	user.Disabled = true
	if err := f.store.Update(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.Login(ctx, "admin@example.com", "hunter2", "", "", ""); !errors.Is(err, adminx.ErrUserDisabled) {
		t.Errorf("a disabled account logged in: %v", err)
	}
}

func TestLoginWithTwoFactor(t *testing.T) {
	ctx := context.Background()
	f, user := newFixture(t, true)

	if _, err := f.auth.Login(ctx, user.Email, "hunter2", "", "", ""); !errors.Is(err, adminx.ErrCodeRequired) {
		t.Fatalf("without a code: %v", err)
	}
	if _, err := f.auth.Login(ctx, user.Email, "hunter2", "000000", "", ""); err == nil {
		t.Fatal("a made up code was accepted")
	}

	code, err := adminx.TOTPCode(user.TOTPSecret, f.now)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	if _, err := f.auth.Login(ctx, user.Email, "hunter2", code, "", ""); err != nil {
		t.Fatalf("Login: %v", err)
	}

	// The used code must not work a second time: an intercepted code is then useless.
	if _, err := f.auth.Login(ctx, user.Email, "hunter2", code, "", ""); !errors.Is(err, adminx.ErrCodeReused) {
		t.Errorf("the code worked twice: %v", err)
	}

	// The next step gives a new code, and that one works.
	f.now = f.now.Add(30 * time.Second)
	next, err := adminx.TOTPCode(user.TOTPSecret, f.now)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	if next == code {
		t.Skip("the codes of two steps coincided")
	}
	if _, err := f.auth.Login(ctx, user.Email, "hunter2", next, "", ""); err != nil {
		t.Errorf("the next code: %v", err)
	}
}

func TestSessionExpires(t *testing.T) {
	ctx := context.Background()
	f, _ := newFixture(t, false)

	token, err := f.auth.Login(ctx, "admin@example.com", "hunter2", "", "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	f.now = f.now.Add(time.Hour + time.Second)
	if _, _, err := f.auth.Session(ctx, token); !errors.Is(err, adminx.ErrSessionExpired) {
		t.Fatalf("err = %v", err)
	}
	// An expired session is removed, so a stale cookie does not keep a row alive.
	if _, _, err := f.auth.Session(ctx, token); !errors.Is(err, adminx.ErrNoSession) {
		t.Errorf("the session stayed: %v", err)
	}
}

func TestSessionUnknownToken(t *testing.T) {
	ctx := context.Background()
	f, _ := newFixture(t, false)

	for name, token := range map[string]string{"empty": "", "made up": "nonsense"} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := f.auth.Session(ctx, token); !errors.Is(err, adminx.ErrNoSession) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// An account disabled while signed in loses access at once, without waiting for the
// session to expire.
func TestDisablingUserEndsSessions(t *testing.T) {
	ctx := context.Background()
	f, user := newFixture(t, false)

	token, err := f.auth.Login(ctx, user.Email, "hunter2", "", "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	user, err = f.store.ByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Disabled = true
	if err := f.store.Update(ctx, user); err != nil {
		t.Fatal(err)
	}

	if _, _, err := f.auth.Session(ctx, token); !errors.Is(err, adminx.ErrUserDisabled) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := f.auth.Session(ctx, token); !errors.Is(err, adminx.ErrNoSession) {
		t.Errorf("the sessions were not revoked: %v", err)
	}
}

func TestLogout(t *testing.T) {
	ctx := context.Background()
	f, _ := newFixture(t, false)

	token, err := f.auth.Login(ctx, "admin@example.com", "hunter2", "", "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := f.auth.Logout(ctx, token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, _, err := f.auth.Session(ctx, token); !errors.Is(err, adminx.ErrNoSession) {
		t.Errorf("the session survived the logout: %v", err)
	}

	// Logging out twice, or with no cookie at all, is not an error.
	if err := f.auth.Logout(ctx, token); err != nil {
		t.Errorf("second Logout: %v", err)
	}
	if err := f.auth.Logout(ctx, ""); err != nil {
		t.Errorf("Logout without a token: %v", err)
	}
}

func TestCreateUser(t *testing.T) {
	ctx := context.Background()
	store := adminx.NewMemoryStore()
	auth := adminx.NewAuth(store.Users(), store.Sessions(), 0)

	user, secret, err := auth.CreateUser(ctx, " Support@Example.com ", "letmein", []string{"support"}, true)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.Email != "support@example.com" {
		t.Errorf("email = %q", user.Email)
	}
	if secret == "" || user.TOTPSecret != secret {
		t.Error("the secret was not returned")
	}
	if user.Has(adminx.RoleAdmin) {
		t.Error("support must not be an admin")
	}
	if !user.Has("support") {
		t.Error("the role was not set")
	}

	code, err := adminx.TOTPCode(secret, time.Now())
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	if _, err := auth.Login(ctx, "support@example.com", "letmein", code, "", ""); err != nil {
		t.Errorf("Login: %v", err)
	}

	if _, _, err := auth.CreateUser(ctx, "support@example.com", "other", nil, false); err == nil {
		t.Error("a duplicate address was accepted")
	}
	if _, _, err := auth.CreateUser(ctx, "  ", "other", nil, false); err == nil {
		t.Error("an empty address was accepted")
	}
}

func TestSetPassword(t *testing.T) {
	ctx := context.Background()
	f, user := newFixture(t, false)

	token, err := f.auth.Login(ctx, user.Email, "hunter2", "", "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := f.auth.SetPassword(ctx, user.ID, "correcthorse"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	// Changing a password signs out the browsers that were signed in.
	if _, _, err := f.auth.Session(ctx, token); !errors.Is(err, adminx.ErrNoSession) {
		t.Errorf("the session survived the password change: %v", err)
	}
	if _, err := f.auth.Login(ctx, user.Email, "hunter2", "", "", ""); !errors.Is(err, adminx.ErrWrongPassword) {
		t.Errorf("the old password still works: %v", err)
	}
	if _, err := f.auth.Login(ctx, user.Email, "correcthorse", "", "", ""); err != nil {
		t.Errorf("the new password: %v", err)
	}
	if err := f.auth.SetPassword(ctx, 999, "whatever"); !errors.Is(err, adminx.ErrNoUser) {
		t.Errorf("err = %v", err)
	}
}

func TestCleanupRemovesExpiredSessions(t *testing.T) {
	ctx := context.Background()
	f, _ := newFixture(t, false)

	if _, err := f.auth.Login(ctx, "admin@example.com", "hunter2", "", "", ""); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if n, err := f.auth.Cleanup(ctx); err != nil || n != 0 {
		t.Fatalf("Cleanup = %d, %v", n, err)
	}

	f.now = f.now.Add(2 * time.Hour)
	if n, err := f.auth.Cleanup(ctx); err != nil || n != 1 {
		t.Fatalf("Cleanup = %d, %v", n, err)
	}
}

func TestRoles(t *testing.T) {
	admin := adminx.User{Roles: []string{adminx.RoleAdmin}}
	support := adminx.User{Roles: []string{"support"}}

	// An admin has every role: otherwise every new page would need a migration.
	if !admin.Has("anything") || !admin.HasAny([]string{"support"}) {
		t.Error("an admin lacks a role")
	}
	if support.Has(adminx.RoleAdmin) {
		t.Error("support became an admin")
	}
	if !support.HasAny([]string{"billing", "support"}) {
		t.Error("HasAny did not find the role")
	}
	if support.HasAny([]string{"billing"}) {
		t.Error("HasAny found a role that is not there")
	}
	// A page without roles is open to any signed in user.
	if !support.HasAny(nil) {
		t.Error("a page without roles was refused")
	}
}

func TestUserDeletionRemovesSessions(t *testing.T) {
	ctx := context.Background()
	f, user := newFixture(t, false)

	token, err := f.auth.Login(ctx, user.Email, "hunter2", "", "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := f.store.Delete(ctx, user.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := f.auth.Session(ctx, token); !errors.Is(err, adminx.ErrNoSession) {
		t.Errorf("err = %v", err)
	}
	if err := f.store.Delete(ctx, user.ID); !errors.Is(err, adminx.ErrNoUser) {
		t.Errorf("second Delete: %v", err)
	}
}

func TestAuditLog(t *testing.T) {
	ctx := context.Background()
	store := adminx.NewMemoryStore()
	log := store.Audit()

	for _, action := range []string{adminx.ActionLogin, adminx.ActionSettingSet, adminx.ActionSettingReset} {
		if err := log.Add(ctx, adminx.AuditEntry{Actor: "admin@example.com", Action: action, Target: "api.ratelimit.rps"}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	// Newest first: that is the order an incident is read in.
	entries, err := log.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 || entries[0].Action != adminx.ActionSettingReset {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].At.IsZero() {
		t.Error("the time was not recorded")
	}

	page, err := log.List(ctx, 2, 0)
	if err != nil || len(page) != 2 {
		t.Fatalf("List = %+v, %v", page, err)
	}
	next, err := log.List(ctx, 2, page[len(page)-1].ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(next) != 1 || next[0].Action != adminx.ActionLogin {
		t.Errorf("the next page = %+v", next)
	}
}

func TestSessionTokensDiffer(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		token, id, err := adminx.NewSessionToken()
		if err != nil {
			t.Fatalf("NewSessionToken: %v", err)
		}
		if seen[token] {
			t.Fatal("a token repeated")
		}
		seen[token] = true
		if id != adminx.SessionID(token) {
			t.Fatal("the id does not match the token")
		}
		if id == token {
			t.Fatal("the id is the token itself")
		}
	}
}

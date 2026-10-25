package codemod_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/codemod"
)

const adminxPath = "github.com/aidarbn/platform-go/kit/adminx"

// adminxBefore uses the API of kit/adminx v0.5.11.
const adminxBefore = `package users

import (
	"context"
	"errors"

	"` + adminxPath + `"
)

// Seed creates the first users.
func Seed(ctx context.Context, a *adminx.Auth, twoFactor bool) error {
	u, secret, err := a.CreateUser(ctx, "a@b.c", "pw", []string{"admin"}, true) // the owner
	if err != nil {
		return err
	}
	_ = u
	_ = secret
	_, _, err = a.CreateUser(ctx, "d@b.c", "pw", nil, false)
	if err != nil {
		return err
	}
	_, _, _ = a.CreateUser(ctx, "e@b.c", "pw", nil, twoFactor)
	return nil
}

var first, _, firstErr = (*adminx.Auth)(nil).CreateUser(context.Background(), "f@b.c", "pw", nil, false)

func login(ctx context.Context, a *adminx.Auth, code string) (string, error) {
	token, err := a.Login(ctx, "a@b.c", "pw", code, "127.0.0.1", "curl")
	if errors.Is(err, adminx.ErrCodeRequired) || errors.Is(err, adminx.ErrInvalidCode) {
		return "", err
	}
	return token, err
}

func quiet(ctx context.Context, a *adminx.Auth) {
	_, _ = a.Login(ctx, "a@b.c", "pw", "", "127.0.0.1", "curl")
}
`

// adminxField uses the auth of the package from a file that does not import kit.
const adminxField = `package users

import "context"

type server struct{ auth authLike }

type authLike interface{}

func (s *server) passthrough(ctx context.Context, u struct{ TwoFactor bool }) {
	_ = u.TwoFactor
	s.auth.(interface{ CreateUser(...any) }).CreateUser(ctx, "a", "b", nil, true)
}
`

const adminxTOTP = `package users

import (
	"time"

	ax "` + adminxPath + `"
)

func secret() string {
	s, _ := ax.NewTOTPSecret()
	code, _ := ax.TOTPCode(s, time.Now())
	_ = ax.User{Email: "a@b.c", TwoFactor: true, TOTPSecret: s}
	_ = ax.User{}
	return code
}
`

func notes(res codemod.Result) []string {
	var out []string
	for _, n := range res.Notes {
		out = append(out, n.String())
	}
	return out
}

func hasNote(t *testing.T, res codemod.Result, path string, line int, part string) {
	t.Helper()
	for _, n := range res.Notes {
		if n.Path == path && n.Line == line && strings.Contains(n.Message, part) && n.Codemod == "adminx.no-totp" {
			return
		}
	}
	t.Errorf("no note %s:%d with %q in\n%s", path, line, part, strings.Join(notes(res), "\n"))
}

func TestAdminxNoTOTP(t *testing.T) {
	dir := project(t, map[string]string{
		"internal/users/users.go":  adminxBefore,
		"internal/users/server.go": adminxField,
		"internal/other/other.go":  "package other\n\nfunc f(a interface{ Login(...any) }) { a.Login(1, 2, 3, 4, 5, 6) }\n",
	})
	mods := []codemod.Codemod{codemod.AdminxNoTOTP()}

	res, err := codemod.Run(dir, mods, mods)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Changes) != 2 {
		t.Fatalf("changes = %+v", res.Changes)
	}
	byPath := map[string]string{}
	for _, c := range res.Changes {
		byPath[c.Path] = string(c.Content)
	}

	got := byPath["internal/users/users.go"]
	for _, want := range []string{
		`u, err := a.CreateUser(ctx, "a@b.c", "pw", []string{"admin"}) // the owner`,
		`_, err = a.CreateUser(ctx, "d@b.c", "pw", nil)`,
		`_, _ = a.CreateUser(ctx, "e@b.c", "pw", nil)`,
		`var first, firstErr = (*adminx.Auth)(nil).CreateUser(context.Background(), "f@b.c", "pw", nil)`,
		`token, err := a.Login(ctx, "a@b.c", "pw", "127.0.0.1", "curl")`,
		`_, _ = a.Login(ctx, "a@b.c", "pw", "127.0.0.1", "curl")`,
		"// Seed creates the first users.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("users.go lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(byPath["internal/users/server.go"], `CreateUser(ctx, "a", "b", nil)`) {
		t.Errorf("a call in a file without the import was not rewritten:\n%s", byPath["internal/users/server.go"])
	}

	const users = "internal/users/users.go"
	hasNote(t, res, users, 12, "secret is gone")
	hasNote(t, res, users, 12, "the argument true is dropped")
	hasNote(t, res, users, 22, "the argument twoFactor is dropped")
	hasNote(t, res, users, 29, "the argument code is dropped")
	hasNote(t, res, users, 30, "drop the handling of adminx.ErrCodeRequired")
	hasNote(t, res, users, 30, "drop the handling of adminx.ErrInvalidCode")
	hasNote(t, res, "internal/users/server.go", 10, "adminx.User has no TwoFactor")
	hasNote(t, res, "internal/users/server.go", 11, "returns two results")
	for _, n := range res.Notes {
		if n.Path == users && (n.Line == 18 || n.Line == 26 || n.Line == 37) {
			t.Errorf("a call with false or an empty code needs nothing by hand: %s", n)
		}
		if n.Path == "internal/other/other.go" {
			t.Errorf("a package without kit/adminx got a note: %s", n)
		}
	}

	// Idempotent: the rewritten project has nothing left to rewrite; the checks still see
	// the removed names, the open manual steps.
	for _, c := range res.Changes {
		if err := os.WriteFile(filepath.Join(dir, c.Path), c.Content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	again, err := codemod.Run(dir, mods, mods)
	if err != nil || len(again.Changes) != 0 {
		t.Fatalf("second run: %+v, %v", again.Changes, err)
	}
	checks, err := codemod.Run(dir, nil, mods)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks.Notes) != 2 || !strings.Contains(checks.Notes[0].Message, "ErrCodeRequired") {
		t.Errorf("checks after the rewrite:\n%s", strings.Join(notes(checks), "\n"))
	}
}

func TestAdminxChecksRemovedNames(t *testing.T) {
	dir := project(t, map[string]string{"users/totp.go": adminxTOTP})
	mods := []codemod.Codemod{codemod.AdminxNoTOTP()}

	res, err := codemod.Run(dir, nil, mods)
	if err != nil {
		t.Fatal(err)
	}
	hasNote(t, res, "users/totp.go", 10, "adminx.NewTOTPSecret")
	hasNote(t, res, "users/totp.go", 11, "adminx.TOTPCode")
	hasNote(t, res, "users/totp.go", 12, "has no TwoFactor")
	hasNote(t, res, "users/totp.go", 12, "has no TOTPSecret")
	if len(res.Notes) != 4 || len(res.Changes) != 0 {
		t.Errorf("notes:\n%s", strings.Join(notes(res), "\n"))
	}
}

func TestAdminxDefineWithNothingNew(t *testing.T) {
	dir := project(t, map[string]string{"a.go": `package a

import "` + adminxPath + `"

func f(a *adminx.Auth) {
	_, s, _ := a.CreateUser(nil, "", "", nil, false)
	_ = s
}
`})
	mods := []codemod.Codemod{codemod.AdminxNoTOTP()}
	res, err := codemod.Run(dir, mods, nil)
	if err != nil || len(res.Changes) != 1 {
		t.Fatalf("%+v, %v", res, err)
	}
	if got := string(res.Changes[0].Content); !strings.Contains(got, `_, _ = a.CreateUser(nil, "", "", nil)`) {
		t.Errorf("result:\n%s", got)
	}
}

func TestAdminxRedeclarationAndOtherPackages(t *testing.T) {
	dir := project(t, map[string]string{"a.go": `package a

import (
	"context"

	"` + adminxPath + `"
	"example.com/sso"
)

func f(ctx context.Context, a *adminx.Auth) error {
	u, err := a.CreateUser(ctx, "", "", nil)
	u, s, err := a.CreateUser(ctx, "", "", nil, false)
	_, _ = u, s
	_, _ = sso.Login(ctx, "a", "b", "c", "d", "e")
	return err
}
`})
	mods := []codemod.Codemod{codemod.AdminxNoTOTP()}
	res, err := codemod.Run(dir, mods, nil)
	if err != nil || len(res.Changes) != 1 {
		t.Fatalf("%+v, %v", res, err)
	}
	got := string(res.Changes[0].Content)
	for _, want := range []string{
		`u, err = a.CreateUser(ctx, "", "", nil)`,
		`_, _ = sso.Login(ctx, "a", "b", "c", "d", "e")`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("result lacks %q:\n%s", want, got)
		}
	}
}

func TestAdminxCoversItsChanges(t *testing.T) {
	c := codemod.AdminxNoTOTP()
	for _, want := range []string{"kit/adminx.(*Auth).CreateUser", "kit/adminx.(*Auth).Login", "kit/adminx.VerifyTOTP", "kit/adminx.User.TOTPCounter"} {
		if !slices.Contains(c.Covers, want) {
			t.Errorf("covers lack %s: %v", want, c.Covers)
		}
	}
	if len(c.Covers) != 12 || c.Version != "v0.6.0" {
		t.Errorf("covers = %v, version %s", c.Covers, c.Version)
	}
}

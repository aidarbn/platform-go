package apicover_test

import (
	"os"
	"slices"
	"testing"

	"github.com/aidarbn/platform-go/internal/apicover"
	"github.com/aidarbn/platform-go/internal/codemod"
)

const report = `Incompatible changes:
- github.com/aidarbn/platform-go/kit/adminx.(*Auth).Login: changed from func(string) to func()
- github.com/aidarbn/platform-go/kit/adminx.TOTPCode: removed
- github.com/aidarbn/platform-go/kit/riverx.Old: removed
- github.com/aidarbn/platform-go/kit/riverx.Gone: removed
Compatible changes:
- github.com/aidarbn/platform-go/kit/pgdb.MigrateSchema: added
`

const docs = "# Upgrades\n\n```\n### v0.7.0\n`kit/riverx.Gone`\n```\n\n## Manual steps\n\n### v0.7.0\n\n- `kit/riverx.Gone` is removed\n\n#### details\n\n- `kit/riverx.Old` too\n\n### v0.5.0\n\n- `kit/adminx.TOTPCode`\n\n## Version policy\n\n`kit/adminx.TOTPCode`\n"

func TestIncompatible(t *testing.T) {
	got := apicover.Incompatible(report, codemod.Module)
	want := []string{"kit/adminx.(*Auth).Login", "kit/adminx.TOTPCode", "kit/riverx.Old", "kit/riverx.Gone"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q", got)
	}
	if got := apicover.Incompatible("Compatible changes:\n- x: added\n", codemod.Module); len(got) != 0 {
		t.Errorf("compatible changes counted: %q", got)
	}
}

func TestManual(t *testing.T) {
	got := apicover.Manual(docs)
	if !slices.Equal(got["v0.7.0"], []string{"kit/riverx.Gone", "kit/riverx.Old"}) || !slices.Equal(got["v0.5.0"], []string{"kit/adminx.TOTPCode"}) || len(got) != 2 {
		t.Errorf("manual = %q", got)
	}
}

func TestUncovered(t *testing.T) {
	changes := apicover.Incompatible(report, codemod.Module)
	manual := apicover.Manual(docs)
	mods := []codemod.Codemod{
		{Version: "v0.7.0", Covers: []string{"kit/adminx.(*Auth).Login"}},
		{Version: "v0.9.0", Covers: []string{"kit/adminx.TOTPCode"}},
	}
	for _, tc := range []struct {
		base, release string
		want          []string
	}{
		{"v0.6.0", "v0.7.0", []string{"kit/adminx.TOTPCode"}},
		{"v0.6.0", "", nil},
		{"v0.7.0", "", []string{"kit/adminx.(*Auth).Login", "kit/riverx.Gone", "kit/riverx.Old"}},
		{"v0.4.0", "v0.6.0", []string{"kit/adminx.(*Auth).Login", "kit/riverx.Gone", "kit/riverx.Old"}},
	} {
		if got := apicover.Uncovered(changes, mods, manual, tc.base, tc.release); !slices.Equal(got, tc.want) {
			t.Errorf("Uncovered(%s, %s) = %q, want %q", tc.base, tc.release, got, tc.want)
		}
	}
}

// The incompatible changes of v0.6.0 are what the codemod of v0.6.0 covers.
func TestV060IsCovered(t *testing.T) {
	changes := []string{
		"kit/adminx.(*Auth).CreateUser", "kit/adminx.(*Auth).Login", "kit/adminx.ErrCodeRequired", "kit/adminx.ErrCodeReused",
		"kit/adminx.ErrInvalidCode", "kit/adminx.NewTOTPSecret", "kit/adminx.TOTPCode", "kit/adminx.TOTPURI",
		"kit/adminx.User.TOTPCounter", "kit/adminx.User.TOTPSecret", "kit/adminx.User.TwoFactor", "kit/adminx.VerifyTOTP",
	}
	if got := apicover.Uncovered(changes, codemod.All(), nil, "v0.5.11", "v0.6.0"); len(got) != 0 {
		t.Errorf("uncovered by codemods: %q", got)
	}
	docs, err := os.ReadFile("../../docs/upgrades.md")
	if err != nil {
		t.Fatal(err)
	}
	if got := apicover.Uncovered(changes, nil, apicover.Manual(string(docs)), "v0.5.11", "v0.6.0"); len(got) != 0 {
		t.Errorf("uncovered by docs/upgrades.md: %q", got)
	}
}

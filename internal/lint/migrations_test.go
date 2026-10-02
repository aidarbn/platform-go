package lint_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/lint"
)

// fakeGit answers the git calls of the migrations check and records the squawk runs.
type fakeGit struct {
	originExists bool
	added        string
	untracked    string
	refs         []string
	squawkRuns   []squawkRun
	squawkFail   map[string]string // migration → squawk output that fails it
}

type squawkRun struct {
	args []string
	sql  string
}

func (f *fakeGit) exec(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	if name == "squawk" {
		rel := args[len(args)-1]
		sql, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return nil, err
		}
		f.squawkRuns = append(f.squawkRuns, squawkRun{args: args, sql: string(sql)})
		if out, ok := f.squawkFail[rel]; ok {
			return []byte(out), errors.New("exit status 1")
		}
		return nil, nil
	}
	switch args[0] {
	case "rev-parse":
		if f.originExists {
			return nil, nil
		}
		return nil, errors.New("exit status 1")
	case "diff":
		f.refs = append(f.refs, args[3])
		return []byte(f.added), nil
	case "ls-files":
		return []byte(f.untracked), nil
	}
	return nil, errors.New("unexpected command " + name)
}

func squawkStub(context.Context) (string, error) { return "squawk", nil }

func TestMigrationsLintsOnlyNewFilesAndOnlyUp(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "db/migrations/001_old.sql", "-- +goose Up\nDROP TABLE x;\n")
	write(t, dir, "db/migrations/002_new.sql", "-- +goose Up\nSET lock_timeout = '5s';\nALTER TABLE a ADD COLUMN b int;\n\n-- +goose Down\nALTER TABLE a DROP COLUMN b;\n")
	write(t, dir, "db/migrations/003_draft.sql", "-- +goose Up\nCREATE TABLE c (id int);\n")
	git := &fakeGit{originExists: true, added: "db/migrations/002_new.sql\n", untracked: "db/migrations/003_draft.sql\n"}

	m := lint.Migrations{Against: "dev", Exec: git.exec, Squawk: squawkStub}
	if err := m.Run(context.Background(), dir); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !slices.Equal(git.refs, []string{"origin/dev"}) {
		t.Errorf("compared against %v, want the remote branch", git.refs)
	}
	if len(git.squawkRuns) != 2 {
		t.Fatalf("squawk ran %d times, want the two new files", len(git.squawkRuns))
	}
	first := git.squawkRuns[0]
	if strings.Contains(first.sql, "DROP COLUMN") || !strings.Contains(first.sql, "ADD COLUMN") {
		t.Errorf("squawk must see Up only:\n%s", first.sql)
	}
	if !slices.Contains(first.args, "--assume-in-transaction") {
		t.Errorf("goose runs a migration in a transaction: %v", first.args)
	}
	if !strings.Contains(strings.Join(first.args, " "), "require-statement-timeout") {
		t.Errorf("statement_timeout is excluded: %v", first.args)
	}
}

func TestMigrationsWithoutBaseLintsUncommitted(t *testing.T) {
	git := &fakeGit{}
	m := lint.Migrations{Exec: git.exec, Squawk: squawkStub}
	if err := m.Run(context.Background(), t.TempDir()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !slices.Equal(git.refs, []string{"HEAD"}) {
		t.Errorf("compared against %v, want HEAD", git.refs)
	}
}

func TestMigrationsReportsSquawkFindings(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "db/migrations/002_drop.sql", "-- +goose Up\nALTER TABLE a DROP COLUMN b;\n")
	git := &fakeGit{
		added:      "db/migrations/002_drop.sql\n",
		squawkFail: map[string]string{"db/migrations/002_drop.sql": "db/migrations/002_drop.sql:2:0: warning: ban-drop-column"},
	}
	err := lint.Migrations{Exec: git.exec, Squawk: squawkStub}.Run(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "002_drop.sql:2:0: warning: ban-drop-column") || !strings.Contains(err.Error(), "squawk-ignore") {
		t.Fatalf("err = %v", err)
	}
}

func TestMigrationsOwnRules(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string // part of the finding; empty when the file passes
		inTx bool
	}{
		{"index concurrently in a transaction", "-- +goose Up\nCREATE INDEX CONCURRENTLY i ON a (b);\n", "add -- +goose NO TRANSACTION", true},
		{"index concurrently without a transaction", "-- +goose NO TRANSACTION\n-- +goose Up\nCREATE INDEX CONCURRENTLY IF NOT EXISTS i ON a (b);\n", "", false},
		{"function", "-- +goose Up\nCREATE OR REPLACE FUNCTION f() RETURNS int AS $$ SELECT 1 $$ LANGUAGE sql;\n", "functions, procedures and triggers", true},
		{"constraint trigger", "-- +goose Up\ncreate constraint trigger t after insert on a for each row execute function f();\n", "functions, procedures and triggers", true},
		{"mentioned in a comment", "-- +goose Up\n-- CREATE FUNCTION is not allowed, CONCURRENTLY neither\nSELECT 1;\n", "", true},
		{"dropped in Down", "-- +goose Up\nSELECT 1;\n-- +goose Down\nDROP FUNCTION IF EXISTS f;\nCREATE FUNCTION f() RETURNS int AS $$ SELECT 1 $$ LANGUAGE sql;\n", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "db/migrations/002_x.sql", tt.sql)
			git := &fakeGit{added: "db/migrations/002_x.sql\n"}
			err := lint.Migrations{Exec: git.exec, Squawk: squawkStub}.Run(context.Background(), dir)
			if tt.want == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if got := slices.Contains(git.squawkRuns[0].args, "--assume-in-transaction"); got != tt.inTx {
				t.Errorf("--assume-in-transaction = %v, want %v", got, tt.inTx)
			}
		})
	}
}

func TestMigrationsPassesProjectConfig(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".squawk.toml", "excluded_rules = [\"ban-drop-column\"]\n")
	write(t, dir, "db/migrations/002_x.sql", "-- +goose Up\nSELECT 1;\n")
	git := &fakeGit{added: "db/migrations/002_x.sql\n"}
	if err := (lint.Migrations{Exec: git.exec, Squawk: squawkStub}).Run(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(git.squawkRuns[0].args, " "), "--config="+filepath.Join(dir, ".squawk.toml")) {
		t.Errorf("args = %v", git.squawkRuns[0].args)
	}
}

func TestChecksLintMigrationsWithPostgres(t *testing.T) {
	has := func(modules ...string) bool {
		for _, c := range lint.Checks(lint.Options{Modules: modules}, nil, nil) {
			if c.Name == "migrations" {
				return true
			}
		}
		return false
	}
	if !has("postgres") || has("api") {
		t.Error("the migrations check belongs to the postgres module")
	}
}

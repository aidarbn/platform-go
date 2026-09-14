package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/gen"
)

// flagSets reads the flag sets of the commands from the source: command name to flags.
func flagSets(t *testing.T) map[string][]string {
	t.Helper()
	files, _ := filepath.Glob("*.go")
	out := map[string][]string{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			var command string
			var flags []string
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				value, _ := strconv.Unquote(lit.Value)
				switch {
				case sel.Sel.Name == "NewFlagSet":
					command, _, _ = strings.Cut(value, " ")
				case slices.Contains([]string{"String", "Bool", "Int", "Duration"}, sel.Sel.Name):
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "fs" {
						flags = append(flags, value)
					}
				}
				return true
			})
			if command != "" {
				out[command] = append(out[command], flags...)
			}
		}
	}
	return out
}

func TestCompletionKnowsEveryFlag(t *testing.T) {
	sets := flagSets(t)
	if len(sets) < 10 {
		t.Fatalf("found only %d flag sets: %v", len(sets), sets)
	}
	described := map[string][]string{}
	for _, c := range commands() {
		described[c.Name] = []string{}
		for _, f := range c.Flags {
			described[c.Name] = append(described[c.Name], f.Name)
		}
	}
	for command, flags := range sets {
		got, ok := described[command]
		if !ok {
			t.Errorf("command %s is missing from commands()", command)
			continue
		}
		slices.Sort(flags)
		slices.Sort(got)
		if !slices.Equal(flags, got) {
			t.Errorf("command %s: flags %v, completion knows %v", command, flags, got)
		}
	}
	// Every command of run is completed.
	src, _ := os.ReadFile("main.go")
	for _, c := range commands() {
		if !strings.Contains(string(src), `case "`+c.Name+`"`) {
			t.Errorf("completion offers %s, which run does not know", c.Name)
		}
	}
}

func TestCompletionCommand(t *testing.T) {
	out := mustRun(t, "completion", "zsh")
	if !strings.Contains(out, "compdef _platformgo platformgo pgo") || !strings.Contains(out, "monitoring") {
		t.Errorf("zsh script:\n%s", out)
	}
	if _, err := output(t, "completion", "tcsh"); err == nil {
		t.Error("tcsh is accepted")
	}
}

func TestSetupWritesCompletion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	out := mustRun(t, "setup", "--shell", "bash", "--alias")
	if !strings.Contains(out, ".bashrc") {
		t.Errorf("output:\n%s", out)
	}
	if out := mustRun(t, "setup", "--shell", "bash", "--alias"); !strings.Contains(out, "already set up") {
		t.Errorf("second run:\n%s", out)
	}
	mustRun(t, "setup", "--shell", "bash", "--remove")
	if raw, _ := os.ReadFile(filepath.Join(home, ".bashrc")); strings.Contains(string(raw), "platformgo") {
		t.Errorf(".bashrc after remove:\n%s", raw)
	}
}

func TestDoctorFixesProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/zsh")
	dir := filepath.Join(t.TempDir(), "project")
	mustRun(t, "new", "example.com/shop", "--dir", dir, "--with", "monitoring")
	if err := os.Remove(filepath.Join(dir, gen.ModulesPath)); err != nil {
		t.Fatal(err)
	}

	out, err := output(t, "doctor", "--dir", dir)
	if err == nil || !strings.Contains(out, "stale") || !strings.Contains(out, "doctor --fix") {
		t.Fatalf("doctor did not see the problems: %v\n%s", err, out)
	}
	for _, want := range []string{"warn   .env", "warn   monitoring/.env", "warn   completion"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}

	out, err = output(t, "doctor", "--dir", dir, "--fix")
	if err != nil && strings.Contains(out, "fail   generation") {
		t.Fatalf("doctor --fix: %v\n%s", err, out)
	}
	for _, path := range []string{gen.ModulesPath, ".env", "monitoring/.env"} {
		if _, err := os.Stat(filepath.Join(dir, path)); err != nil {
			t.Errorf("%s was not fixed: %v\n%s", path, err, out)
		}
	}
	out, _ = output(t, "doctor", "--dir", dir)
	if strings.Contains(out, "warn   .env") || strings.Contains(out, "stale") || strings.Contains(out, "warn   completion") {
		t.Errorf("problems left after --fix:\n%s", out)
	}
}

func TestProjectPlatform(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) {
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("module example.com/a\n\ngo 1.27\n")
	if _, ok := projectPlatform(dir); ok {
		t.Error("a project without the tool delegates")
	}
	write("module example.com/a\n\ngo 1.27\n\nrequire github.com/aidarbn/platform-go v0.2.2\n\ntool github.com/aidarbn/platform-go/cmd/platformgo\n")
	if v, ok := projectPlatform(dir); !ok || v != "v0.2.2" {
		t.Errorf("pinned = %q, %v", v, ok)
	}
	write("module example.com/a\n\ngo 1.27\n\nrequire github.com/aidarbn/platform-go v0.2.2\n\ntool github.com/aidarbn/platform-go/cmd/platformgo\n\nreplace github.com/aidarbn/platform-go => ../platform-go\n")
	if v, ok := projectPlatform(dir); !ok || v != "local" {
		t.Errorf("pinned = %q, %v", v, ok)
	}
}

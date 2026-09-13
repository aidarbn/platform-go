// Package lint runs the checks of a project in one place: the checks of taply, with
// the ones that depend on a module run only when the module is enabled.
package lint

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Linters run with go run at a pinned version instead of as tools in go.mod: their
// dependencies would share the module graph of the project and can clash with it —
// protoc-gen-connect-openapi needs gobwas/glob v1, the linters inside golangci-lint
// need v0.2.
const (
	golangciLint = "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2"
	govulncheck  = "golang.org/x/vuln/cmd/govulncheck@v1.8.0"
)

// DefaultMaxLines is the file length limit of taply.
const DefaultMaxLines = 1000

// Options select what runs.
type Options struct {
	Skip         []string // names of checks to skip
	MaxLines     int      // file length limit; DefaultMaxLines when zero
	ProtoAgainst string   // git branch to check proto breaking changes against; off when empty
	Modules      []string // enabled modules
}

// Check is one named check.
type Check struct {
	Name string
	Run  func(ctx context.Context, dir string) error
}

// Runner runs a command in the project. Tests replace it.
type Runner func(ctx context.Context, dir string, out io.Writer, env []string, name string, args ...string) error

// Checks returns the checks for a project in the order they run: cheap ones first, so
// a formatting slip fails in a second rather than after the vulnerability scan.
func Checks(opts Options, run Runner, verify func(ctx context.Context, dir string) error) []Check {
	maxLines := opts.MaxLines
	if maxLines <= 0 {
		maxLines = DefaultMaxLines
	}

	checks := []Check{
		{"format", func(ctx context.Context, dir string) error { return Format(dir) }},
		{"tidy", func(ctx context.Context, dir string) error {
			return run(ctx, dir, io.Discard, nil, "go", "mod", "tidy", "-diff")
		}},
		{"build", func(ctx context.Context, dir string) error {
			return run(ctx, dir, io.Discard, []string{"CGO_ENABLED=0"}, "go", "build", "./...")
		}},
		{"generate", verify},
		{"files", func(ctx context.Context, dir string) error { return Files(dir, maxLines) }},
		{"go", func(ctx context.Context, dir string) error {
			return run(ctx, dir, io.Discard, nil, "go", "run", golangciLint, "run", "./...")
		}},
	}
	if slices.Contains(opts.Modules, "api") {
		checks = append(checks, Check{"proto", func(ctx context.Context, dir string) error {
			if err := run(ctx, dir, io.Discard, nil, "go", "tool", "buf", "lint"); err != nil {
				return err
			}
			if opts.ProtoAgainst == "" {
				return nil
			}
			return run(ctx, dir, io.Discard, nil, "go", "tool", "buf", "breaking", "--against", ".git#branch="+opts.ProtoAgainst)
		}})
	}
	checks = append(checks, Check{"security", func(ctx context.Context, dir string) error {
		return run(ctx, dir, io.Discard, nil, "go", "run", govulncheck, "./...")
	}})

	out := checks[:0]
	for _, c := range checks {
		if !slices.Contains(opts.Skip, c.Name) {
			out = append(out, c)
		}
	}
	return out
}

// Names lists every check name, for flag help.
func Names() []string {
	return []string{"format", "tidy", "build", "generate", "files", "go", "proto", "security"}
}

// Run runs the checks, reports each one and returns an error when any failed. All
// checks run even after a failure, so one run shows everything that needs fixing.
func Run(ctx context.Context, dir string, checks []Check, out io.Writer) error {
	var failed []string
	for _, c := range checks {
		if err := c.Run(ctx, dir); err != nil {
			failed = append(failed, c.Name)
			fmt.Fprintf(out, "FAIL %-9s %s\n", c.Name, indent(err.Error()))
			continue
		}
		fmt.Fprintf(out, "ok   %s\n", c.Name)
	}
	if len(failed) > 0 {
		return fmt.Errorf("lint failed: %s", strings.Join(failed, ", "))
	}
	return nil
}

func indent(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n               ")
}

// generatedHeader is the Go convention for generated files.
var generatedHeader = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.?$`)

// skipDir are folders that hold no project code.
func skipDir(name string) bool {
	return name != "." && (strings.HasPrefix(name, ".") || slices.Contains([]string{"vendor", "node_modules", "bin", "dist"}, name))
}

// Format fails when a Go file of the project is not gofmt formatted.
func Format(dir string) error {
	var bad []string
	err := walkGo(dir, func(path string, content []byte) error {
		formatted, err := gofmt(content)
		if err != nil {
			bad = append(bad, path+" (does not parse)")
			return nil
		}
		if string(formatted) != string(content) {
			bad = append(bad, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(bad) > 0 {
		return fmt.Errorf("not formatted, run gofmt -w .:\n%s", strings.Join(bad, "\n"))
	}
	return nil
}

// Files fails when a hand written Go file is longer than maxLines: a file that long is
// several files that have not been split yet. Generated files are exempt.
func Files(dir string, maxLines int) error {
	var bad []string
	err := walkGo(dir, func(path string, content []byte) error {
		if isGenerated(content) {
			return nil
		}
		if n := countLines(content); n > maxLines {
			bad = append(bad, fmt.Sprintf("%s has %d lines (max %d)", path, n, maxLines))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(bad) > 0 {
		return errors.New(strings.Join(bad, "\n"))
	}
	return nil
}

func walkGo(dir string, fn func(path string, content []byte) error) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		return fn(filepath.ToSlash(rel), content)
	})
}

func isGenerated(content []byte) bool {
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if generatedHeader.MatchString(line) {
			return true
		}
	}
	return false
}

func countLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	n := strings.Count(string(content), "\n")
	if content[len(content)-1] != '\n' {
		n++
	}
	return n
}

// Package upgrade moves a project to another platform version.
//
// The new version is required in go.mod, recorded in platformgo.yaml, and then applied
// by the new version itself through go tool: only it knows its generated files, its
// tools, its codemods and its checks. Afterwards the project is built, verified and
// tested. A dry run does all of it in a copy of the project and prints the plan.
package upgrade

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aidarbn/platform-go/internal/gomod"
	"github.com/aidarbn/platform-go/internal/spec"
)

// Module is the Go module of the platform.
const Module = "github.com/aidarbn/platform-go"

// Runner runs the go command in the project. Tests replace it.
type Runner func(ctx context.Context, dir string, out io.Writer, args ...string) error

// Options say how to upgrade.
type Options struct {
	Version string // a tag, or latest when empty
	DryRun  bool   // upgrade a copy of the project and print the plan; the project stays as it is
	NoCheck bool   // skip go build, platformgo verify and go test after the upgrade
}

// Check is a command run after the upgrade.
type Check struct {
	Name   string
	Args   []string // of the go command
	Err    error
	Output string // of a dry run
}

// Checks are the commands that prove the upgraded project: it builds, its generation
// and manual steps are done, its tests pass.
func Checks() []Check {
	return []Check{
		{Name: "go build", Args: []string{"build", "./..."}},
		{Name: "platformgo verify", Args: []string{"tool", "platformgo", "verify"}},
		{Name: "go test", Args: []string{"test", "./..."}},
	}
}

// Result says what changed.
type Result struct {
	From, To string
	Untidy   string  // go mod tidy -diff before the upgrade: what apply would tidy on top of it
	TidyErr  string  // go mod tidy -diff could not run: the reason
	Plan     string  // platformgo plan of the new version, in a dry run
	Checks   []Check // run after the upgrade
}

// Failed lists the checks that failed.
func (r Result) Failed() []string {
	var out []string
	for _, c := range r.Checks {
		if c.Err != nil {
			out = append(out, c.Name)
		}
	}
	return out
}

var platformLine = regexp.MustCompile(`(?m)^platform:[ \t]*\S*[ \t]*$`)

// Run upgrades the project in dir.
func Run(ctx context.Context, dir string, opts Options, run Runner, out io.Writer) (Result, error) {
	if opts.Version == "" {
		opts.Version = "latest"
	}
	if _, err := spec.Load(filepath.Join(dir, spec.FileName)); err != nil {
		return Result{}, err
	}
	if opts.DryRun {
		return dryRun(ctx, dir, opts, run, out)
	}

	res, err := upgrade(ctx, dir, opts.Version, run, out)
	if res.Untidy != "" {
		fmt.Fprintf(out, "warning: go.mod was not tidy before the upgrade, so the upgrade carries this too; commit go mod tidy apart:\n%s", indent(res.Untidy))
	}
	if res.TidyErr != "" {
		fmt.Fprintf(out, "warning: could not check that go.mod was tidy before the upgrade:\n%s", indent(res.TidyErr))
	}
	if err != nil {
		return res, err
	}
	if err := run(ctx, dir, out, "tool", "platformgo", "apply"); err != nil {
		return res, fmt.Errorf("upgrade: apply of %s: %w", res.To, err)
	}
	if res.From == res.To || opts.NoCheck {
		return res, nil
	}
	res.Checks = runChecks(ctx, dir, run, func(Check) io.Writer { return out })
	if failed := res.Failed(); len(failed) > 0 {
		return res, fmt.Errorf("upgrade: %s → %s is applied, but checks failed: %s", res.From, res.To, strings.Join(failed, ", "))
	}
	return res, nil
}

// upgrade requires the new version and records it.
func upgrade(ctx context.Context, dir, version string, run Runner, out io.Writer) (Result, error) {
	before, err := gomod.Read(dir)
	if err != nil {
		return Result{}, err
	}
	res := Result{From: before.Requires[Module]}

	// apply runs go mod tidy: an untidy go.mod would sneak into the upgrade commit.
	var diff bytes.Buffer
	if err := run(ctx, dir, &diff, "mod", "tidy", "-diff"); err != nil {
		// A diff is what tidy would change; anything else, a download that failed, says
		// nothing about the project.
		if text := diff.String(); strings.HasPrefix(text, "diff ") || strings.Contains(text, "\n+++ ") {
			res.Untidy = text
		} else {
			res.TidyErr = strings.TrimSpace(err.Error() + "\n" + text)
		}
	}

	if err := run(ctx, dir, out, "get", Module+"@"+version); err != nil {
		return res, fmt.Errorf("upgrade: %w", err)
	}
	after, err := gomod.Read(dir)
	if err != nil {
		return res, err
	}
	res.To = after.Requires[Module]
	if res.To == "" {
		return res, fmt.Errorf("upgrade: go.mod does not require %s", Module)
	}
	if err := recordVersion(dir, res.To); err != nil {
		return res, err
	}
	return res, nil
}

func runChecks(ctx context.Context, dir string, run Runner, output func(Check) io.Writer) []Check {
	checks := Checks()
	for i := range checks {
		checks[i].Err = run(ctx, dir, output(checks[i]), checks[i].Args...)
	}
	return checks
}

// dryRun upgrades a copy of the project and prints what the upgrade would do.
func dryRun(ctx context.Context, dir string, opts Options, run Runner, out io.Writer) (Result, error) {
	work, err := os.MkdirTemp("", "platformgo-upgrade-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(work)
	copyDir := filepath.Join(work, "project")
	if err := copyProject(dir, copyDir); err != nil {
		return Result{}, fmt.Errorf("upgrade: copy of the project: %w", err)
	}

	var log bytes.Buffer
	res, err := upgrade(ctx, copyDir, opts.Version, run, &log)
	if err != nil {
		return res, fmt.Errorf("%w\n%s", err, indent(log.String()))
	}

	fmt.Fprintf(out, "platform    %s → %s\n", res.From, res.To)
	if res.From == res.To {
		fmt.Fprintln(out, "already on", res.To)
		return res, nil
	}
	fmt.Fprintf(out, "config      %s: platform %s\n", spec.FileName, res.To)
	if res.Untidy != "" {
		fmt.Fprintf(out, "go.mod      not tidy before the upgrade, the upgrade would carry this; commit go mod tidy apart:\n%s", indent(res.Untidy))
	}
	if res.TidyErr != "" {
		fmt.Fprintf(out, "go.mod      could not check that it is tidy:\n%s", indent(res.TidyErr))
	}

	var plan bytes.Buffer
	if err := run(ctx, copyDir, &plan, "tool", "platformgo", "plan"); err != nil {
		return res, fmt.Errorf("upgrade: plan of %s: %w\n%s", res.To, err, indent(plan.String()))
	}
	res.Plan = plan.String()
	fmt.Fprintf(out, "plan of %s:\n%s", res.To, indent(res.Plan))

	log.Reset()
	if err := run(ctx, copyDir, &log, "tool", "platformgo", "apply"); err != nil {
		return res, fmt.Errorf("upgrade: apply of %s: %w\n%s", res.To, err, indent(log.String()))
	}
	if opts.NoCheck {
		return res, nil
	}

	outputs := map[string]*bytes.Buffer{}
	res.Checks = runChecks(ctx, copyDir, run, func(c Check) io.Writer {
		outputs[c.Name] = &bytes.Buffer{}
		return outputs[c.Name]
	})
	fmt.Fprintln(out, "checks:")
	for i, c := range res.Checks {
		res.Checks[i].Output = outputs[c.Name].String()
		if c.Err == nil {
			fmt.Fprintf(out, "  ok    %s\n", c.Name)
			continue
		}
		fmt.Fprintf(out, "  fail  %s: %v\n%s", c.Name, c.Err, indent(indent(res.Checks[i].Output)))
	}
	if failed := res.Failed(); len(failed) > 0 {
		return res, fmt.Errorf("upgrade: %s → %s would fail checks: %s", res.From, res.To, strings.Join(failed, ", "))
	}
	return res, nil
}

// copyProject copies the project for a dry run, without .git and node_modules. Replace
// directives that point at a folder outside the project by a relative path are made
// absolute, so the copy builds against the same code.
func copyProject(src, dst string) error {
	src, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if path != src && (d.Name() == ".git" || d.Name() == "node_modules") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil // .git of a worktree is a file pointing at the repository
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
	if err != nil {
		return err
	}
	if !gomod.Exists(dst) {
		return nil
	}
	mod, err := gomod.Read(dst)
	if err != nil {
		return err
	}
	for _, r := range mod.Local {
		if filepath.IsAbs(r.Dir) || !strings.HasPrefix(filepath.ToSlash(filepath.Clean(r.Dir)), "../") {
			continue // a folder inside the project is copied with it
		}
		r.Dir = filepath.Join(src, r.Dir)
		if err := gomod.SetLocalReplace(dst, r); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}

func indent(text string) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return ""
	}
	return "    " + strings.ReplaceAll(text, "\n", "\n    ") + "\n"
}

// recordVersion writes the platform version into platformgo.yaml and the schema link of
// its first line, keeping the rest of the file as the developer wrote it.
func recordVersion(dir, version string) error {
	path := filepath.Join(dir, spec.FileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(raw)

	if platformLine.MatchString(text) {
		text = platformLine.ReplaceAllString(text, "platform: "+version)
	} else {
		text = strings.Replace(text, "schema: ", "platform: "+version+"\nschema: ", 1)
	}
	schemaLink := regexp.MustCompile(`platform-go/[^/\s]+/schema/platformgo\.schema\.json`)
	text = schemaLink.ReplaceAllString(text, "platform-go/"+version+"/schema/platformgo.schema.json")

	if _, err := spec.Parse([]byte(text)); err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// Command platformgo creates, generates and maintains Go projects on the platform.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/aidarbn/platform-go/internal/apply"
	"github.com/aidarbn/platform-go/internal/codegen"
	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/lint"
	"github.com/aidarbn/platform-go/internal/lock"
	"github.com/aidarbn/platform-go/internal/registry"
	"github.com/aidarbn/platform-go/internal/scaffold"
	"github.com/aidarbn/platform-go/internal/spec"
	"github.com/aidarbn/platform-go/internal/upgrade"
	"github.com/aidarbn/platform-go/kit/pgdb"
)

func main() {
	if code, delegated := delegate(os.Args[1:]); delegated {
		os.Exit(code)
	}
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		usage(out)
		return nil
	}

	switch args[0] {
	case "new":
		return cmdNew(args[1:], out)
	case "generate":
		return cmdGenerate(args[1:], out)
	case "plan":
		return cmdPlan(args[1:], out)
	case "apply":
		return cmdApply(args[1:], out)
	case "verify":
		return cmdVerify(args[1:], out)
	case "migrate":
		return cmdMigrate(args[1:], out)
	case "db":
		return cmdDB(args[1:], out)
	case "lint":
		return cmdLint(args[1:], out)
	case "upgrade":
		return cmdUpgrade(args[1:], out)
	case "schema":
		raw, err := spec.JSONSchema()
		if err != nil {
			return err
		}
		_, err = out.Write(raw)
		return err
	case "doctor":
		return cmdDoctor(args[1:], out)
	case "setup":
		return cmdSetup(args[1:], out)
	case "completion":
		return cmdCompletion(args[1:], out)
	case "version":
		fmt.Fprintln(out, version())
		return nil
	case "help", "-h", "--help":
		usage(out)
		return nil
	default:
		usage(out)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage(out io.Writer) {
	fmt.Fprint(out, `platformgo — a platform for Go projects

  platformgo new <module-path>    create a project
  platformgo plan                 show what apply would change
  platformgo apply [--no-tidy]    bring the project in line with platformgo.yaml
  platformgo generate [--check]   apply without go mod tidy; --check fails when stale
  platformgo verify               the same check as generate --check, for CI
  platformgo lint                 every check of the project: format, tidy, build, generation,
                                  file length, golangci-lint, proto, govulncheck
  platformgo migrate create <name> add an SQL migration to db/migrations
  platformgo db generate          migrate the database and generate the jet query builder
  platformgo upgrade [version]    move the project to a platform version, latest by default
  platformgo schema               print the JSON schema of platformgo.yaml
  platformgo doctor [--fix]       check the development environment and the project
  platformgo setup [--alias]      shell completion and the short pgo alias
  platformgo completion <shell>   print the completion script: bash, fish, zsh
  platformgo version              print the version

A project is described in `+spec.FileName+`.
`)
}

func cmdNew(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	dir := fs.String("dir", "", "project directory; defaults to the service name")
	service := fs.String("service", "", "service name; defaults to the last element of the module path")
	with := fs.String("with", "", "comma separated modules: "+strings.Join(registry.Names(), ", "))
	module, err := parsePositional(fs, args)
	if err != nil {
		return err
	}
	if module == "" {
		return fmt.Errorf("pass a Go module path, for example platformgo new github.com/me/shop-api")
	}

	opts := scaffold.Options{
		Dir:     *dir,
		Module:  module,
		Service: *service,
	}
	if *with != "" {
		opts.Modules = strings.Split(*with, ",")
		for i := range opts.Modules {
			opts.Modules[i] = strings.TrimSpace(opts.Modules[i])
		}
	}
	if opts.Dir == "" {
		parts := strings.Split(opts.Module, "/")
		opts.Dir = parts[len(parts)-1]
	}

	created, err := scaffold.New(opts)
	if err != nil {
		return err
	}
	for _, path := range created {
		fmt.Fprintln(out, "created", filepath.Join(opts.Dir, path))
	}
	fmt.Fprintf(out, "\nnext:\n  cd %s && go mod tidy && make run\n", opts.Dir)
	return nil
}

func cmdGenerate(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project directory")
	check := fs.Bool("check", false, "do not write files, fail when anything is stale: for CI")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *check {
		return verify(*dir, out)
	}
	plan, err := runApply(*dir, out)
	if err != nil {
		return err
	}
	return runSqlc(plan, *dir, out)
}

// runSqlc runs the external generators of the enabled modules: sqlc for postgres, buf
// for api.
func runSqlc(plan *apply.Plan, dir string, out io.Writer) error {
	ctx := context.Background()
	if slices.Contains(plan.Modules, "postgres") {
		if err := codegen.Sqlc(ctx, dir, out); err != nil {
			return err
		}
	}
	if slices.Contains(plan.Modules, "api") {
		if err := codegen.Buf(ctx, dir, out); err != nil {
			return err
		}
	}
	return nil
}

func cmdVerify(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project directory")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	return verify(*dir, out)
}

// verify fails when the project does not match platformgo.yaml: a stale generated file,
// a leftover of a removed module or a lock out of date.
func verify(dir string, out io.Writer) error {
	if err := checkGenerated(context.Background(), dir); err != nil {
		return err
	}
	fmt.Fprintln(out, "generation is up to date")
	return nil
}

func checkGenerated(ctx context.Context, dir string) error {
	plan, err := apply.Build(dir)
	if err != nil {
		return err
	}
	if !plan.UpToDate() {
		return fmt.Errorf("generation is stale, run platformgo generate: %s", strings.Join(plan.Pending(), ", "))
	}
	if slices.Contains(plan.Modules, "postgres") {
		if err := codegen.SqlcCheck(context.Background(), dir); err != nil {
			return err
		}
	}
	if slices.Contains(plan.Modules, "api") {
		if err := codegen.BufCheck(ctx, dir); err != nil {
			return err
		}
	}
	return nil
}

func cmdLint(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project directory")
	skip := fs.String("skip", "", "comma separated checks to skip: "+strings.Join(lint.Names(), ", "))
	maxLines := fs.Int("max-lines", lint.DefaultMaxLines, "longest hand written Go file")
	against := fs.String("proto-against", "", "git branch to check proto breaking changes against")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	plan, err := apply.Build(*dir)
	if err != nil {
		return err
	}
	opts := lint.Options{MaxLines: *maxLines, ProtoAgainst: *against, Modules: plan.Modules}
	if *skip != "" {
		for _, name := range strings.Split(*skip, ",") {
			opts.Skip = append(opts.Skip, strings.TrimSpace(name))
		}
	}
	return lint.Run(context.Background(), *dir, lint.Checks(opts, runCommand, checkGenerated), out)
}

// runCommand runs a check command and puts its output into the error when it fails.
func runCommand(ctx context.Context, dir string, _ io.Writer, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func cmdApply(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project directory")
	noTidy := fs.Bool("no-tidy", false, "skip go mod tidy")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	plan, err := runApply(*dir, out)
	if err != nil {
		return err
	}
	if *noTidy {
		return nil
	}
	// Enabling or removing a module changes the Go dependencies of the project.
	if _, err := os.Stat(filepath.Join(*dir, "go.mod")); err != nil {
		return nil
	}
	fmt.Fprintln(out, "go mod tidy")
	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = *dir
	cmd.Stdout, cmd.Stderr = out, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	// The generators are in go.sum now, so the queries can be generated.
	return runSqlc(plan, *dir, out)
}

func cmdDB(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "generate" {
		return fmt.Errorf("usage: platformgo db generate [--dsn url]")
	}
	fs := flag.NewFlagSet("db generate", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project directory")
	dsnFlag := fs.String("dsn", "", "database address; DATABASE_URL, .env and .env.example otherwise")
	if err := parseFlags(fs, args[1:]); err != nil {
		return err
	}

	plan, err := apply.Build(*dir)
	if err != nil {
		return err
	}
	if !slices.Contains(plan.Modules, "postgres") {
		return errors.New("db generate needs the postgres module")
	}
	dsn, err := codegen.DSN(*dir, *dsnFlag)
	if err != nil {
		return err
	}

	ctx := context.Background()
	pool, err := pgdb.Open(ctx, pgdb.Config{URL: dsn, ConnectTimeout: 5 * time.Second})
	if err != nil {
		return err
	}
	defer pool.Close()

	// The builder reflects the schema, so the database first gets every migration.
	applied, err := pgdb.Migrate(ctx, pool, os.DirFS(filepath.Join(*dir, codegen.MigrationsDir)), nil)
	if err != nil {
		return err
	}
	for _, name := range applied {
		fmt.Fprintln(out, "migrated", name)
	}

	if err := codegen.Jet(ctx, *dir, dsn, out); err != nil {
		return err
	}
	fmt.Fprintln(out, "generated", codegen.JetOut)
	return nil
}

func runApply(dir string, out io.Writer) (*apply.Plan, error) {
	plan, err := apply.Build(dir)
	if err != nil {
		return nil, err
	}
	if plan.UpToDate() {
		fmt.Fprintln(out, "generation is up to date")
		return plan, nil
	}
	if err := plan.Execute(); err != nil {
		return nil, err
	}
	printChanges(out, plan, "created", "wrote", "deleted")
	return plan, nil
}

func cmdUpgrade(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project directory")
	version, err := parsePositional(fs, args)
	if err != nil {
		return err
	}

	run := func(ctx context.Context, dir string, out io.Writer, args ...string) error {
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = out, os.Stderr
		return cmd.Run()
	}
	res, err := upgrade.Run(context.Background(), *dir, version, run, out)
	if err != nil {
		return err
	}
	if res.From == res.To {
		fmt.Fprintf(out, "already on %s\n", res.To)
		return nil
	}
	fmt.Fprintf(out, "upgraded %s → %s\n", res.From, res.To)
	return nil
}

func cmdMigrate(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("usage: platformgo migrate create <name>")
	}
	fs := flag.NewFlagSet("migrate create", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project directory")
	name, err := parsePositional(fs, args[1:])
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(*dir, spec.FileName)); err != nil {
		return fmt.Errorf("%s: not a platformgo project: %w", *dir, err)
	}

	path, err := gen.NewMigration(*dir, name, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "created", path)
	return nil
}

func cmdPlan(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project directory")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	plan, err := apply.Build(*dir)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "service  %s\n", plan.Service)
	modules := strings.Join(plan.Modules, ", ")
	if modules == "" {
		modules = "none"
	}
	fmt.Fprintf(out, "modules  %s\n", modules)
	for _, m := range plan.Diff.AddedModules {
		fmt.Fprintf(out, "  + module %s\n", m)
	}
	for _, m := range plan.Diff.RemovedModules {
		fmt.Fprintf(out, "  - module %s\n", m)
	}

	if plan.UpToDate() && len(plan.Kept) == 0 {
		fmt.Fprintln(out, "no changes")
		return nil
	}
	printChanges(out, plan, "+", "~", "-")
	return nil
}

func printChanges(out io.Writer, plan *apply.Plan, create, write, del string) {
	for _, path := range plan.Create {
		fmt.Fprintln(out, create, path)
	}
	for _, path := range plan.Write {
		fmt.Fprintln(out, write, path)
	}
	for _, path := range plan.Delete {
		fmt.Fprintln(out, del, path)
	}
	for _, c := range plan.Codemods {
		fmt.Fprintf(out, "%s %s: codemod %s\n", write, c.Path, strings.Join(c.Codemods, ", "))
	}
	for _, t := range plan.AddTools {
		fmt.Fprintf(out, "%s tool %s@%s\n", create, t.Package, t.Version)
	}
	for _, pkg := range plan.DropTools {
		fmt.Fprintf(out, "%s tool %s\n", del, pkg)
	}
	for _, path := range plan.Kept {
		fmt.Fprintf(out, "kept %s: it belonged to a removed module but was edited by hand\n", path)
	}
	if plan.LockStale {
		fmt.Fprintln(out, write, lock.FileName)
	}
}

// parseFlags parses a command that takes no arguments of its own.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return nil
}

// parsePositional parses a command with one argument of its own, letting it stand
// before or after the flags: the flag package stops at the first positional argument,
// so what follows it is parsed in a second pass.
func parsePositional(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return "", nil
	}
	if err := fs.Parse(rest[1:]); err != nil {
		return "", err
	}
	if fs.NArg() > 0 {
		return "", fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return rest[0], nil
}

func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	var revision string
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 7 {
			revision = s.Value[:7]
		}
	}
	if revision == "" {
		return "dev"
	}
	return "dev+" + revision
}

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
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
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
	case "openapi":
		return cmdOpenAPI(args[1:], out)
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
  platformgo plan [--diff]        show what apply would change
  platformgo apply [--no-tidy] [--adopt] [--force path]
                                  bring the project in line with platformgo.yaml
  platformgo generate [--check]   apply without go mod tidy; --check fails when stale
  platformgo verify               the same check as generate --check, for CI
  platformgo lint                 every check of the project: format, tidy, build, generation,
                                  file length, golangci-lint, proto, migrations, govulncheck
  platformgo migrate create <name> add an SQL migration to db/migrations
  platformgo migrate lint         lint the migrations the branch adds (--against <branch>)
  platformgo db generate          migrate the database and generate the jet query builder
  platformgo upgrade [version]    move the project to a platform version, latest by default, and
                                  check it: go build, verify, go test (--no-check skips them,
                                  --strict fails on them); --dry-run does it in a copy
  platformgo openapi convert      move openapiv2 options of grpc-gateway in the proto files to the
                                  options of the platform's OpenAPI generator (--dry-run reports only)
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
	plan, err := runApply(*dir, nil, out)
	if err != nil {
		return err
	}
	return runSqlc(plan, *dir, out)
}

// runSqlc runs the external generators of the enabled modules: sqlc for postgres, buf
// for api, templ for web.
func runSqlc(plan *apply.Plan, dir string, out io.Writer) error {
	ctx := context.Background()
	if slices.Contains(plan.Modules, "postgres") {
		if err := codegen.Sqlc(ctx, dir, sqlcLayout(plan), out); err != nil {
			return err
		}
	}
	if slices.Contains(plan.Modules, "api") {
		if err := codegen.Buf(ctx, dir, apiLayout(plan), out); err != nil {
			return err
		}
	}
	if slices.Contains(plan.Modules, "web") {
		if err := codegen.Templ(ctx, dir, out); err != nil {
			return err
		}
	}
	return nil
}

// sqlcLayout is where sqlc works in the project, from the sqlc option of postgres.
func sqlcLayout(plan *apply.Plan) codegen.SqlcLayout {
	return codegen.SqlcLayout{Config: plan.Sqlc.ConfigPath(), Queries: plan.Sqlc.QueriesDir(), Out: plan.Sqlc.Out}
}

// apiLayout is where buf works in the project, from the options of api.
func apiLayout(plan *apply.Plan) codegen.APILayout {
	return codegen.APILayout{ProtoDir: plan.API.ProtoDir, OpenAPI: plan.API.GeneratesOpenAPI(), Split: plan.API.OpenAPI.Generator == gen.OpenAPIPlatform}
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
	if len(plan.Conflicts) > 0 {
		return fmt.Errorf("files platformgo generates are the project's own: %s; resolve them, see platformgo plan --diff and platformgo apply --adopt", strings.Join(plan.ConflictPaths(), ", "))
	}
	if !plan.UpToDate() {
		return fmt.Errorf("generation is stale, run platformgo generate: %s", strings.Join(plan.Pending(), ", "))
	}
	if len(plan.Notes) > 0 {
		var b strings.Builder
		printNotes(&b, plan)
		return fmt.Errorf("manual steps of a platform upgrade are open:\n%s", strings.TrimSuffix(b.String(), "\n"))
	}
	if slices.Contains(plan.Modules, "postgres") {
		if err := codegen.SqlcCheck(ctx, dir, sqlcLayout(plan)); err != nil {
			return err
		}
	}
	if slices.Contains(plan.Modules, "api") {
		if err := codegen.BufCheck(ctx, dir, apiLayout(plan)); err != nil {
			return err
		}
	}
	if slices.Contains(plan.Modules, "web") {
		if err := codegen.TemplCheck(ctx, dir); err != nil {
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
	against := fs.String("against", "", "base branch: proto breaking changes and the migrations it lacks")
	// The flag before migrations were linted too; Makefiles of existing projects pass it.
	protoAgainst := fs.String("proto-against", "", "the same as --against")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *against == "" {
		against = protoAgainst
	}

	plan, err := apply.Build(*dir)
	if err != nil {
		return err
	}
	opts := lint.Options{MaxLines: *maxLines, Against: *against, Modules: plan.Modules, API: apiLayout(plan), Exec: lint.ExecCommand}
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
	adopt := fs.Bool("adopt", false, "for a project with files of its own: create only what does not exist, report the conflicts in "+apply.AdoptReport)
	var force stringList
	fs.Func("force", "take the generated version of a conflicting file; repeat or separate with commas", force.Set)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *adopt {
		return runAdopt(*dir, force, out)
	}
	plan, err := runApply(*dir, force, out)
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
	if err := goModTidy(*dir, out); err != nil {
		return err
	}
	// The generators are in go.sum now, so the queries can be generated.
	if err := generators(plan, *dir, out); err != nil {
		return err
	}
	// The generated code may import other packages than before: a codemod that drops
	// an import of a proto file, as openapi.gnostic does, drops the Go module of that
	// import from the generated code, and go.mod would keep it.
	if err := goModTidy(*dir, out); err != nil {
		return fmt.Errorf("go mod tidy after the generators: %w", err)
	}
	return nil
}

// goModTidy and generators are what apply runs after it writes the files; tests replace
// them, so those tests can not run in parallel.
var (
	goModTidy = func(dir string, out io.Writer) error {
		fmt.Fprintln(out, "go mod tidy")
		cmd := exec.Command("go", "mod", "tidy")
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = out, os.Stderr
		return cmd.Run()
	}
	generators = runSqlc
)

// stringList is a flag that may repeat and holds comma separated values.
type stringList []string

// Set adds the values of one occurrence of the flag.
func (l *stringList) Set(v string) error {
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			*l = append(*l, item)
		}
	}
	return nil
}

// runAdopt applies to a project that has files of its own: only what does not exist is
// created, and the conflicts go into a report instead of failing.
func runAdopt(dir string, force []string, out io.Writer) error {
	plan, err := apply.Build(dir)
	if err != nil {
		return err
	}
	if err := plan.Force(force...); err != nil {
		return err
	}
	created, report, err := plan.Adopt()
	if err != nil {
		return err
	}
	for _, path := range created {
		fmt.Fprintln(out, "created", path)
	}
	fmt.Fprintln(out, "wrote", lock.FileName)
	for _, c := range plan.Conflicts {
		fmt.Fprintln(out, "!", c.Path+": the project's own, kept")
	}
	if report {
		fmt.Fprintf(out, "report %s: the conflicts and what apply left; run platformgo apply once they are resolved\n", apply.AdoptReport)
	}
	return nil
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

func runApply(dir string, force []string, out io.Writer) (*apply.Plan, error) {
	plan, err := apply.Build(dir)
	if err != nil {
		return nil, err
	}
	if err := plan.Force(force...); err != nil {
		return nil, err
	}
	if plan.UpToDate() {
		fmt.Fprintln(out, "generation is up to date")
		printNotes(out, plan)
		return plan, nil
	}
	if len(plan.Conflicts) > 0 {
		printConflicts(out, plan)
		return nil, fmt.Errorf("%d generated files are the project's own; nothing was changed: apply --adopt to create the rest and get a report, apply --force <path> to take the generated version", len(plan.Conflicts))
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
	dryRun := fs.Bool("dry-run", false, "upgrade a copy of the project and print the plan; the project stays as it is")
	noCheck := fs.Bool("no-check", false, "skip go build, platformgo verify and go test after the upgrade: for CI with its own checks")
	strict := fs.Bool("strict", false, "fail when a check after the upgrade fails; without it a failed check is a warning")
	version, err := parsePositional(fs, args)
	if err != nil {
		return err
	}

	// Ctrl-C stops go and still removes the copy of a dry run.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	opts := upgrade.Options{Version: version, DryRun: *dryRun, NoCheck: *noCheck, Strict: *strict}
	res, err := upgrade.Run(ctx, *dir, opts, upgradeRunner, out)
	if err != nil {
		return err
	}
	switch {
	case *dryRun:
	case res.From == res.To:
		fmt.Fprintf(out, "already on %s\n", res.To)
	default:
		fmt.Fprintf(out, "upgraded %s → %s\n", res.From, res.To)
	}
	return nil
}

// upgradeRunner runs the go command for upgrade; tests replace it.
var upgradeRunner upgrade.Runner = goRunner

// goRunner runs the go command for upgrade. Its output goes to out; its errors go to
// stderr when out is the terminal output, as they always did, and into out when the
// output is captured, for a dry run or the go.mod check.
func goRunner(ctx context.Context, dir string, out io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	if out == io.Writer(os.Stdout) {
		cmd.Stderr = os.Stderr
	}
	return cmd.Run()
}

func cmdMigrate(args []string, out io.Writer) error {
	if len(args) > 0 && args[0] == "lint" {
		return cmdMigrateLint(args[1:], out)
	}
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("usage: platformgo migrate create <name> | platformgo migrate lint [--against <branch>]")
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

// cmdMigrateLint lints the migrations a branch adds. It needs no platformgo.yaml, so a
// project the platform did not create runs it as well, with go run at a version.
func cmdMigrateLint(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("migrate lint", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project directory")
	against := fs.String("against", "", "base branch; without it the migrations not committed yet are linted")
	migrations := fs.String("migrations", "", "migrations folder in the project (default db/migrations)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	m := lint.Migrations{Dir: *migrations, Against: *against, Exec: lint.ExecCommand}
	if err := m.Run(context.Background(), *dir); err != nil {
		return err
	}
	fmt.Fprintln(out, "ok   migrations")
	return nil
}

func cmdPlan(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project directory")
	showDiff := fs.Bool("diff", false, "show each conflict as a diff from the project's file to the generated one")
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
		printNotes(out, plan)
		return nil
	}
	printChanges(out, plan, "+", "~", "-")
	printConflicts(out, plan)
	if *showDiff {
		for _, c := range plan.Conflicts {
			fmt.Fprint(out, "\n"+apply.UnifiedDiff(c.Path+" (project)", c.Path+" (platformgo)", c.Disk, c.Generated))
		}
	}
	return nil
}

// printConflicts lists the generated files the project has in a version of its own.
func printConflicts(out io.Writer, plan *apply.Plan) {
	for _, c := range plan.Conflicts {
		fmt.Fprintf(out, "! %s: the project's own, not generated by platformgo; apply keeps it\n", c.Path)
	}
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
	printNotes(out, plan)
}

// printNotes lists what codemods left to a person.
func printNotes(out io.Writer, plan *apply.Plan) {
	for _, n := range plan.Notes {
		fmt.Fprintln(out, "manual", n)
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

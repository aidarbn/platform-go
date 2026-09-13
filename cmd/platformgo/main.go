// Command platformgo creates, generates and maintains Go projects on the platform.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/registry"
	"github.com/aidarbn/platform-go/internal/scaffold"
	"github.com/aidarbn/platform-go/internal/spec"
)

func main() {
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
	case "doctor":
		return cmdDoctor(args[1:], out)
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
  platformgo generate [--check]   generate module wiring and the environment example
  platformgo plan                 show what generate would change
  platformgo doctor               check the development environment
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
	check := fs.Bool("check", false, "do not write files, fail on differences: for CI")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	files, err := projectFiles(*dir)
	if err != nil {
		return err
	}

	if *check {
		changed, err := gen.Changed(*dir, files)
		if err != nil {
			return err
		}
		if len(changed) > 0 {
			return fmt.Errorf("generation is stale, run platformgo generate: %s", strings.Join(changed, ", "))
		}
		fmt.Fprintln(out, "generation is up to date")
		return nil
	}

	written, err := gen.Apply(*dir, files)
	if err != nil {
		return err
	}
	if len(written) == 0 {
		fmt.Fprintln(out, "generation is up to date")
		return nil
	}
	for _, path := range written {
		fmt.Fprintln(out, "wrote", path)
	}
	return nil
}

func cmdPlan(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project directory")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	f, err := spec.Load(filepath.Join(*dir, spec.FileName))
	if err != nil {
		return err
	}
	files, err := gen.Files(*dir, f)
	if err != nil {
		return err
	}
	changed, err := gen.Changed(*dir, files)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "service  %s\n", f.Service())
	names := make([]string, 0, len(f.EnabledModules()))
	for _, m := range f.EnabledModules() {
		names = append(names, m.Name)
	}
	if len(names) == 0 {
		names = append(names, "none")
	}
	fmt.Fprintf(out, "modules  %s\n", strings.Join(names, ", "))

	if len(changed) == 0 {
		fmt.Fprintln(out, "no changes")
		return nil
	}
	fmt.Fprintln(out, "will be rewritten:")
	for _, path := range changed {
		fmt.Fprintln(out, "  ~", path)
	}
	return nil
}

func cmdDoctor(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	fmt.Fprintf(out, "%-10s %s\n", "go", runtime.Version())
	fmt.Fprintf(out, "%-10s %s\n", "platformgo", version())

	var missing []string
	for _, bin := range []string{"git", "docker"} {
		if path, err := exec.LookPath(bin); err == nil {
			fmt.Fprintf(out, "%-10s %s\n", bin, path)
			continue
		}
		fmt.Fprintf(out, "%-10s not found\n", bin)
		missing = append(missing, bin)
	}
	fmt.Fprintf(out, "%-10s %s\n", "modules", strings.Join(registry.Names(), ", "))

	if len(missing) > 0 {
		return fmt.Errorf("missing programs: %s", strings.Join(missing, ", "))
	}
	return nil
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

func projectFiles(dir string) (map[string][]byte, error) {
	f, err := spec.Load(filepath.Join(dir, spec.FileName))
	if err != nil {
		return nil, err
	}
	return gen.Files(dir, f)
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

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aidarbn/platform-go/internal/lint"
	"github.com/aidarbn/platform-go/internal/registry"
	"github.com/aidarbn/platform-go/internal/setup"
)

// commands describe the CLI for shell completion. A test compares them with the flag
// sets of the commands, so a new flag cannot be forgotten here.
func commands() []setup.Command {
	dir := setup.Flag{Name: "dir", Summary: "project directory", Dir: true}
	return []setup.Command{
		{Name: "new", Summary: "create a project", Flags: []setup.Flag{
			{Name: "dir", Summary: "project directory", Dir: true},
			{Name: "service", Summary: "service name"},
			{Name: "with", Summary: "modules", Values: registry.Names(), List: true},
		}},
		{Name: "plan", Summary: "show what apply would change", Flags: []setup.Flag{dir}},
		{Name: "apply", Summary: "bring the project in line with platformgo.yaml", Flags: []setup.Flag{
			dir, {Name: "no-tidy", Summary: "skip go mod tidy", Bool: true},
		}},
		{Name: "generate", Summary: "regenerate the project", Flags: []setup.Flag{
			dir, {Name: "check", Summary: "fail when anything is stale", Bool: true},
		}},
		{Name: "verify", Summary: "fail when generation is stale", Flags: []setup.Flag{dir}},
		{Name: "lint", Summary: "every check of the project", Flags: []setup.Flag{
			dir,
			{Name: "skip", Summary: "checks to skip", Values: lint.Names(), List: true},
			{Name: "max-lines", Summary: "longest hand written Go file"},
			{Name: "proto-against", Summary: "git branch for proto breaking changes"},
		}},
		{Name: "migrate", Summary: "add an SQL migration", Sub: []string{"create"}, Flags: []setup.Flag{dir}},
		{Name: "db", Summary: "generate the jet query builder", Sub: []string{"generate"}, Flags: []setup.Flag{
			dir, {Name: "dsn", Summary: "database address"},
		}},
		{Name: "upgrade", Summary: "move the project to a platform version", Flags: []setup.Flag{dir}},
		{Name: "doctor", Summary: "check the environment and the project", Flags: []setup.Flag{
			dir, {Name: "fix", Summary: "fix what can be fixed", Bool: true},
		}},
		{Name: "setup", Summary: "shell completion and the pgo alias", Flags: []setup.Flag{
			{Name: "shell", Summary: "shell", Values: setup.Shells},
			{Name: "alias", Summary: "add the pgo alias", Bool: true},
			{Name: "remove", Summary: "remove completion and the alias", Bool: true},
		}},
		{Name: "completion", Summary: "print the completion script", Sub: setup.Shells},
		{Name: "schema", Summary: "print the JSON schema of platformgo.yaml"},
		{Name: "version", Summary: "print the version"},
		{Name: "help", Summary: "show help"},
	}
}

func cmdSetup(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	shell := fs.String("shell", "", "shell: "+strings.Join(setup.Shells, ", ")+"; $SHELL by default")
	alias := fs.Bool("alias", false, "add the short alias pgo")
	remove := fs.Bool("remove", false, "remove the completion and the alias")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	opts, err := setupOptions(*shell)
	if err != nil {
		return err
	}
	opts.Alias, opts.Remove = *alias, *remove

	changes, err := setup.Install(opts, commands())
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		fmt.Fprintf(out, "%s is already set up\n", opts.Shell)
		return nil
	}
	for _, c := range changes {
		verb := "wrote"
		if c.Removed {
			verb = "deleted"
		}
		fmt.Fprintln(out, verb, c.Path)
	}
	if !*remove {
		fmt.Fprintln(out, "open a new shell to use it")
	}
	return nil
}

func setupOptions(shell string) (setup.Options, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return setup.Options{}, err
	}
	if shell == "" {
		if shell, err = setup.Detect(os.Getenv("SHELL")); err != nil {
			return setup.Options{}, err
		}
	}
	return setup.Options{Home: home, Config: os.Getenv("XDG_CONFIG_HOME"), Shell: shell}, nil
}

func cmdCompletion(args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: platformgo completion " + strings.Join(setup.Shells, "|"))
	}
	script, err := setup.Completion(args[0], commands(), "platformgo", "pgo")
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, script)
	return err
}

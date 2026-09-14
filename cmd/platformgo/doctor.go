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
	"strings"
	"time"

	"github.com/aidarbn/platform-go/internal/apply"
	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/setup"
	"github.com/aidarbn/platform-go/internal/spec"
)

// check is one line of doctor.
type check struct {
	name   string
	status string // ok, warn, fail
	detail string
	fix    func() (string, error) // nil when doctor cannot fix it
}

func cmdDoctor(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project directory")
	fix := fs.Bool("fix", false, "fix what doctor can: generation, .env files, shell completion")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	checks := environmentChecks()
	if _, err := os.Stat(filepath.Join(*dir, spec.FileName)); err == nil {
		checks = append(checks, projectChecks(*dir, out)...)
	}

	var failed, fixable int
	for _, c := range checks {
		if c.status != "ok" && c.fix != nil && *fix {
			done, err := c.fix()
			if err != nil {
				c.detail += "; fix failed: " + err.Error()
			} else {
				c.status, c.detail = "fixed", done
			}
		}
		fmt.Fprintf(out, "%-6s %-12s %s\n", c.status, c.name, c.detail)
		if c.status == "fail" {
			failed++
		}
		if (c.status == "fail" || c.status == "warn") && c.fix != nil {
			fixable++
		}
	}
	if fixable > 0 && !*fix {
		fmt.Fprintln(out, "\nplatformgo doctor --fix fixes what is marked fixable")
	}
	if failed > 0 {
		return fmt.Errorf("%d checks failed", failed)
	}
	return nil
}

func environmentChecks() []check {
	checks := []check{{name: "platformgo", status: "ok", detail: version()}}

	goVersion, err := probe("go", "env", "GOVERSION")
	if err != nil {
		checks = append(checks, check{name: "go", status: "fail", detail: "not found: https://go.dev/dl"})
	} else {
		checks = append(checks, check{name: "go", status: "ok", detail: goVersion})
	}

	if path, err := exec.LookPath("git"); err != nil {
		checks = append(checks, check{name: "git", status: "fail", detail: "not found"})
	} else {
		checks = append(checks, check{name: "git", status: "ok", detail: path})
	}

	switch _, err := exec.LookPath("docker"); {
	case err != nil:
		checks = append(checks, check{name: "docker", status: "warn", detail: "not found: local services and the monitoring stack need Docker with compose"})
	default:
		composeVersion, err := probe("docker", "compose", "version", "--short")
		if err != nil {
			checks = append(checks, check{name: "docker", status: "warn", detail: "docker compose is missing"})
			break
		}
		if _, err := probe("docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
			checks = append(checks, check{name: "docker", status: "warn", detail: "the Docker daemon does not answer: start Docker Desktop, colima or dockerd"})
			break
		}
		checks = append(checks, check{name: "docker", status: "ok", detail: "compose " + composeVersion})
	}

	checks = append(checks, shellCheck())
	return checks
}

// shellCheck looks for the completion setup wrote.
func shellCheck() check {
	c := check{name: "completion"}
	opts, err := setupOptions("")
	if err != nil {
		c.status, c.detail = "ok", "skipped: "+err.Error()
		return c
	}
	script, err := setup.ScriptPath(opts)
	if err != nil {
		c.status, c.detail = "ok", "skipped: "+err.Error()
		return c
	}
	if _, err := os.Stat(script); err == nil {
		c.status, c.detail = "ok", script
		return c
	}
	c.status, c.detail = "warn", "not set up for "+opts.Shell+" (fixable: platformgo setup)"
	c.fix = func() (string, error) {
		if _, err := setup.Install(opts, commands()); err != nil {
			return "", err
		}
		return "completion for " + opts.Shell + " installed, open a new shell", nil
	}
	return c
}

func projectChecks(dir string, out io.Writer) []check {
	f, err := spec.Load(filepath.Join(dir, spec.FileName))
	if err != nil {
		return []check{{name: "config", status: "fail", detail: err.Error()}}
	}
	checks := []check{{name: "config", status: "ok", detail: spec.FileName + ": service " + f.Service()}}

	switch pinned, ok := projectPlatform(dir); {
	case !ok:
		checks = append(checks, check{name: "pinned", status: "warn", detail: "go.mod has no tool " + platformTool + ": run platformgo upgrade"})
	case pinned == version():
		checks = append(checks, check{name: "pinned", status: "ok", detail: pinned})
	default:
		checks = append(checks, check{name: "pinned", status: "ok", detail: fmt.Sprintf("%s; project commands run it through go tool platformgo", pinned)})
	}

	plan, err := apply.Build(dir)
	switch {
	case err != nil:
		checks = append(checks, check{name: "generation", status: "fail", detail: err.Error()})
	case plan.UpToDate():
		checks = append(checks, check{name: "generation", status: "ok", detail: "up to date"})
	default:
		checks = append(checks, check{
			name: "generation", status: "fail",
			detail: "stale (fixable): " + strings.Join(plan.Pending(), ", "),
			fix: func() (string, error) {
				if err := cmdApply([]string{"--dir", dir}, out); err != nil {
					return "", err
				}
				return "applied", nil
			},
		})
	}

	envFiles := []string{".env"}
	if _, ok := f.Modules["monitoring"]; ok {
		envFiles = append(envFiles, "monitoring/.env")
	}
	for _, env := range envFiles {
		checks = append(checks, envCheck(dir, env))
	}
	return checks
}

// envCheck reports a missing local environment file and copies it from the example.
func envCheck(dir, name string) check {
	path := filepath.Join(dir, name)
	example := strings.TrimSuffix(path, ".env") + ".env.example"
	if name == ".env" {
		example = filepath.Join(dir, gen.EnvPath)
	}
	c := check{name: name}
	if _, err := os.Stat(path); err == nil {
		c.status, c.detail = "ok", "present"
		return c
	}
	if _, err := os.Stat(example); err != nil {
		c.status, c.detail = "warn", "missing, and so is its example: run platformgo generate"
		return c
	}
	c.status, c.detail = "warn", "missing, the example is used (fixable: copied from the example)"
	c.fix = func() (string, error) {
		raw, err := os.ReadFile(example)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return "", err
		}
		return "copied from " + filepath.Base(example) + ": review the secrets in it", nil
	}
	return c
}

// probe runs a program with a short timeout and returns its trimmed output.
func probe(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "", errors.New("no output")
	}
	return s, nil
}

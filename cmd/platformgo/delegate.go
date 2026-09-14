package main

import (
	"errors"
	"os"
	"os/exec"
	"slices"

	"github.com/aidarbn/platform-go/internal/gomod"
)

const (
	platformModule = "github.com/aidarbn/platform-go"
	platformTool   = platformModule + "/cmd/platformgo"
	delegatedEnv   = "PLATFORMGO_DELEGATED"
)

// global are the commands that do not belong to a project and never delegate.
var global = []string{"new", "setup", "completion", "doctor", "version", "help", "-h", "--help"}

// delegate runs the command with the platformgo version the project pins in go.mod
// when it differs from this binary: a project is generated, checked and upgraded by
// its own version, whatever is installed globally. It reports whether it ran the
// command and the exit code.
func delegate(args []string) (int, bool) {
	if len(args) == 0 || slices.Contains(global, args[0]) || os.Getenv(delegatedEnv) != "" {
		return 0, false
	}
	pinned, ok := projectPlatform(".")
	if !ok || pinned == version() {
		return 0, false
	}
	cmd := exec.Command("go", append([]string{"tool", "platformgo"}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), delegatedEnv+"=1")
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), true
		}
		// go itself is missing: let this binary do the work and say what it can.
		return 0, false
	}
	return 0, true
}

// projectPlatform returns the platform version a project pins, "local" when it points
// at a working copy with replace.
func projectPlatform(dir string) (string, bool) {
	if !gomod.Exists(dir) {
		return "", false
	}
	mod, err := gomod.Read(dir)
	if err != nil || !mod.HasTool(platformTool) {
		return "", false
	}
	if slices.Contains(mod.Replaced, platformModule) {
		return "local", true
	}
	return mod.Requires[platformModule], true
}

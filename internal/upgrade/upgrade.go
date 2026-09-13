// Package upgrade moves a project to another platform version.
//
// The new version is required in go.mod, recorded in platformgo.yaml, and then applied
// by the new version itself through go tool: only it knows its generated files, its
// tools and its checks.
package upgrade

import (
	"context"
	"fmt"
	"io"
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

// Result says what changed.
type Result struct {
	From, To string
}

var platformLine = regexp.MustCompile(`(?m)^platform:[ \t]*\S*[ \t]*$`)

// Run upgrades the project in dir to version: a tag, or latest.
func Run(ctx context.Context, dir, version string, run Runner, out io.Writer) (Result, error) {
	if version == "" {
		version = "latest"
	}
	if _, err := spec.Load(filepath.Join(dir, spec.FileName)); err != nil {
		return Result{}, err
	}
	before, err := gomod.Read(dir)
	if err != nil {
		return Result{}, err
	}
	from := before.Requires[Module]

	if err := run(ctx, dir, out, "get", Module+"@"+version); err != nil {
		return Result{}, fmt.Errorf("upgrade: %w", err)
	}
	after, err := gomod.Read(dir)
	if err != nil {
		return Result{}, err
	}
	to := after.Requires[Module]
	if to == "" {
		return Result{}, fmt.Errorf("upgrade: go.mod does not require %s", Module)
	}

	if err := recordVersion(dir, to); err != nil {
		return Result{}, err
	}

	// The new version applies itself: generated files, tools, lock, go mod tidy.
	if err := run(ctx, dir, out, "tool", "platformgo", "apply"); err != nil {
		return Result{From: from, To: to}, fmt.Errorf("upgrade: apply of %s: %w", to, err)
	}
	return Result{From: from, To: to}, nil
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

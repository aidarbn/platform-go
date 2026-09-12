// Package scaffold creates a new project on the platform.
//
// Only the domain part is written by hand in the created project: the entry point and
// the wiring function. Module wiring, settings and the environment example are generated.
package scaffold

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/registry"
	"github.com/aidarbn/platform-go/internal/spec"
	"github.com/aidarbn/platform-go/internal/version"
)

// PlatformModule is the Go module path of the platform.
const PlatformModule = "github.com/aidarbn/platform-go"

// Options describes the new project.
type Options struct {
	Dir       string   // project directory
	Module    string   // Go module path, for example github.com/me/shop-api
	Service   string   // service name; defaults to the last element of the module path
	Modules   []string // platform modules to enable
	GoVersion string   // Go version for go.mod
	Require   string   // platform version in go.mod
	Replace   string   // path to the platform for a replace directive: development and tests
}

func (o *Options) setDefaults() {
	if o.Service == "" {
		parts := strings.Split(o.Module, "/")
		o.Service = parts[len(parts)-1]
	}
	if o.GoVersion == "" {
		o.GoVersion = "1.27"
	}
	if o.Require == "" {
		o.Require = version.Platform
	}
	slices.Sort(o.Modules)
}

func (o *Options) validate() error {
	if o.Module == "" {
		return fmt.Errorf("the Go module path of the project is missing")
	}
	for _, name := range o.Modules {
		if _, ok := registry.Get(name); !ok {
			return fmt.Errorf("unknown module %q, known modules: %s", name, strings.Join(registry.Names(), ", "))
		}
	}
	return nil
}

// New creates the project and returns the list of created files.
func New(o Options) ([]string, error) {
	o.setDefaults()
	if err := o.validate(); err != nil {
		return nil, err
	}
	if err := ensureEmpty(o.Dir); err != nil {
		return nil, err
	}

	description := specFile(o)
	files := map[string][]byte{
		spec.FileName:     []byte(description),
		"go.mod":          []byte(goMod(o)),
		"cmd/app/main.go": []byte(mainGo),
		"cmd/app/wire.go": []byte(wireGo),
		"Makefile":        []byte(makefile),
		".gitignore":      []byte(gitignore),
		"README.md":       []byte(readme(o)),
	}

	// The wiring is generated right away so the project compiles from the first minute.
	f, err := spec.Parse([]byte(description))
	if err != nil {
		return nil, err
	}
	wiring, err := gen.Wiring(f)
	if err != nil {
		return nil, err
	}
	for path, content := range wiring {
		files[path] = content
	}

	return gen.Apply(o.Dir, files)
}

func ensureEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(dir, 0o755)
		}
		return err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			return fmt.Errorf("directory %s is not empty", dir)
		}
	}
	return nil
}

func specFile(o Options) string {
	var b strings.Builder
	fmt.Fprintf(&b, "schema: %d\n", spec.SchemaVersion)
	fmt.Fprintf(&b, "platform: %s\n\n", o.Require)
	b.WriteString("project:\n")
	fmt.Fprintf(&b, "  module: %s\n", o.Module)
	fmt.Fprintf(&b, "  service: %s\n", o.Service)
	fmt.Fprintf(&b, "  go: \"%s\"\n", o.GoVersion)
	b.WriteString("\nmodules:\n")
	if len(o.Modules) == 0 {
		b.WriteString("  {}\n")
		return b.String()
	}
	for _, name := range o.Modules {
		fmt.Fprintf(&b, "  %s: {}\n", name)
	}
	return b.String()
}

func goMod(o Options) string {
	var b strings.Builder
	fmt.Fprintf(&b, "module %s\n\ngo %s\n\nrequire %s %s\n", o.Module, o.GoVersion, PlatformModule, o.Require)
	if o.Replace != "" {
		fmt.Fprintf(&b, "\nreplace %s => %s\n", PlatformModule, o.Replace)
	}
	return b.String()
}

const mainGo = `package main

import (
	"log"

	"github.com/aidarbn/platform-go/kit/platform"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatal(err)
	}

	if err := platform.Run(platform.Config{}, platformModules(cfg), wireDomain); err != nil {
		log.Fatal(err)
	}
}
`

const wireGo = `package main

import "github.com/aidarbn/platform-go/kit/platform"

// wireDomain builds the domain part of the project: repositories, use cases, handlers.
// This is the only place edited by hand when modules change.
func wireDomain(app *platform.App) error {
	// platformgo:wire
	_ = app
	return nil
}
`

const makefile = `.PHONY: help generate build run test lint

help: ## list targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-10s %s\n", $$1, $$2}'

generate: ## regenerate module wiring
	go tool platformgo generate

build: generate ## build
	go build ./...

run: generate ## run
	go run ./cmd/app

test: ## tests
	go test -race ./...

lint: ## checks, including generation freshness
	gofmt -l .
	go vet ./...
	go tool platformgo generate --check
`

const gitignore = `/bin/
/dist/
*.test
.env
.DS_Store
`

func readme(o Options) string {
	return fmt.Sprintf(`# %s

A project built on platform-go.

## Usage

	make run      # run
	make test     # tests
	make lint     # checks, including generation freshness

## Layout

- %s lists the enabled modules and their settings; run `+"`make generate`"+` after editing it
- cmd/app/main.go and cmd/app/wire.go are the only hand written files
- cmd/app/*.gen.go and .env.example are generated, do not edit them
`, o.Service, spec.FileName)
}

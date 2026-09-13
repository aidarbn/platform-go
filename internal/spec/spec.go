// Package spec reads and validates platformgo.yaml, the description of a project.
package spec

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/aidarbn/platform-go/internal/registry"
)

// FileName is the description file in the project root.
const FileName = "platformgo.yaml"

// SchemaVersion is the file format this platformgo understands.
const SchemaVersion = 1

// File is the content of platformgo.yaml.
type File struct {
	Schema   int                       `yaml:"schema"`
	Platform string                    `yaml:"platform"`
	Project  Project                   `yaml:"project"`
	Modules  map[string]map[string]any `yaml:"modules"`
}

// Project describes the project itself.
type Project struct {
	Module  string `yaml:"module"`  // Go module path
	Service string `yaml:"service"` // service name in logs
	Go      string `yaml:"go"`      // Go version
}

// Load reads the description file and validates it.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	return Parse(raw)
}

// Parse reads the description from bytes.
func Parse(raw []byte) (*File, error) {
	var f File
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // a typo in a field name is an error, not a silently ignored key
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: parse: %w", FileName, err)
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

func (f *File) validate() error {
	var errs []error

	switch {
	case f.Schema == 0:
		errs = append(errs, errors.New("schema field is missing"))
	case f.Schema > SchemaVersion:
		errs = append(errs, fmt.Errorf("schema %d is newer than this platformgo understands (%d): upgrade platformgo", f.Schema, SchemaVersion))
	}
	if f.Project.Module == "" {
		errs = append(errs, errors.New("project.module is missing"))
	}

	for _, name := range f.moduleNames() {
		m, ok := registry.Get(name)
		if !ok {
			errs = append(errs, fmt.Errorf("unknown module %q, known modules: %s", name, strings.Join(registry.Names(), ", ")))
			continue
		}
		for _, dep := range m.Requires {
			if _, enabled := f.Modules[dep]; !enabled {
				errs = append(errs, fmt.Errorf("module %s requires module %s: add it to modules", name, dep))
			}
		}
		errs = append(errs, checkOptions(m, f.Modules[name])...)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s: %w", FileName, errors.Join(errs...))
	}
	return nil
}

// checkOptions rejects an option the module does not have: a typo in platformgo.yaml
// must fail rather than be ignored.
func checkOptions(m registry.Module, options map[string]any) []error {
	var errs []error
	for _, key := range sortedKeys(options) {
		if _, ok := m.Option(key); !ok {
			known := "none"
			if len(m.Options) > 0 {
				names := make([]string, 0, len(m.Options))
				for _, o := range m.Options {
					names = append(names, o.Name)
				}
				known = strings.Join(names, ", ")
			}
			errs = append(errs, fmt.Errorf("module %s: unknown option %q, known options: %s", m.Name, key, known))
			continue
		}
		if _, isString := options[key].(string); !isString {
			errs = append(errs, fmt.Errorf("module %s: option %s must be a string", m.Name, key))
		}
	}
	return errs
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (f *File) moduleNames() []string {
	out := make([]string, 0, len(f.Modules))
	for name := range f.Modules {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// Service returns the service name: from the file, or the last element of the module path.
func (f *File) Service() string {
	if f.Project.Service != "" {
		return f.Project.Service
	}
	parts := strings.Split(f.Project.Module, "/")
	return parts[len(parts)-1]
}

// EnabledModules returns modules in dependency order: dependencies come first.
// The order is stable, so generated files do not change between runs.
func (f *File) EnabledModules() []registry.Module {
	var out []registry.Module
	added := make(map[string]bool, len(f.Modules))

	var add func(name string)
	add = func(name string) {
		if added[name] {
			return
		}
		m, ok := registry.Get(name)
		if !ok {
			return
		}
		added[name] = true
		for _, dep := range m.Requires {
			if _, enabled := f.Modules[dep]; enabled {
				add(dep)
			}
		}
		out = append(out, m)
	}

	for _, name := range f.moduleNames() {
		add(name)
	}
	return out
}

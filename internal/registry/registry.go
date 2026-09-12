// Package registry is the catalogue of modules platformgo knows how to wire in.
//
// For every module it holds exactly what the generator needs: package, settings type,
// dependencies and environment variables. That is why enabling a module needs no code
// changes: the generator assembles the wiring from this description.
package registry

import (
	"fmt"
	"slices"
	"strings"
)

// EnvVar describes an environment variable of a module for .env.example.
type EnvVar struct {
	Key      string
	Example  string
	Comment  string
	Required bool
}

// Module describes a platform module.
type Module struct {
	Name     string   // section name in platformgo.yaml
	Requires []string // modules it cannot work without
	Import   string   // import path of the module package
	Package  string   // package name in code
	Field    string   // field name in the project's Config struct
	Env      []EnvVar
}

// ConfigType is the settings type of the module, for example postgres.Config.
func (m Module) ConfigType() string { return m.Package + ".Config" }

// LoadCall is the call that reads settings from the environment.
func (m Module) LoadCall() string { return m.Package + ".Load(l)" }

// NewCall builds the module from the project's Config struct.
func (m Module) NewCall() string { return fmt.Sprintf("%s.New(cfg.%s)", m.Package, m.Field) }

var all = []Module{
	{
		Name:    "postgres",
		Import:  "github.com/aidarbn/platform-go/kit/modules/postgres",
		Package: "postgres",
		Field:   "Postgres",
		Env: []EnvVar{
			{Key: "DATABASE_URL", Example: "postgres://app:app@localhost:5432/app?sslmode=disable", Comment: "database address", Required: true},
			{Key: "DATABASE_MAX_CONNS", Example: "10", Comment: "connection limit"},
			{Key: "DATABASE_MIN_CONNS", Example: "0", Comment: "connections kept open"},
		},
	},
}

// All returns every known module in alphabetical order.
func All() []Module {
	out := slices.Clone(all)
	slices.SortFunc(out, func(a, b Module) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Get returns a module by name.
func Get(name string) (Module, bool) {
	for _, m := range all {
		if m.Name == name {
			return m, true
		}
	}
	return Module{}, false
}

// Names returns the names of known modules.
func Names() []string {
	out := make([]string, 0, len(all))
	for _, m := range All() {
		out = append(out, m.Name)
	}
	return out
}

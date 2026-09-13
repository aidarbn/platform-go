// Package lock reads and writes platformgo.lock: what platformgo has actually applied
// to a project.
//
// platformgo.yaml says what the project wants; the lock says what it got. The
// difference between the two is what lets apply remove the files of a module that is
// no longer declared, so taking a module out never needs manual cleanup.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FileName is the lock file in the project root.
const FileName = "platformgo.lock"

// Lock is the content of platformgo.lock.
type Lock struct {
	Platform  string   `yaml:"platform"`  // platform version the project was last applied with
	Modules   []string `yaml:"modules"`   // enabled modules
	Generated []string `yaml:"generated"` // files owned by generation
}

const header = "# Written by platformgo apply. Do not edit: it records what has been applied.\n"

// Read loads the lock of a project. A project without a lock has an empty one: it has
// never been applied, which is what a project created before locks looks like.
func Read(dir string) (Lock, error) {
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return Lock{}, nil
	}
	if err != nil {
		return Lock{}, fmt.Errorf("%s: %w", FileName, err)
	}

	var l Lock
	if err := yaml.Unmarshal(raw, &l); err != nil {
		return Lock{}, fmt.Errorf("%s: parse: %w", FileName, err)
	}
	return l.normalized(), nil
}

// Write stores the lock. The content is sorted, so the file only changes when what has
// been applied changes.
func Write(dir string, l Lock) error {
	raw, err := Marshal(l)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, FileName), raw, 0o644)
}

// Marshal renders the lock as it is stored.
func Marshal(l Lock) ([]byte, error) {
	body, err := yaml.Marshal(l.normalized())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	return []byte(header + string(body)), nil
}

func (l Lock) normalized() Lock {
	out := Lock{Platform: l.Platform, Modules: uniqSorted(l.Modules), Generated: uniqSorted(l.Generated)}
	if out.Modules == nil {
		out.Modules = []string{}
	}
	if out.Generated == nil {
		out.Generated = []string{}
	}
	return out
}

func uniqSorted(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

// Diff is what changes between two locks.
type Diff struct {
	AddedModules   []string
	RemovedModules []string
	StaleFiles     []string // generated before and no longer generated
}

// Compare returns the difference between the applied lock and the wanted one.
func Compare(applied, wanted Lock) Diff {
	a, w := applied.normalized(), wanted.normalized()
	return Diff{
		AddedModules:   minus(w.Modules, a.Modules),
		RemovedModules: minus(a.Modules, w.Modules),
		StaleFiles:     minus(a.Generated, w.Generated),
	}
}

// Empty reports whether nothing changes.
func (d Diff) Empty() bool {
	return len(d.AddedModules) == 0 && len(d.RemovedModules) == 0 && len(d.StaleFiles) == 0
}

func minus(from, remove []string) []string {
	var out []string
	for _, v := range from {
		if !slices.Contains(remove, v) {
			out = append(out, v)
		}
	}
	return out
}

// SafePath reports whether a recorded path stays inside the project: the lock is a file
// in the repository, and a path like ../../etc must never be deleted because of it.
func SafePath(path string) bool {
	if path == "" || filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	return clean != "." && !strings.HasPrefix(clean, ".."+string(filepath.Separator)) && clean != ".."
}

// Package apply brings a project in line with its platformgo.yaml.
//
// A plan compares three things: what generation produces now, what the project has on
// disk, and what platformgo.lock says was applied before. From that it knows which files
// to write, which files a newly enabled module needs, and which generated files belong to
// a module that is gone. That last part is what makes removing a module a one line edit.
package apply

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing/fstest"

	"github.com/aidarbn/platform-go/internal/codemod"
	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/gomod"
	"github.com/aidarbn/platform-go/internal/lock"
	"github.com/aidarbn/platform-go/internal/registry"
	"github.com/aidarbn/platform-go/internal/spec"
	"github.com/aidarbn/platform-go/internal/version"
)

// Plan is what apply would do to a project.
type Plan struct {
	Service string
	Modules []string
	Diff    lock.Diff

	Create    []string         // files a module needs and the project does not have yet
	Write     []string         // generated files that differ from the project
	Delete    []string         // generated files of removed modules
	Kept      []string         // generated files of removed modules that were edited by hand
	AddTools  []registry.Tool  // tools of enabled modules missing from go.mod
	DropTools []string         // tools of removed modules still declared in go.mod
	LockStale bool             // platformgo.lock does not match
	Codemods  []codemod.Change // project files rewritten for changes of kit

	dir     string
	creates map[string][]byte
	files   map[string][]byte
	wanted  lock.Lock
}

// codemods are the rewrites of the platform; tests replace them.
var codemods = codemod.All

// Build reads the project and computes the plan without changing anything.
func Build(dir string) (*Plan, error) {
	f, err := spec.Load(filepath.Join(dir, spec.FileName))
	if err != nil {
		return nil, err
	}
	applied, err := lock.Read(dir)
	if err != nil {
		return nil, err
	}

	creates, err := ownedFiles(dir, f)
	if err != nil {
		return nil, err
	}

	files, err := gen.FilesFrom(f, overlay{base: os.DirFS(dir), extra: creates})
	if err != nil {
		return nil, err
	}

	write, err := gen.Changed(dir, files)
	if err != nil {
		return nil, err
	}

	p := &Plan{
		Service: f.Service(),
		Write:   write,
		dir:     dir,
		creates: creates,
		files:   files,
		Create:  slices.Sorted(maps.Keys(creates)),
	}
	var tools []registry.Tool
	for _, m := range f.EnabledModules() {
		p.Modules = append(p.Modules, m.Name)
		tools = append(tools, m.Tools...)
	}
	slices.Sort(p.Modules)

	// Codemods newer than the version that applied the project last.
	if p.Codemods, err = codemod.Run(dir, codemod.Pending(codemods(), applied.Platform)); err != nil {
		return nil, err
	}

	p.wanted = lock.Lock{Platform: version.Platform, Modules: p.Modules, Generated: slices.Sorted(maps.Keys(files))}
	for _, t := range tools {
		p.wanted.Tools = append(p.wanted.Tools, t.Package)
	}
	p.Diff = lock.Compare(applied, p.wanted)

	if err := p.classifyStale(); err != nil {
		return nil, err
	}
	if err := p.planTools(tools); err != nil {
		return nil, err
	}
	if p.LockStale, err = lockStale(dir, p.wanted); err != nil {
		return nil, err
	}
	return p, nil
}

// ownedFiles are the files an enabled module needs to exist. They are created once and
// belong to the project afterwards, so an existing one is never touched.
func ownedFiles(dir string, f *spec.File) (map[string][]byte, error) {
	out := map[string][]byte{}
	if _, ok := f.Modules["i18n"]; ok {
		missing, err := notExists(filepath.Join(dir, gen.MessagesPath))
		if err != nil {
			return nil, err
		}
		if missing {
			out[gen.MessagesPath] = []byte(gen.MessagesExample)
		}
	}
	if _, ok := f.Modules["monitoring"]; ok {
		missing, err := notExists(filepath.Join(dir, gen.MonitoringPath))
		if err != nil {
			return nil, err
		}
		if missing {
			out[gen.MonitoringPath] = []byte(gen.MonitoringExample)
		}
		missing, err = notExists(filepath.Join(dir, gen.MonitoringRulesPath))
		if err != nil {
			return nil, err
		}
		if missing {
			out[gen.MonitoringRulesPath] = []byte(gen.MonitoringRulesExample)
		}
	}
	if _, ok := f.Modules["api"]; ok {
		missing, err := notExists(filepath.Join(dir, gen.OpenAPIBasePath))
		if err != nil {
			return nil, err
		}
		if missing {
			out[gen.OpenAPIBasePath] = []byte(strings.ReplaceAll(gen.OpenAPIBaseExample, "{{service}}", f.Project.Service))
		}
	}
	if _, ok := f.Modules["rbac"]; ok {
		missing, err := notExists(filepath.Join(dir, gen.PolicyPath))
		if err != nil {
			return nil, err
		}
		if missing {
			out[gen.PolicyPath] = []byte(gen.PolicyExample)
		}
	}
	if gen.SettingsEnabled(f) {
		path := gen.SettingsSchemaPath(f)
		if !lock.SafePath(path) {
			return nil, fmt.Errorf("module settings: schema path %q must stay inside the project", path)
		}
		missing, err := notExists(filepath.Join(dir, path))
		if err != nil {
			return nil, err
		}
		if missing {
			out[path] = []byte(gen.SettingsExample)
		}
	}
	return out, nil
}

// classifyStale splits the generated files of removed modules into the ones to delete
// and the ones somebody has taken over by hand.
func (p *Plan) classifyStale() error {
	for _, path := range p.Diff.StaleFiles {
		if !lock.SafePath(path) {
			p.Kept = append(p.Kept, path)
			continue
		}
		content, err := os.ReadFile(filepath.Join(p.dir, path))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if gen.IsGenerated(content) {
			p.Delete = append(p.Delete, path)
		} else {
			p.Kept = append(p.Kept, path)
		}
	}
	return nil
}

// planTools compares the tools of the enabled modules with go.mod. Only tools platformgo
// added itself, according to the lock, are ever removed.
func (p *Plan) planTools(tools []registry.Tool) error {
	if !gomod.Exists(p.dir) {
		return nil
	}
	mod, err := gomod.Read(p.dir)
	if err != nil {
		return err
	}
	for _, t := range tools {
		if !mod.HasTool(t.Package) {
			p.AddTools = append(p.AddTools, t)
		}
	}
	for _, pkg := range p.Diff.StaleTools {
		if mod.HasTool(pkg) {
			p.DropTools = append(p.DropTools, pkg)
		}
	}
	return nil
}

func lockStale(dir string, wanted lock.Lock) (bool, error) {
	want, err := lock.Marshal(wanted)
	if err != nil {
		return false, err
	}
	got, err := os.ReadFile(filepath.Join(dir, lock.FileName))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("%s: %w", lock.FileName, err)
	}
	return !bytes.Equal(got, want), nil
}

// UpToDate reports whether the project already matches its description.
func (p *Plan) UpToDate() bool {
	return len(p.Create) == 0 && len(p.Write) == 0 && len(p.Delete) == 0 && len(p.Codemods) == 0 &&
		len(p.AddTools) == 0 && len(p.DropTools) == 0 && !p.LockStale
}

// Pending lists every path apply would touch, for messages.
func (p *Plan) Pending() []string {
	out := slices.Concat(p.Create, p.Write, p.Delete)
	for _, c := range p.Codemods {
		out = append(out, c.Path)
	}
	if len(p.AddTools) > 0 || len(p.DropTools) > 0 {
		out = append(out, "go.mod")
	}
	if p.LockStale {
		out = append(out, lock.FileName)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Execute carries out the plan: creates, writes, deletes and records the lock.
func (p *Plan) Execute() error {
	for _, c := range p.Codemods {
		if err := os.WriteFile(filepath.Join(p.dir, c.Path), c.Content, 0o644); err != nil {
			return fmt.Errorf("codemod: %w", err)
		}
	}
	if _, err := gen.Apply(p.dir, p.creates); err != nil {
		return err
	}
	if _, err := gen.Apply(p.dir, p.files); err != nil {
		return err
	}
	for _, path := range p.Delete {
		full := filepath.Join(p.dir, path)
		if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s: %w", path, err)
		}
		removeEmptyParents(p.dir, filepath.Dir(full))
	}
	for _, t := range p.AddTools {
		if err := gomod.AddTool(p.dir, t.Module, t.Package, t.Version); err != nil {
			return err
		}
	}
	for _, pkg := range p.DropTools {
		if err := gomod.DropTool(p.dir, pkg); err != nil {
			return err
		}
	}
	if p.LockStale {
		if err := lock.Write(p.dir, p.wanted); err != nil {
			return err
		}
	}
	return nil
}

// removeEmptyParents removes directories left empty by a deletion, up to the project
// root: a removed module should not leave empty folders behind.
func removeEmptyParents(root, dir string) {
	root = filepath.Clean(root)
	for {
		dir = filepath.Clean(dir)
		if dir == root || !strings.HasPrefix(dir, root+string(filepath.Separator)) {
			return
		}
		if err := os.Remove(dir); err != nil {
			return // not empty, or not removable: stop here
		}
		dir = filepath.Dir(dir)
	}
}

// overlay reads the files apply is about to create before the disk.
type overlay struct {
	base  fs.FS
	extra map[string][]byte
}

func (o overlay) Open(name string) (fs.File, error) {
	if content, ok := o.extra[name]; ok {
		return fstest.MapFS{name: {Data: content}}.Open(name)
	}
	return o.base.Open(name)
}

func notExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, err
}

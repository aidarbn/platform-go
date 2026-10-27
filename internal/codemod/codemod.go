// Package codemod rewrites project code when an incompatible change of kit arrives.
//
// A codemod belongs to the platform version that brings the change. apply runs the ones
// newer than the version the project was last applied with, which platformgo.lock
// records, so an upgrade over several versions replays every step in order. A codemod
// must be idempotent: code that has nothing left to rewrite stays as it is.
//
// What a machine cannot rewrite goes to a person as a note: File.Report. Notes of Fix
// are printed by plan and apply once, when the codemod runs. Check finds code that can
// no longer compile with the version, such as a reference to a removed name, and runs on
// every plan and verify, so the note stays open until somebody fixes the code.
package codemod

import (
	"bytes"
	"cmp"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// Codemod is one rewrite.
type Codemod struct {
	Version     string // the platform version that brings the change
	Name        string
	Description string
	// Covers are the incompatible changes of kit the codemod answers for, as apidiff names
	// them relative to the module: "kit/adminx.(*Auth).Login". scripts/apicheck.sh accepts
	// an incompatible change covered here or in the manual steps of docs/upgrades.md.
	Covers []string

	Fix   func(f *File)      // rewrites one Go file through File.Replace; runs while pending
	Check func(f *File)      // reports code that cannot compile with Version; runs every time
	Proto func(f *ProtoFile) // rewrites one .proto file; runs while pending
	// Skip leaves the codemod out for a project it does not concern, by its settings.
	Skip func(p Project) bool
}

// Project is what a codemod may ask about the settings of a project.
type Project struct {
	OpenAPIGenerator string // the openapi.generator of the api module, empty without it
}

// Applicable leaves out the codemods that skip a project.
func Applicable(mods []Codemod, p Project) []Codemod {
	var out []Codemod
	for _, m := range mods {
		if m.Skip == nil || !m.Skip(p) {
			out = append(out, m)
		}
	}
	return out
}

// Change is a file a run rewrote.
type Change struct {
	Path     string   // relative to the project root
	Codemods []string // names of the codemods that changed it
	Content  []byte
}

// Note is a place a person has to change: what a codemod could not rewrite.
type Note struct {
	Path    string // relative to the project root
	Line    int
	Codemod string
	Message string
}

func (n Note) String() string {
	return fmt.Sprintf("%s:%d: %s (%s)", n.Path, n.Line, n.Message, n.Codemod)
}

// Result is what a run would do.
type Result struct {
	Changes []Change
	Notes   []Note
}

// Pending returns the codemods newer than the version the project was applied with, in
// version order. An empty or invalid version means before codemods existed: all of them.
func Pending(all []Codemod, applied string) []Codemod {
	var out []Codemod
	for _, c := range all {
		if !semver.IsValid(applied) || semver.Compare(c.Version, applied) > 0 {
			out = append(out, c)
		}
	}
	slices.SortStableFunc(out, func(a, b Codemod) int { return semver.Compare(a.Version, b.Version) })
	return out
}

// Run applies the pending codemods to the Go and proto files of the project without
// writing anything, then the checks of every codemod in checks to the result. It returns
// the files that change and the notes for a person.
func Run(dir string, pending, checks []Codemod) (Result, error) {
	var res Result
	if len(pending) == 0 && !slices.ContainsFunc(checks, func(c Codemod) bool { return c.Check != nil }) {
		return res, nil
	}
	files, err := projectFiles(dir)
	if err != nil {
		return res, err
	}
	imports := packageImports(dir, files)
	for _, rel := range files {
		var change *Change
		var notes []Note
		if strings.HasSuffix(rel, ".proto") {
			change, notes, err = runProto(dir, rel, pending)
		} else {
			change, notes, err = runGo(dir, rel, imports[filepath.Dir(rel)], pending, checks)
		}
		if err != nil {
			return Result{}, err
		}
		if change != nil {
			res.Changes = append(res.Changes, *change)
		}
		res.Notes = append(res.Notes, notes...)
	}
	return res, nil
}

// projectFiles lists the Go and proto files of the project, relative to dir, leaving out
// vendor, testdata and hidden folders.
func projectFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != dir && (name == "vendor" || name == "testdata" || name == "node_modules" ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".proto") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, err
}

// packageImports collects the imports of every Go folder: a method call on a field of a
// kit type sits in a file that does not import kit itself, but its package does.
func packageImports(dir string, files []string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, rel := range files {
		if !strings.HasSuffix(rel, ".go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, rel), nil, parser.ImportsOnly)
		if err != nil {
			continue
		}
		folder := filepath.Dir(rel)
		if out[folder] == nil {
			out[folder] = map[string]bool{}
		}
		for _, imp := range file.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err == nil {
				out[folder][p] = true
			}
		}
	}
	return out
}

func runGo(dir, rel string, imports map[string]bool, pending, checks []Codemod) (*Change, []Note, error) {
	path := filepath.Join(dir, rel)
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, nil, nil // code that does not parse is not ours to touch; the build reports it
	}
	if ast.IsGenerated(file) {
		return nil, nil, nil // generation rewrites it
	}

	var applied []string
	var notes []Note
	for _, m := range pending {
		if m.Fix == nil {
			continue
		}
		f := &File{Path: rel, Fset: fset, AST: file, src: src, imports: imports, codemod: m.Name}
		m.Fix(f)
		notes = append(notes, f.notes...)
		if len(f.edits) == 0 {
			continue
		}
		out, err := f.apply()
		if err != nil {
			return nil, nil, fmt.Errorf("codemod %s: %s: %w", m.Name, rel, err)
		}
		if bytes.Equal(out, src) {
			continue
		}
		applied = append(applied, m.Name)
		src = out
		fset = token.NewFileSet()
		if file, err = parser.ParseFile(fset, path, src, parser.ParseComments); err != nil {
			return nil, nil, fmt.Errorf("codemod %s: %s: the result does not parse: %w", m.Name, rel, err)
		}
	}
	for _, m := range checks {
		if m.Check == nil {
			continue
		}
		f := &File{Path: rel, Fset: fset, AST: file, src: src, imports: imports, codemod: m.Name}
		m.Check(f)
		notes = append(notes, f.notes...)
	}
	if len(applied) == 0 {
		return nil, notes, nil
	}
	return &Change{Path: rel, Codemods: applied, Content: src}, notes, nil
}

// File is a Go file a codemod rewrites. Edits replace source ranges, so comments and
// layout outside them stay as the developer wrote them.
type File struct {
	Path string
	Fset *token.FileSet
	AST  *ast.File

	src     []byte
	edits   []edit
	notes   []Note
	imports map[string]bool // of every file of the package
	codemod string
}

type edit struct {
	start, end int
	text       string
}

// Replace replaces the source of a node.
func (f *File) Replace(n ast.Node, text string) {
	f.ReplaceRange(n.Pos(), n.End(), text)
}

// ReplaceRange replaces the source between two positions; an empty text deletes it.
func (f *File) ReplaceRange(from, to token.Pos, text string) {
	f.edits = append(f.edits, edit{
		start: f.Fset.Position(from).Offset,
		end:   f.Fset.Position(to).Offset,
		text:  text,
	})
}

// Report leaves a note for a person at a node: what the codemod could not rewrite.
func (f *File) Report(n ast.Node, msg string) {
	f.notes = append(f.notes, Note{Path: f.Path, Line: f.Fset.Position(n.Pos()).Line, Codemod: f.codemod, Message: msg})
}

// Source returns the source of a node.
func (f *File) Source(n ast.Node) string {
	return string(f.src[f.Fset.Position(n.Pos()).Offset:f.Fset.Position(n.End()).Offset])
}

// ImportName returns the name a package is known by in the file, when it is imported.
func (f *File) ImportName(path string) (string, bool) {
	for _, imp := range f.AST.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name, true
		}
		return pathBase(p), true
	}
	return "", false
}

// importedAs reports whether a name is the name of a package the file imports.
func (f *File) importedAs(name string) bool {
	for _, imp := range f.AST.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if (imp.Name != nil && imp.Name.Name == name) || (imp.Name == nil && pathBase(p) == name) {
			return true
		}
	}
	return false
}

// PackageImports reports whether any file of the package of the file imports a path:
// values of a kit type can be used in a file that does not import kit itself.
func (f *File) PackageImports(path string) bool {
	if _, ok := f.ImportName(path); ok {
		return true
	}
	return f.imports[path]
}

// PackageRef reports whether an expression is name.sel for a package imported under
// name and not shadowed in the file.
func (f *File) PackageRef(e ast.Expr, pkg string) (*ast.SelectorExpr, bool) {
	name, ok := f.ImportName(pkg)
	if !ok || name == "_" || name == "." {
		return nil, false
	}
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	// A local variable shadowing the package name has no Obj-free way to tell apart
	// without type checking; package identifiers have no declaration in the file, which
	// is what Obj == nil says.
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != name || id.Obj != nil {
		return nil, false
	}
	return sel, true
}

func (f *File) apply() ([]byte, error) {
	out, err := applyEdits(f.src, f.edits)
	if err != nil {
		return nil, err
	}
	return format.Source(out)
}

func applyEdits(src []byte, edits []edit) ([]byte, error) {
	edits = slices.Clone(edits)
	slices.SortFunc(edits, func(a, b edit) int { return cmp.Compare(a.start, b.start) })
	var b bytes.Buffer
	last := 0
	for _, e := range edits {
		if e.start < last {
			return nil, fmt.Errorf("overlapping edits at offset %d", e.start)
		}
		b.Write(src[last:e.start])
		b.WriteString(e.text)
		last = e.end
	}
	b.Write(src[last:])
	return b.Bytes(), nil
}

// pathBase is the package name a path gets by default: the last element without a
// major version suffix.
func pathBase(path string) string {
	parts := strings.Split(path, "/")
	last := parts[len(parts)-1]
	if len(parts) > 1 && len(last) > 1 && last[0] == 'v' && strings.Trim(last[1:], "0123456789") == "" {
		last = parts[len(parts)-2]
	}
	return strings.ReplaceAll(last, "-", "_")
}

// RenameSymbol is the common codemod: an exported name of a kit package changes.
func RenameSymbol(version, pkg, from, to string) Codemod {
	return Codemod{
		Version:     version,
		Name:        pathBase(pkg) + "." + from + "→" + to,
		Description: fmt.Sprintf("%s.%s is renamed to %s", pkg, from, to),
		Covers:      []string{strings.TrimPrefix(pkg, Module+"/") + "." + from},
		Fix: func(f *File) {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if sel, ok := f.PackageRef(asExpr(n), pkg); ok && sel.Sel.Name == from {
					f.Replace(sel.Sel, to)
				}
				return true
			})
		},
	}
}

// Module is the Go module of the platform.
const Module = "github.com/aidarbn/platform-go"

func asExpr(n ast.Node) ast.Expr {
	e, _ := n.(ast.Expr)
	return e
}

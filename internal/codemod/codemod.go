// Package codemod rewrites project code when an incompatible change of kit arrives.
//
// A codemod belongs to the platform version that brings the change. apply runs the ones
// newer than the version the project was last applied with, which platformgo.lock
// records, so an upgrade over several versions replays every step in order. A codemod
// must be idempotent: code that has nothing left to rewrite stays as it is.
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
	Fix         func(f *File) // rewrites one Go file through File.Replace
}

// Change is a file a run rewrote.
type Change struct {
	Path     string   // relative to the project root
	Codemods []string // names of the codemods that changed it
	Content  []byte
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

// Run applies the codemods to the Go files of the project without writing anything and
// returns the files that change.
func Run(dir string, mods []Codemod) ([]Change, error) {
	if len(mods) == 0 {
		return nil, nil
	}
	var changes []Change
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != dir && (name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		change, err := runFile(path, filepath.ToSlash(rel), mods)
		if err != nil {
			return err
		}
		if change != nil {
			changes = append(changes, *change)
		}
		return nil
	})
	return changes, err
}

func runFile(path, rel string, mods []Codemod) (*Change, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, nil // code that does not parse is not ours to touch; the build reports it
	}
	if ast.IsGenerated(file) {
		return nil, nil // generation rewrites it
	}

	var applied []string
	for _, m := range mods {
		f := &File{Path: rel, Fset: fset, AST: file, src: src}
		m.Fix(f)
		if len(f.edits) == 0 {
			continue
		}
		out, err := f.apply()
		if err != nil {
			return nil, fmt.Errorf("codemod %s: %s: %w", m.Name, rel, err)
		}
		if bytes.Equal(out, src) {
			continue
		}
		applied = append(applied, m.Name)
		src = out
		fset = token.NewFileSet()
		if file, err = parser.ParseFile(fset, path, src, parser.ParseComments); err != nil {
			return nil, fmt.Errorf("codemod %s: %s: the result does not parse: %w", m.Name, rel, err)
		}
	}
	if len(applied) == 0 {
		return nil, nil
	}
	return &Change{Path: rel, Codemods: applied, Content: src}, nil
}

// File is a Go file a codemod rewrites. Edits replace source ranges, so comments and
// layout outside them stay as the developer wrote them.
type File struct {
	Path string
	Fset *token.FileSet
	AST  *ast.File

	src   []byte
	edits []edit
}

type edit struct {
	start, end int
	text       string
}

// Replace replaces the source of a node.
func (f *File) Replace(n ast.Node, text string) {
	f.edits = append(f.edits, edit{
		start: f.Fset.Position(n.Pos()).Offset,
		end:   f.Fset.Position(n.End()).Offset,
		text:  text,
	})
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

func (f *File) apply() ([]byte, error) {
	edits := slices.Clone(f.edits)
	slices.SortFunc(edits, func(a, b edit) int { return cmp.Compare(a.start, b.start) })
	var b bytes.Buffer
	last := 0
	for _, e := range edits {
		if e.start < last {
			return nil, fmt.Errorf("overlapping edits at offset %d", e.start)
		}
		b.Write(f.src[last:e.start])
		b.WriteString(e.text)
		last = e.end
	}
	b.Write(f.src[last:])
	return format.Source(b.Bytes())
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
		Fix: func(f *File) {
			name, ok := f.ImportName(pkg)
			if !ok || name == "_" || name == "." {
				return
			}
			ast.Inspect(f.AST, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != from {
					return true
				}
				// A local variable shadowing the package name has no Obj-free way to tell
				// apart without type checking; package identifiers have no declaration
				// in the file, which is what Obj == nil says.
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == name && id.Obj == nil {
					f.Replace(sel.Sel, to)
				}
				return true
			})
		},
	}
}

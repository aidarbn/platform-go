// Package gomod reads and edits the go.mod of a project through the go command.
//
// go mod edit works offline and keeps the file formatted the way go writes it, which is
// why it is used instead of editing the text.
package gomod

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// File is the part of go.mod platformgo cares about.
type File struct {
	Module   string
	Go       string            // the go directive
	Requires map[string]string // module path to version
	Replaced []string          // module paths with a replace directive
	Tools    []string          // packages declared with tool
}

// Exists reports whether the project has a go.mod.
func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil
}

// Read loads go.mod of the project.
func Read(dir string) (File, error) {
	out, err := run(dir, "mod", "edit", "-json")
	if err != nil {
		return File{}, err
	}

	var raw struct {
		Module  struct{ Path string }
		Go      string
		Require []struct{ Path, Version string }
		Replace []struct{ Old struct{ Path string } }
		Tool    []struct{ Path string }
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return File{}, fmt.Errorf("go.mod: %w", err)
	}

	f := File{Module: raw.Module.Path, Go: raw.Go, Requires: make(map[string]string, len(raw.Require))}
	for _, r := range raw.Replace {
		f.Replaced = append(f.Replaced, r.Old.Path)
	}
	for _, r := range raw.Require {
		f.Requires[r.Path] = r.Version
	}
	for _, t := range raw.Tool {
		f.Tools = append(f.Tools, t.Path)
	}
	slices.Sort(f.Tools)
	return f, nil
}

// HasTool reports whether a package is declared as a tool.
func (f File) HasTool(pkg string) bool { return slices.Contains(f.Tools, pkg) }

// AddTool declares a tool. The module is required at the given version unless the
// project already requires it: a version the project chose is kept.
func AddTool(dir, module, pkg, version string) error {
	f, err := Read(dir)
	if err != nil {
		return err
	}
	args := []string{"mod", "edit", "-tool=" + pkg}
	if _, ok := f.Requires[module]; !ok {
		args = append(args, "-require="+module+"@"+version)
	}
	_, err = run(dir, args...)
	return err
}

// DropTool removes a tool declaration. The require line stays until go mod tidy finds
// it unused, which keeps a module the project imports itself.
func DropTool(dir, pkg string) error {
	_, err := run(dir, "mod", "edit", "-droptool="+pkg)
	return err
}

func run(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("go %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

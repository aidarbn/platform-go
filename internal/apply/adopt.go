package apply

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/lock"
)

// AdoptReport is the report apply --adopt leaves for a person: the conflicts and what
// apply did not do yet. It is an input for the work of moving the project, not
// documentation; delete it when the work is done.
const AdoptReport = "platformgo.adopt.md"

// Adopt carries out the safe part of the plan for a project that has files of its own,
// such as a service moving to the platform. It creates the files that do not exist yet
// and the conflicts the plan forces. Nothing else that exists is changed: writes to
// existing files, codemods, deletions and go.mod are left for apply once the conflicts
// are resolved, and the report lists them.
//
// The lock records only what adopt did: the files it wrote join the generated files
// of the old lock, and the platform version, modules and tools stay as they were. So a
// later apply still sees the codemods, deletions and tools as pending, and still
// refuses the conflicts, which the lock does not claim.
func (p *Plan) Adopt() (created []string, report bool, err error) {
	for _, path := range p.Create {
		if err := writeNew(p.dir, path, p.creates[path]); err != nil {
			return nil, false, err
		}
		created = append(created, path)
	}
	for _, path := range p.Write {
		missing, err := notExists(filepath.Join(p.dir, path))
		if err != nil {
			return nil, false, err
		}
		switch {
		case missing:
			if err := writeNew(p.dir, path, p.files[path]); err != nil {
				return nil, false, err
			}
		case slices.Contains(p.forced, path):
			if err := os.WriteFile(filepath.Join(p.dir, path), p.files[path], 0o644); err != nil {
				return nil, false, fmt.Errorf("%s: %w", path, err)
			}
		default:
			continue
		}
		created = append(created, path)
	}
	slices.Sort(created)

	adopted := p.applied
	adopted.Generated = slices.Clone(p.applied.Generated)
	for _, path := range created {
		if _, generated := p.files[path]; generated {
			adopted.Generated = append(adopted.Generated, path)
		}
	}
	if err := lock.Write(p.dir, adopted); err != nil {
		return nil, false, err
	}

	text := p.adoptReport(created)
	full := filepath.Join(p.dir, AdoptReport)
	if text == "" {
		// Nothing is left: a report of an earlier adopt would only mislead.
		if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, false, fmt.Errorf("%s: %w", AdoptReport, err)
		}
		return created, false, nil
	}
	if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
		return nil, false, fmt.Errorf("%s: %w", AdoptReport, err)
	}
	return created, true, nil
}

func writeNew(dir, rel string, content []byte) error {
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil // appeared in the meantime: it is the project's now
	}
	if err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return fmt.Errorf("%s: %w", rel, err)
	}
	return f.Close()
}

// conflictHints say where the content of a file of the project goes on the platform.
var conflictHints = map[string]string{
	gen.ComposePath: "Services the platform does not run go into `docker-compose.override.yml`, which docker compose merges automatically.",
	gen.EnvPath:     "Variables of the project's own code go into `.env.project.example`; apply appends it to `.env.example`.",
	gen.BufPath:     "The proto folder, extra dependencies and breaking exceptions are options of the api module: `proto_dir`, `buf.deps`, `buf.breaking_except`.",
	gen.BufGenPath:  "Plugins and outputs are the platform's; the OpenAPI generator is the api option `openapi.generator` (`connect` or `none`).",
}

func conflictHint(p string) string {
	if h, ok := conflictHints[p]; ok {
		return h
	}
	if path.Base(p) == "sqlc.yaml" {
		return "Paths, overrides and renames are the postgres option `sqlc`: `dir`, `queries`, `out`, `emit_interface`, `overrides`, `renames`."
	}
	return ""
}

// adoptReport is the text of the report, or "" when nothing is left to a person.
func (p *Plan) adoptReport(created []string) string {
	var left []string
	for _, path := range p.Write {
		if !slices.Contains(created, path) {
			left = append(left, "write `"+path+"`: generated, differs from the project")
		}
	}
	for _, path := range p.Delete {
		left = append(left, "delete `"+path+"`: generated for a removed module")
	}
	for _, c := range p.Codemods {
		left = append(left, "rewrite `"+c.Path+"`: codemod "+strings.Join(c.Codemods, ", "))
	}
	for _, t := range p.AddTools {
		left = append(left, "add the tool `"+t.Package+"@"+t.Version+"` to go.mod")
	}
	for _, pkg := range p.DropTools {
		left = append(left, "remove the tool `"+pkg+"` from go.mod")
	}
	if len(p.Conflicts) == 0 && len(left) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("# platformgo adopt report\n\n")
	b.WriteString("Written by `platformgo apply --adopt`. It is a work list for moving this project to the platform, not documentation: delete it when the work is done.\n\n")
	b.WriteString("apply created only the files that did not exist, the ones forced with `--force`, and `" + lock.FileName + "`; nothing else that existed was changed.\n")
	if len(p.Conflicts) > 0 {
		b.WriteString("\n## Conflicts\n\n")
		b.WriteString("platformgo generates these files, and the project has them in a version of its own: without the platformgo mark and not in `" + lock.FileName + "`. apply does not overwrite them. For each, carry over what the project needs, then take the generated version with `platformgo apply --force <path>` (or delete the file and run `platformgo apply`). The diff goes from the project's file to the generated one.\n")
		for _, c := range p.Conflicts {
			b.WriteString("\n### " + c.Path + "\n\n")
			if h := conflictHint(c.Path); h != "" {
				b.WriteString(h + "\n\n")
			}
			diff := UnifiedDiff(c.Path+" (project)", c.Path+" (platformgo)", c.Disk, c.Generated)
			fence := "```"
			for strings.Contains(diff, fence) {
				fence += "`"
			}
			b.WriteString(fence + "diff\n" + diff + fence + "\n")
		}
	}
	if len(left) > 0 {
		b.WriteString("\n## Left for platformgo apply\n\n")
		b.WriteString("Changes to what exists, made by `platformgo apply` once the conflicts are resolved:\n\n")
		for _, l := range left {
			b.WriteString("- " + l + "\n")
		}
	}
	return b.String()
}

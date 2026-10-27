// Package apicover holds the version rule of the platform before v1: a minor version may
// break kit, but every incompatible change must come with a codemod that rewrites or
// reports it, or with a manual step under its version in docs/upgrades.md.
package apicover

import (
	"regexp"
	"slices"
	"strings"

	"github.com/aidarbn/platform-go/internal/codemod"
	"golang.org/x/mod/semver"
)

// Incompatible returns the incompatible changes of an apidiff report, as names relative
// to the module: "kit/adminx.(*Auth).Login".
func Incompatible(report, module string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(report, "\n") {
		switch {
		case strings.HasPrefix(line, "Incompatible changes:"):
			in = true
		case !in:
		case strings.HasPrefix(line, "- "):
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "- "), ": ")
			out = append(out, strings.TrimPrefix(name, module+"/"))
		default:
			in = false
		}
	}
	return out
}

var (
	heading  = regexp.MustCompile(`^(#+)\s+(v\d+\.\d+\.\d+)\b`)
	anyHead  = regexp.MustCompile(`^(#+)\s`)
	backtick = regexp.MustCompile("`([^`]+)`")
)

// Manual returns the names in backticks under every version heading of the manual
// steps, by version.
func Manual(docs string) map[string][]string {
	out := map[string][]string{}
	version, level := "", 0
	fence := false
	for _, line := range strings.Split(docs, "\n") {
		if strings.HasPrefix(line, "```") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		if m := heading.FindStringSubmatch(line); m != nil {
			version, level = m[2], len(m[1])
			continue
		}
		if m := anyHead.FindStringSubmatch(line); m != nil && len(m[1]) <= level {
			version, level = "", 0
			continue
		}
		if version == "" {
			continue
		}
		for _, m := range backtick.FindAllStringSubmatch(line, -1) {
			out[version] = append(out[version], m[1])
		}
	}
	return out
}

// Uncovered returns the incompatible changes no codemod and no manual step answers for.
// Only codemods and manual steps of the versions after base and up to release count; an
// empty release means every version after base.
func Uncovered(changes []string, mods []codemod.Codemod, manual map[string][]string, base, release string) []string {
	inRange := func(v string) bool {
		return semver.IsValid(v) && semver.Compare(v, base) > 0 && (release == "" || semver.Compare(v, release) <= 0)
	}
	covered := map[string]bool{}
	for _, m := range mods {
		if inRange(m.Version) {
			for _, name := range m.Covers {
				covered[name] = true
			}
		}
	}
	for v, names := range manual {
		if inRange(v) {
			for _, name := range names {
				covered[name] = true
			}
		}
	}
	var out []string
	for _, c := range changes {
		if !covered[c] {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

package openapi

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DocumentSpec is a description for one audience — the tablet, the public, the admin
// panel — cut out of the whole one: api.openapi.documents in platformgo.yaml. The
// generator writes it as openapi.<name>.bundle.yaml, with only the operations it selects
// and only the components they use; the api module serves it at /openapi/<name>.yaml.
type DocumentSpec struct {
	Name        string   `yaml:"name"`
	Title       string   `yaml:"title"`       // info.title; the base's when empty
	Description string   `yaml:"description"` // info.description; the base's when empty
	Include     Selector `yaml:"include"`     // what goes in; everything when empty
	Exclude     Selector `yaml:"exclude"`     // what stays out of what goes in
}

// Selector picks operations: one matches when its tag is listed, its tag is in a listed
// group of x-tagGroups, or the gRPC service of its method is listed by full name.
type Selector struct {
	TagGroups []string `yaml:"tag_groups"`
	Tags      []string `yaml:"tags"`
	Services  []string `yaml:"services"`
}

func (s Selector) empty() bool {
	return len(s.TagGroups) == 0 && len(s.Tags) == 0 && len(s.Services) == 0
}

// AllDocuments names the whole description among the documents: the api module's
// API_DOCS_DOCUMENTS lists it to serve /openapi.yaml, so no document may take the name.
const AllDocuments = "all"

// DocumentFile is the bundle of a document, relative to the output folder.
func DocumentFile(name string) string { return "openapi." + name + ".bundle.yaml" }

// documentName is a name that is a file name, a URL segment and an item of a comma
// separated list alike.
var documentName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// CheckDocuments finds what is wrong with the documents themselves, before any proto
// file is read: names, duplicates, empty entries.
func CheckDocuments(docs []DocumentSpec) error {
	var errs []error
	seen := map[string]bool{}
	for i, d := range docs {
		switch {
		case !documentName.MatchString(d.Name):
			errs = append(errs, fmt.Errorf("documents[%d]: name %q must be lower case letters, digits, - and _, starting with a letter or a digit", i, d.Name))
		case d.Name == AllDocuments:
			errs = append(errs, fmt.Errorf("documents[%d]: name %q is the whole description", i, d.Name))
		case seen[d.Name]:
			errs = append(errs, fmt.Errorf("documents[%d]: name %q is used twice", i, d.Name))
		}
		seen[d.Name] = true
		for _, sel := range []struct {
			key string
			s   Selector
		}{{"include", d.Include}, {"exclude", d.Exclude}} {
			for _, list := range [][]string{sel.s.TagGroups, sel.s.Tags, sel.s.Services} {
				for _, v := range list {
					if strings.TrimSpace(v) == "" {
						errs = append(errs, fmt.Errorf("documents[%d] %s: an empty name", i, sel.key))
					}
				}
			}
		}
	}
	return errors.Join(errs...)
}

// ReadDocuments reads api.openapi.documents of platformgo.yaml: the plugin gets the path
// of the file, documents=platformgo.yaml, and buf runs it in the project folder.
// platformgo checks the file against its schema; here only the documents matter.
func ReadDocuments(file string) ([]DocumentSpec, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("documents: %w", err)
	}
	var cfg struct {
		Modules struct {
			API struct {
				OpenAPI struct {
					Documents []DocumentSpec `yaml:"documents"`
				} `yaml:"openapi"`
			} `yaml:"api"`
		} `yaml:"modules"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("documents: %s: %w", file, err)
	}
	docs := cfg.Modules.API.OpenAPI.Documents
	if err := CheckDocuments(docs); err != nil {
		return nil, fmt.Errorf("%s: api.openapi: %w", file, err)
	}
	return docs, nil
}

// document cuts a document out of the whole description, a fresh merge of the base and
// the generated document: the selected operations, the paths, tags and groups that
// still have one, and the components they use. services maps an operation id to the
// full name of the gRPC service of its method.
func document(root *yaml.Node, d DocumentSpec, services map[string]string) error {
	// An anchor of the base may be cut away while an alias of it stays, and what an
	// alias holds would be missed by the walks: every alias becomes its value.
	unalias(root)
	groupsOf := map[string][]string{} // tag to its groups
	knownGroups := map[string]bool{}
	if groups := get(root, "x-tagGroups"); groups != nil {
		for _, g := range groups.Content {
			name := scalarOf(get(g, "name"))
			knownGroups[name] = true
			if tags := get(g, "tags"); tags != nil {
				for _, t := range tags.Content {
					groupsOf[t.Value] = append(groupsOf[t.Value], name)
				}
			}
		}
	}
	knownTags := map[string]bool{}
	if tags := get(root, "tags"); tags != nil {
		for _, t := range tags.Content {
			knownTags[scalarOf(get(t, "name"))] = true
		}
	}
	knownServices := map[string]bool{}
	for _, s := range services {
		knownServices[s] = true
	}
	paths := get(root, "paths")
	hadOperations := map[*yaml.Node]bool{} // path items; one of the base without any stays
	if paths != nil {
		for i := 0; i+1 < len(paths.Content); i += 2 {
			hadOperations[paths.Content[i+1]] = hasOperation(paths.Content[i+1])
		}
	}
	forEachOperation(paths, func(_ string, op *yaml.Node) bool {
		for _, t := range opTags(op) {
			knownTags[t] = true
		}
		return true
	})
	var errs []error
	for _, sel := range []struct {
		key string
		s   Selector
	}{{"include", d.Include}, {"exclude", d.Exclude}} {
		for _, g := range sel.s.TagGroups {
			if !knownGroups[g] {
				errs = append(errs, fmt.Errorf("%s.tag_groups: no group %q in x-tagGroups", sel.key, g))
			}
		}
		for _, t := range sel.s.Tags {
			if !knownTags[t] {
				errs = append(errs, fmt.Errorf("%s.tags: no tag %q", sel.key, t))
			}
		}
		for _, s := range sel.s.Services {
			if !knownServices[s] {
				errs = append(errs, fmt.Errorf("%s.services: no service %q with an operation; a service is named in full, package.Service", sel.key, s))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("document %s: %w", d.Name, err)
	}

	matches := func(s Selector, op *yaml.Node) bool {
		if slices.Contains(s.Services, services[scalarOf(get(op, "operationId"))]) {
			return true
		}
		for _, t := range opTags(op) {
			if slices.Contains(s.Tags, t) {
				return true
			}
			for _, g := range groupsOf[t] {
				if slices.Contains(s.TagGroups, g) {
					return true
				}
			}
		}
		return false
	}
	usedTags := map[string]bool{}
	kept := 0
	forEachOperation(paths, func(_ string, op *yaml.Node) bool {
		if (!d.Include.empty() && !matches(d.Include, op)) || matches(d.Exclude, op) {
			return false
		}
		kept++
		for _, t := range opTags(op) {
			usedTags[t] = true
		}
		return true
	})
	if kept == 0 {
		return fmt.Errorf("document %s selects no operation", d.Name)
	}
	if paths != nil {
		// A path whose operations are all cut away goes, whatever else its item says.
		var content []*yaml.Node
		for i := 0; i+1 < len(paths.Content); i += 2 {
			if item := paths.Content[i+1]; !hadOperations[item] || hasOperation(item) {
				content = append(content, paths.Content[i], paths.Content[i+1])
			}
		}
		paths.Content = content
	}
	if tags := get(root, "tags"); tags != nil {
		tags.Content = slices.DeleteFunc(tags.Content, func(t *yaml.Node) bool { return !usedTags[scalarOf(get(t, "name"))] })
		if len(tags.Content) == 0 {
			remove(root, "tags")
		}
	}
	if groups := get(root, "x-tagGroups"); groups != nil {
		groups.Content = slices.DeleteFunc(groups.Content, func(g *yaml.Node) bool {
			tags := get(g, "tags")
			if tags == nil {
				return true
			}
			tags.Content = slices.DeleteFunc(tags.Content, func(t *yaml.Node) bool { return !usedTags[t.Value] })
			return len(tags.Content) == 0
		})
		if len(groups.Content) == 0 {
			remove(root, "x-tagGroups")
		}
	}
	if info := get(root, "info"); info != nil {
		if d.Title != "" {
			set(info, "title", scalar(d.Title))
		}
		if d.Description != "" {
			set(info, "description", scalar(d.Description))
		}
	}
	shake(root)
	return nil
}

// shake leaves in components only what the document uses: what a reference outside of
// components names, what the components so kept name in turn, and the security schemes
// a security requirement names.
func shake(root *yaml.Node) {
	comps := get(root, "components")
	if comps == nil || comps.Kind != yaml.MappingNode {
		return
	}
	used := map[string]bool{} // kind/name
	var queue []*yaml.Node
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode {
			if rest, ok := strings.CutPrefix(n.Value, "#/components/"); ok {
				kind, name, ok := strings.Cut(rest, "/")
				if !ok {
					return
				}
				name, _, _ = strings.Cut(name, "/") // a pointer into a component keeps all of it
				name = unpointer(name)
				if key := kind + "/" + name; !used[key] {
					used[key] = true
					if kn := get(comps, kind); kn != nil {
						if c := get(kn, name); c != nil {
							queue = append(queue, c)
						}
					}
				}
			}
			return
		}
		if n.Kind == yaml.MappingNode {
			// A discriminator maps values to schemas by reference or by bare name.
			if disc := get(n, "discriminator"); disc != nil {
				if mapping := get(disc, "mapping"); mapping != nil {
					for i := 1; i < len(mapping.Content); i += 2 {
						if v := mapping.Content[i].Value; !strings.Contains(v, "#") && !strings.Contains(v, "/") {
							walk(scalar("#/components/schemas/" + pointerToken(v)))
						}
					}
				}
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "components" {
			walk(root.Content[i+1])
		}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		walk(n)
	}
	// Security requirements name schemes, not refer to them.
	schemes := map[string]bool{}
	requirements := func(sec *yaml.Node) {
		if sec == nil {
			return
		}
		for _, req := range sec.Content {
			for i := 0; i+1 < len(req.Content); i += 2 {
				schemes[req.Content[i].Value] = true
			}
		}
	}
	requirements(get(root, "security"))
	forEachOperation(get(root, "paths"), func(_ string, op *yaml.Node) bool {
		requirements(get(op, "security"))
		return true
	})
	var kinds []*yaml.Node
	for i := 0; i+1 < len(comps.Content); i += 2 {
		kind, entries := comps.Content[i].Value, comps.Content[i+1]
		if entries.Kind != yaml.MappingNode || strings.HasPrefix(kind, "x-") {
			kinds = append(kinds, comps.Content[i], entries)
			continue
		}
		var content []*yaml.Node
		for j := 0; j+1 < len(entries.Content); j += 2 {
			name := entries.Content[j].Value
			keep := used[kind+"/"+name]
			if kind == "securitySchemes" {
				keep = keep || schemes[name]
			}
			if keep {
				content = append(content, entries.Content[j], entries.Content[j+1])
			}
		}
		if len(content) > 0 {
			entries.Content = content
			kinds = append(kinds, comps.Content[i], entries)
		}
	}
	comps.Content = kinds
	if len(kinds) == 0 {
		remove(root, "components")
	}
}

// forEachOperation calls keep with every operation of the paths and removes the ones it
// returns false for.
func forEachOperation(paths *yaml.Node, keep func(path string, op *yaml.Node) bool) {
	if paths == nil || paths.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(paths.Content); i += 2 {
		item := paths.Content[i+1]
		if item.Kind != yaml.MappingNode {
			continue
		}
		var content []*yaml.Node
		for j := 0; j+1 < len(item.Content); j += 2 {
			k, v := item.Content[j], item.Content[j+1]
			if slices.Contains(verbs, k.Value) && !keep(paths.Content[i].Value, v) {
				continue
			}
			content = append(content, k, v)
		}
		item.Content = content
	}
}

func hasOperation(item *yaml.Node) bool {
	for j := 0; j+1 < len(item.Content); j += 2 {
		if slices.Contains(verbs, item.Content[j].Value) {
			return true
		}
	}
	return false
}

func opTags(op *yaml.Node) []string {
	var out []string
	if tags := get(op, "tags"); tags != nil {
		for _, t := range tags.Content {
			out = append(out, t.Value)
		}
	}
	return out
}

func scalarOf(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	return n.Value
}

func remove(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = slices.Delete(m.Content, i, i+2)
			return
		}
	}
}

// pointerToken is a name as a JSON pointer token.
func pointerToken(name string) string { return strings.NewReplacer("~", "~0", "/", "~1").Replace(name) }

// unalias replaces every alias under n with the node it names, and drops the anchors:
// a node written twice is then written in full twice.
func unalias(n *yaml.Node) {
	n.Anchor = ""
	for i, c := range n.Content {
		for c.Kind == yaml.AliasNode && c.Alias != nil {
			c = c.Alias
		}
		n.Content[i] = c
		unalias(c)
	}
}

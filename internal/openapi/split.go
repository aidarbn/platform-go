package openapi

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The files of the split layout, relative to the output folder.
const (
	// RootFile is the description people read and review: the base, the tags and a
	// reference to every path in the file of its proto file.
	RootFile = "openapi.yaml"
	// BundleFile is the whole description in one file without external references:
	// what the service serves and what clients are generated from.
	BundleFile = "openapi.bundle.yaml"
	// CommonFile holds what belongs to no described proto file: the error body and
	// the messages of dependencies such as googleapis.
	CommonFile = "common.yaml"
)

// fileOf is the yaml of a proto file: shop/v1/shop.proto is shop/v1/shop.yaml.
func fileOf(proto string) string { return strings.TrimSuffix(proto, ".proto") + ".yaml" }

// fragment is a file of the split layout: the paths and the schemas of a proto file.
type fragment struct {
	Paths      *Ordered[*PathItem] `yaml:"paths,omitempty"`
	Components *Components         `yaml:"components,omitempty"`
}

// split lays the description out by proto file: a change to a proto file changes its
// own yaml, and the root only when a path comes or goes.
func (g *generator) split(base []byte, baseName string, doc *Document) (map[string][]byte, error) {
	schemaFile := map[string]string{}
	for name, home := range g.homes {
		schemaFile[name] = CommonFile
		if home != "" {
			schemaFile[name] = fileOf(home)
		}
	}
	for name := range g.schemas {
		if _, ok := schemaFile[name]; !ok {
			schemaFile[name] = CommonFile // the error body
		}
	}
	responseFile := map[string]string{}
	for name := range g.responses {
		responseFile[name] = CommonFile
	}
	for proto := range g.targets {
		f := fileOf(proto)
		for _, own := range []string{RootFile, BundleFile, CommonFile, baseName} {
			// Case does not count: macOS and Windows would write both into one file.
			if strings.EqualFold(f, own) {
				return nil, fmt.Errorf("%s would be written to %s, a file of the description itself; move the proto file into a package folder", proto, own)
			}
		}
	}

	frags := map[string]*fragment{}
	frag := func(file string) *fragment {
		f := frags[file]
		if f == nil {
			f = &fragment{Paths: NewOrdered[*PathItem](), Components: &Components{Schemas: NewOrdered[*Schema](), Responses: NewOrdered[*Response]()}}
			frags[file] = f
		}
		return f
	}
	frag(CommonFile)

	// The path items of the root: a reference to the item in the file of its proto
	// file, or the item itself when operations of several files share the path.
	rootPaths := NewOrdered[*PathItem]()
	shared := map[string]bool{}
	for _, p := range doc.Paths.keys {
		item := doc.Paths.m[p]
		var file string
		for _, verb := range verbs {
			if op := *item.slot(verb); op != nil {
				if f := fileOf(g.opHomes[op]); file == "" {
					file = f
				} else if f != file {
					shared[p] = true
				}
			}
		}
		if shared[p] {
			// Every operation lives in one place: here the root.
			rootPaths.Set(p, item)
			continue
		}
		frag(file).Paths.Set(p, item)
		rootPaths.Set(p, &PathItem{Ref: file + "#/paths/" + pointer(p)})
	}
	for _, name := range slices.Sorted(maps.Keys(g.schemas)) {
		frag(schemaFile[name]).Components.Schemas.Set(name, g.schemas[name])
	}
	for _, name := range slices.Sorted(maps.Keys(g.responses)) {
		frag(responseFile[name]).Components.Responses.Set(name, g.responses[name])
	}

	refs := func(kind, name string) (string, bool) {
		var f string
		var ok bool
		switch kind {
		case "schemas":
			f, ok = schemaFile[name]
		case "responses":
			f, ok = responseFile[name]
		}
		return f, ok
	}
	out := map[string][]byte{}
	for file, f := range frags {
		var n yaml.Node
		if err := n.Encode(f); err != nil {
			return nil, err
		}
		relink(&n, file, refs)
		b, err := encode(&n)
		if err != nil {
			return nil, err
		}
		out[file] = b
	}

	root, err := mergeNode(base, &Document{OpenAPI: doc.OpenAPI, Tags: doc.Tags, Paths: rootPaths, Extensions: doc.Extensions})
	if err != nil {
		return nil, err
	}
	if paths := get(root, "paths"); paths != nil {
		// Only the items written into the root carry references of their own; a
		// path item of the base keeps the ones it has.
		for i := 0; i+1 < len(paths.Content); i += 2 {
			if shared[paths.Content[i].Value] {
				relink(paths.Content[i+1], RootFile, refs)
			}
		}
	}
	b, err := encode(root)
	if err != nil {
		return nil, err
	}
	out[RootFile] = b
	return out, nil
}

var verbs = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// relink points the references to generated components at the file that holds them,
// relative to the file being written.
func relink(n *yaml.Node, from string, file func(kind, name string) (string, bool)) {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Value != "$ref" || v.Kind != yaml.ScalarNode || !strings.HasPrefix(v.Value, "#/components/") {
				continue
			}
			kind, name, ok := strings.Cut(strings.TrimPrefix(v.Value, "#/components/"), "/")
			if !ok {
				continue
			}
			to, ok := file(kind, unpointer(name))
			if !ok || to == from {
				continue
			}
			v.Value = relative(from, to) + v.Value
		}
	}
	for _, c := range n.Content {
		relink(c, from, file)
	}
}

// relative is the path of to as seen from the folder of from; both are relative to
// the output folder.
func relative(from, to string) string {
	fromDir := strings.Split(path.Dir(from), "/")
	if path.Dir(from) == "." {
		fromDir = nil
	}
	toParts := strings.Split(to, "/")
	i := 0
	for i < len(fromDir) && i < len(toParts)-1 && fromDir[i] == toParts[i] {
		i++
	}
	var parts []string
	for range fromDir[i:] {
		parts = append(parts, "..")
	}
	return strings.Join(append(parts, toParts[i:]...), "/")
}

// pointer is a key as a JSON pointer token inside a URI fragment: / and ~ escaped,
// and the braces of path templates percent-encoded, as a URI wants them.
func pointer(key string) string {
	key = strings.NewReplacer("~", "~0", "/", "~1").Replace(key)
	return strings.NewReplacer("{", "%7B", "}", "%7D").Replace(key)
}

func unpointer(token string) string {
	token = strings.NewReplacer("%7B", "{", "%7D", "}").Replace(token)
	return strings.NewReplacer("~1", "/", "~0", "~").Replace(token)
}

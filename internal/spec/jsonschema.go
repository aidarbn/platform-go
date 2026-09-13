package spec

import (
	"encoding/json"
	"fmt"

	"github.com/aidarbn/platform-go/internal/registry"
)

// SchemaURL is where the JSON schema of platformgo.yaml is published for a version.
// Editors that follow yaml-language-server read it from the first line of the file.
func SchemaURL(version string) string {
	return fmt.Sprintf("https://raw.githubusercontent.com/aidarbn/platform-go/%s/schema/platformgo.schema.json", version)
}

// JSONSchema returns the JSON schema of platformgo.yaml, built from the module registry,
// so the editor knows exactly the modules and options this platformgo knows.
func JSONSchema() ([]byte, error) {
	modules := map[string]any{}
	for _, m := range registry.All() {
		options := map[string]any{}
		for _, o := range m.Options {
			prop := map[string]any{"type": "string", "description": o.Description}
			if o.Default != "" {
				prop["default"] = o.Default
			}
			options[o.Name] = prop
		}
		description := "platform module " + m.Name
		if len(m.Requires) > 0 {
			description += "; requires " + joinNames(m.Requires)
		}
		modules[m.Name] = map[string]any{
			"type":                 "object",
			"description":          description,
			"properties":           options,
			"additionalProperties": false,
		}
	}

	schema := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"$id":                  "https://github.com/aidarbn/platform-go/schema/platformgo.schema.json",
		"title":                "platformgo.yaml",
		"description":          "How a project on platform-go is put together: modules and their technical options. Business settings live in settings.yaml.",
		"type":                 "object",
		"required":             []string{"schema", "project"},
		"additionalProperties": false,
		"properties": map[string]any{
			"schema": map[string]any{
				"description": "format version of this file",
				"const":       SchemaVersion,
			},
			"platform": map[string]any{
				"description": "platform version the project is on",
				"type":        "string",
				"pattern":     `^v\d+\.\d+\.\d+`,
			},
			"project": map[string]any{
				"type":                 "object",
				"required":             []string{"module"},
				"additionalProperties": false,
				"properties": map[string]any{
					"module":  map[string]any{"type": "string", "description": "Go module path"},
					"service": map[string]any{"type": "string", "description": "service name in logs; the last element of the module path by default"},
					"go":      map[string]any{"type": "string", "description": "Go version"},
				},
			},
			"modules": map[string]any{
				"type":                 []string{"object", "null"},
				"description":          "enabled modules; a module is enabled by its section and removed by deleting it",
				"properties":           modules,
				"additionalProperties": false,
			},
		},
	}
	out, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

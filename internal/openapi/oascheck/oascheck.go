// Package oascheck validates OpenAPI 3.1 documents and values against their schemas,
// for the tests of the generator and of the api module: a document must pass the
// schema of the specification, every schema in it must compile as JSON Schema 2020-12,
// and what the gateway answers must match the schema the document gives for it.
package oascheck

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

// The schemas of the specification, from https://spec.openapis.org/oas/3.1/, version
// 2022-10-07: the document, the document with schemas in the OpenAPI dialect, the
// dialect and its vocabulary.
//
//go:embed schemas/*.json
var specFS embed.FS

var specSchemas = map[string]string{
	"https://spec.openapis.org/oas/3.1/schema/2022-10-07":      "schemas/schema.json",
	"https://spec.openapis.org/oas/3.1/schema-base/2022-10-07": "schemas/schema-base.json",
	"https://spec.openapis.org/oas/3.1/dialect/base":           "schemas/dialect-base.json",
	"https://spec.openapis.org/oas/3.1/meta/base":              "schemas/meta-base.json",
}

const docURL = "mem:///openapi.json"

// JSON turns a YAML document into the values encoding/json would read from it.
func JSON(doc []byte) (any, error) {
	var v any
	if err := yaml.Unmarshal(doc, &v); err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(b))
}

// Validate checks a document: the specification's schema with the OpenAPI dialect for
// the schemas inside, then every component schema compiled on its own, so a reference
// to a missing schema fails too.
func Validate(doc []byte) error {
	v, err := JSON(doc)
	if err != nil {
		return err
	}
	c := jsonschema.NewCompiler()
	for id, file := range specSchemas {
		b, err := specFS.ReadFile(file)
		if err != nil {
			return err
		}
		s, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			return err
		}
		if err := c.AddResource(id, s); err != nil {
			return err
		}
	}
	spec, err := c.Compile("https://spec.openapis.org/oas/3.1/schema-base/2022-10-07")
	if err != nil {
		return fmt.Errorf("compile the schema of the specification: %w", err)
	}
	if err := spec.Validate(v); err != nil {
		return fmt.Errorf("not OpenAPI 3.1: %w", err)
	}
	checker, err := NewChecker(doc)
	if err != nil {
		return err
	}
	for _, name := range checker.names {
		if _, err := checker.schema(name); err != nil {
			return err
		}
	}
	return nil
}

// Checker checks values against the component schemas of a document.
type Checker struct {
	c     *jsonschema.Compiler
	names []string
	cache map[string]*jsonschema.Schema
}

// NewChecker reads the component schemas of a document.
func NewChecker(doc []byte) (*Checker, error) {
	v, err := JSON(doc)
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	if err := c.AddResource(docURL, v); err != nil {
		return nil, err
	}
	ch := &Checker{c: c, cache: map[string]*jsonschema.Schema{}}
	if root, ok := v.(map[string]any); ok {
		if comps, ok := root["components"].(map[string]any); ok {
			if schemas, ok := comps["schemas"].(map[string]any); ok {
				for name := range schemas {
					ch.names = append(ch.names, name)
				}
			}
		}
	}
	slices.Sort(ch.names)
	return ch, nil
}

func (ch *Checker) schema(name string) (*jsonschema.Schema, error) {
	if s, ok := ch.cache[name]; ok {
		return s, nil
	}
	ptr := strings.NewReplacer("~", "~0", "/", "~1").Replace(name)
	s, err := ch.c.Compile(docURL + "#/components/schemas/" + url.PathEscape(ptr))
	if err != nil {
		return nil, fmt.Errorf("schema %s: %w", name, err)
	}
	ch.cache[name] = s
	return s, nil
}

// Check validates a JSON value against the component schema of a name.
func (ch *Checker) Check(name string, body []byte) error {
	s, err := ch.schema(name)
	if err != nil {
		return err
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return err
	}
	if err := s.Validate(v); err != nil {
		return fmt.Errorf("%s does not match %s: %w", body, name, err)
	}
	return nil
}

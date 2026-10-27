package openapi

import (
	"go.yaml.in/yaml/v3"
)

// The OpenAPI document the generator writes. Struct fields are in the order a reader
// expects them in the file; maps whose order matters are ordered by the generator, the
// rest are sorted, so the same proto files always give the same bytes.

// Document is an OpenAPI 3.1 document.
type Document struct {
	OpenAPI    string              `yaml:"openapi"`
	Info       *Info               `yaml:"info,omitempty"`
	Tags       []*Tag              `yaml:"tags,omitempty"`
	Paths      *Ordered[*PathItem] `yaml:"paths,omitempty"`
	Components *Components         `yaml:"components,omitempty"`
	Extensions map[string]any      `yaml:",inline"`
}

// Info is the title and the version of the API.
type Info struct {
	Title   string `yaml:"title"`
	Version string `yaml:"version"`
}

// Tag is a section of the API.
type Tag struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description,omitempty"`
	Extensions  map[string]any `yaml:",inline"`
}

// Components are the schemas and responses operations refer to.
type Components struct {
	Schemas   *Ordered[*Schema]   `yaml:"schemas,omitempty"`
	Responses *Ordered[*Response] `yaml:"responses,omitempty"`
}

// PathItem holds the operations of one path.
type PathItem struct {
	Get     *Operation `yaml:"get,omitempty"`
	Put     *Operation `yaml:"put,omitempty"`
	Post    *Operation `yaml:"post,omitempty"`
	Delete  *Operation `yaml:"delete,omitempty"`
	Options *Operation `yaml:"options,omitempty"`
	Head    *Operation `yaml:"head,omitempty"`
	Patch   *Operation `yaml:"patch,omitempty"`
	Trace   *Operation `yaml:"trace,omitempty"`
}

// Operation is one HTTP binding of an RPC.
type Operation struct {
	Tags        []string            `yaml:"tags,omitempty"`
	Summary     string              `yaml:"summary,omitempty"`
	Description string              `yaml:"description,omitempty"`
	OperationID string              `yaml:"operationId"`
	Deprecated  bool                `yaml:"deprecated,omitempty"`
	Parameters  []*Parameter        `yaml:"parameters,omitempty"`
	RequestBody *RequestBody        `yaml:"requestBody,omitempty"`
	Responses   *Ordered[*Response] `yaml:"responses"`
	Extensions  map[string]any      `yaml:",inline"`
}

// Parameter is a path or query parameter.
type Parameter struct {
	Name        string  `yaml:"name"`
	In          string  `yaml:"in"`
	Description string  `yaml:"description,omitempty"`
	Required    bool    `yaml:"required,omitempty"`
	Deprecated  bool    `yaml:"deprecated,omitempty"`
	Style       string  `yaml:"style,omitempty"`
	Explode     *bool   `yaml:"explode,omitempty"`
	Schema      *Schema `yaml:"schema"`
}

// RequestBody is the body of a call.
type RequestBody struct {
	Description string               `yaml:"description,omitempty"`
	Required    bool                 `yaml:"required,omitempty"`
	Content     *Ordered[*MediaType] `yaml:"content"`
}

// Response is an answer of an operation, or a reference to a shared one.
type Response struct {
	Ref         string               `yaml:"$ref,omitempty"`
	Description string               `yaml:"description,omitempty"`
	Content     *Ordered[*MediaType] `yaml:"content,omitempty"`
}

// MediaType is the schema of a body in one content type.
type MediaType struct {
	Schema   *Schema             `yaml:"schema"`
	Encoding *Ordered[*Encoding] `yaml:"encoding,omitempty"`
}

// Encoding is how a part of a multipart body is written.
type Encoding struct {
	ContentType string `yaml:"contentType,omitempty"`
}

// Schema is a JSON Schema 2020-12 schema, the subset the generator writes.
type Schema struct {
	Ref                  string            `yaml:"$ref,omitempty"`
	Type                 any               `yaml:"type,omitempty"` // a name, or a list of names for a nullable type
	Format               string            `yaml:"format,omitempty"`
	Description          string            `yaml:"description,omitempty"`
	Deprecated           bool              `yaml:"deprecated,omitempty"`
	Const                *Value            `yaml:"const,omitempty"`
	Enum                 []any             `yaml:"enum,omitempty"`
	Pattern              string            `yaml:"pattern,omitempty"`
	MinLength            *uint64           `yaml:"minLength,omitempty"`
	MaxLength            *uint64           `yaml:"maxLength,omitempty"`
	Minimum              *Value            `yaml:"minimum,omitempty"`
	ExclusiveMinimum     *Value            `yaml:"exclusiveMinimum,omitempty"`
	Maximum              *Value            `yaml:"maximum,omitempty"`
	ExclusiveMaximum     *Value            `yaml:"exclusiveMaximum,omitempty"`
	ContentEncoding      string            `yaml:"contentEncoding,omitempty"`
	ContentMediaType     string            `yaml:"contentMediaType,omitempty"`
	Items                *Schema           `yaml:"items,omitempty"`
	MinItems             *uint64           `yaml:"minItems,omitempty"`
	MaxItems             *uint64           `yaml:"maxItems,omitempty"`
	UniqueItems          bool              `yaml:"uniqueItems,omitempty"`
	Properties           *Ordered[*Schema] `yaml:"properties,omitempty"`
	Required             []string          `yaml:"required,omitempty"`
	AdditionalProperties *Value            `yaml:"additionalProperties,omitempty"` // false or a *Schema
	PropertyNames        *Schema           `yaml:"propertyNames,omitempty"`
	MinProperties        *uint64           `yaml:"minProperties,omitempty"`
	MaxProperties        *uint64           `yaml:"maxProperties,omitempty"`
	OneOf                []*Schema         `yaml:"oneOf,omitempty"`
	AnyOf                []*Schema         `yaml:"anyOf,omitempty"`
	AllOf                []*Schema         `yaml:"allOf,omitempty"`
	Not                  *Schema           `yaml:"not,omitempty"`
	Examples             []any             `yaml:"examples,omitempty"`
	Extensions           map[string]any    `yaml:",inline"`
}

// Value is a value that must be written even when it is the zero value: minimum: 0,
// const: false, additionalProperties: false.
type Value struct{ V any }

// MarshalYAML writes the value itself.
func (v Value) MarshalYAML() (any, error) { return v.V, nil }

func val(v any) *Value { return &Value{V: v} }

func u64(n uint64) *uint64 { return &n }

// Ordered is a map that keeps the order its keys were set in.
type Ordered[V any] struct {
	keys []string
	m    map[string]V
}

// NewOrdered returns an empty ordered map.
func NewOrdered[V any]() *Ordered[V] { return &Ordered[V]{m: map[string]V{}} }

// Set sets a key; a new key goes last.
func (o *Ordered[V]) Set(k string, v V) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

// Len is the number of keys.
func (o *Ordered[V]) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// IsZero leaves an empty map out of the document.
func (o *Ordered[V]) IsZero() bool { return o.Len() == 0 }

// MarshalYAML writes the keys in order.
func (o *Ordered[V]) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range o.keys {
		var v yaml.Node
		if err := v.Encode(o.m[k]); err != nil {
			return nil, err
		}
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, &v)
	}
	return n, nil
}

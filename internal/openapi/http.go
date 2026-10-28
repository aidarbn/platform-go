package openapi

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// httpRule is the google.api.http option of a method, nil without one: the gateway does
// not serve such a method over REST.
func httpRule(md protoreflect.MethodDescriptor) *annotations.HttpRule {
	opts, ok := md.Options().(*descriptorpb.MethodOptions)
	if !ok || opts == nil || !proto.HasExtension(opts, annotations.E_Http) {
		return nil
	}
	r, _ := proto.GetExtension(opts, annotations.E_Http).(*annotations.HttpRule)
	return r
}

// bindings are the rule and its additional bindings.
func bindings(r *annotations.HttpRule) []*annotations.HttpRule {
	return append([]*annotations.HttpRule{r}, r.GetAdditionalBindings()...)
}

// verbAndPath reads the method and the path template of a binding; the method is empty
// for a custom verb OpenAPI has no operation for.
func verbAndPath(r *annotations.HttpRule) (string, string) {
	switch p := r.GetPattern().(type) {
	case *annotations.HttpRule_Get:
		return "get", p.Get
	case *annotations.HttpRule_Put:
		return "put", p.Put
	case *annotations.HttpRule_Post:
		return "post", p.Post
	case *annotations.HttpRule_Delete:
		return "delete", p.Delete
	case *annotations.HttpRule_Patch:
		return "patch", p.Patch
	case *annotations.HttpRule_Custom:
		switch kind := strings.ToLower(p.Custom.GetKind()); kind {
		case "head", "options", "trace":
			return kind, p.Custom.GetPath()
		}
	}
	return "", ""
}

// pathParam is a variable of a path template.
type pathParam struct {
	field   string // the field path, a.b for a nested field
	pattern string // a regular expression when the variable spans several segments
}

// parseTemplate turns a path template into an OpenAPI path: /v1/{name=shelves/*} is
// /v1/{name}, with the segments the variable must match as its pattern.
func parseTemplate(tmpl string) (string, []pathParam, error) {
	var out strings.Builder
	var params []pathParam
	for i := 0; i < len(tmpl); i++ {
		c := tmpl[i]
		if c != '{' {
			out.WriteByte(c)
			continue
		}
		end := strings.IndexByte(tmpl[i:], '}')
		if end < 0 {
			return "", nil, fmt.Errorf("path %q: unclosed {", tmpl)
		}
		v := tmpl[i+1 : i+end]
		name, segments, hasPattern := strings.Cut(v, "=")
		p := pathParam{field: name}
		if hasPattern && segments != "*" {
			p.pattern = segmentsPattern(segments)
		}
		params = append(params, p)
		out.WriteString("{" + name + "}")
		i += end
	}
	return out.String(), params, nil
}

// segmentsPattern is the regular expression of the segments of a path variable:
// * is one segment, ** any number of them.
func segmentsPattern(segments string) string {
	var parts []string
	for s := range strings.SplitSeq(segments, "/") {
		switch s {
		case "*":
			parts = append(parts, "[^/]+")
		case "**":
			parts = append(parts, ".+")
		default:
			parts = append(parts, regexp.QuoteMeta(s))
		}
	}
	return "^" + strings.Join(parts, "/") + "$"
}

// fieldByPath finds a field of a message by its path, a.b for a nested one.
func fieldByPath(md protoreflect.MessageDescriptor, path string) protoreflect.FieldDescriptor {
	var fd protoreflect.FieldDescriptor
	for part := range strings.SplitSeq(path, ".") {
		if md == nil {
			return nil
		}
		fd = md.Fields().ByName(protoreflect.Name(part))
		if fd == nil {
			fd = md.Fields().ByJSONName(part)
		}
		if fd == nil {
			return nil
		}
		md = fd.Message()
	}
	return fd
}

// operation describes one binding of a method.
func (g *generator) operation(m protoreflect.MethodDescriptor, r *annotations.HttpRule, id string, tag string) (string, string, *Operation, error) {
	verb, tmpl := verbAndPath(r)
	if verb == "" {
		return "", "", nil, nil
	}
	path, params, err := parseTemplate(tmpl)
	if err != nil {
		return "", "", nil, fmt.Errorf("%s: %w", m.FullName(), err)
	}
	req := m.Input()
	op := &Operation{Tags: []string{tag}, OperationID: id, Responses: NewOrdered[*Response]()}
	op.Summary, op.Description = summarize(string(m.Name()), comment(m))
	if opts, ok := m.Options().(*descriptorpb.MethodOptions); ok && opts.GetDeprecated() {
		op.Deprecated = true
	}
	if opts, ok := m.Parent().Options().(*descriptorpb.ServiceOptions); ok && opts.GetDeprecated() {
		op.Deprecated = true
	}

	inPath := map[string]bool{}
	for _, p := range params {
		fd := fieldByPath(req, p.field)
		if fd == nil {
			return "", "", nil, fmt.Errorf("%s: path %s: %s has no field %s", m.FullName(), tmpl, req.FullName(), p.field)
		}
		inPath[p.field] = true
		s, _ := applyRules(g.value(fd, input), fd, fieldRules(fd))
		paramSchema(s)
		if p.pattern != "" {
			s.Pattern = p.pattern
		}
		op.Parameters = append(op.Parameters, &Parameter{
			Name: p.field, In: "path", Required: true, Schema: s,
			Description: comment(fd), Deprecated: deprecatedField(fd),
		})
	}

	body := r.GetBody()
	if body != "*" {
		op.Parameters = append(op.Parameters, g.queryParams(req, "", inPath, body, map[protoreflect.FullName]bool{req.FullName(): true})...)
	}
	switch {
	case body == "*":
		exclude := map[string]bool{}
		for f := range inPath {
			if !strings.Contains(f, ".") {
				exclude[f] = true
			}
		}
		var s *Schema
		if len(exclude) > 0 || g.wellKnown(req, input) != nil {
			s = g.inlineBody(req, exclude)
		} else {
			s = g.ref(req, input)
		}
		op.RequestBody = g.requestBody(req, s, exclude)
		// A body with nothing left once the path took its fields is optional: there is
		// nothing to send.
		// google.protobuf.Empty has no field either; the other well-known types have.
		op.RequestBody.Required = req.Fields().Len() > len(exclude)
	case body != "":
		fd := req.Fields().ByName(protoreflect.Name(body))
		if fd == nil {
			return "", "", nil, fmt.Errorf("%s: body %s: %s has no such field", m.FullName(), body, req.FullName())
		}
		var md protoreflect.MessageDescriptor
		if !fd.IsList() && !fd.IsMap() {
			md = fd.Message()
		}
		op.RequestBody = g.requestBody(md, g.field(fd, input), nil)
		op.RequestBody.Required = true
	}

	if m.IsStreamingClient() && op.RequestBody != nil {
		op.RequestBody.Description = "A stream: one JSON object a line."
	}

	ok := &Response{Description: "OK", Content: NewOrdered[*MediaType]()}
	switch {
	case m.Output().FullName() == "google.api.HttpBody" && r.GetResponseBody() == "":
		ok.Content.Set("*/*", &MediaType{Schema: &Schema{}})
		ok.Description = "The body as the method writes it, in the content type it sets."
	case m.IsStreamingServer():
		ok.Description = "A stream: one JSON object a line, each with a result or the error that ended the stream."
		ok.Content.Set("application/json", &MediaType{Schema: &Schema{
			Type: "object",
			Properties: orderedOf(
				"result", g.responseSchema(m, r),
				"error", &Schema{Ref: schemaRef + errorSchema},
			),
		}})
	default:
		ok.Content.Set("application/json", &MediaType{Schema: g.responseSchema(m, r)})
	}
	op.Responses.Set("200", ok)
	op.Responses.Set("default", &Response{Ref: "#/components/responses/" + errorResponse})
	return verb, path, op, nil
}

// responseSchema is the schema of a response: the message, or its response_body field.
func (g *generator) responseSchema(m protoreflect.MethodDescriptor, r *annotations.HttpRule) *Schema {
	if rb := r.GetResponseBody(); rb != "" {
		if fd := m.Output().Fields().ByName(protoreflect.Name(rb)); fd != nil {
			return g.field(fd, output)
		}
	}
	return g.ref(m.Output(), output)
}

// inlineBody is a request message written into the operation: the one with the fields
// the path carries left out.
func (g *generator) inlineBody(md protoreflect.MessageDescriptor, exclude map[string]bool) *Schema {
	if s := g.wellKnown(md, input); s != nil {
		return s
	}
	return g.message(md, input, exclude)
}

// requestBody is a JSON body, and a multipart one when the message holds files.
//
// The caller marks it required when the rule has a body with fields in it. The gateway
// reads an empty body as {} and validates that, so required asks only for what a client
// can always send, and it tells generated clients and documentation pages to send a body
// rather than leave it out — what protoc-gen-connect-openapi said before v0.10.0.
func (g *generator) requestBody(md protoreflect.MessageDescriptor, s *Schema, exclude map[string]bool) *RequestBody {
	rb := &RequestBody{Content: NewOrdered[*MediaType]()}
	if md != nil && g.hasFiles(md) {
		rb.Content.Set("multipart/form-data", g.multipart(md, exclude))
	}
	rb.Content.Set("application/json", &MediaType{Schema: s})
	return rb
}

// hasFiles reports whether a message has a file field, alone or repeated.
func (g *generator) hasFiles(md protoreflect.MessageDescriptor) bool {
	fields := md.Fields()
	for i := range fields.Len() {
		if fd := fields.Get(i); !fd.IsMap() && isFile(fd.Message()) {
			return true
		}
	}
	return false
}

// multipart describes the body the gateway's multipart decoder accepts: a file field is
// a file part, a repeated one several parts in order or by index (images[0]), a message
// a part of JSON, bytes the raw part, any other field a text part.
func (g *generator) multipart(md protoreflect.MessageDescriptor, exclude map[string]bool) *MediaType {
	s := &Schema{Type: "object", Properties: NewOrdered[*Schema]()}
	enc := NewOrdered[*Encoding]()
	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		name := string(fd.Name())
		if exclude[name] || fd.IsMap() {
			continue
		}
		binary := &Schema{Type: "string", Format: "binary"}
		var p *Schema
		switch {
		case isFile(fd.Message()) && fd.IsList():
			p = &Schema{Type: "array", Items: binary, Description: joinText(comment(fd),
				fmt.Sprintf("Files in parts named %s, in order, or %s[0], %s[1]… to set given positions.", name, name, name))}
		case isFile(fd.Message()):
			p = binary
			p.Description = comment(fd)
		case fd.Kind() == protoreflect.BytesKind && !fd.IsList():
			p = binary
			p.Description = comment(fd)
		case fd.Message() != nil && fd.IsList():
			continue // the decoder has no part for a list of messages
		case fd.Message() != nil:
			// A message, a well-known one too, is a part of JSON: a time is "…" in quotes.
			p = g.field(fd, input)
			enc.Set(name, &Encoding{ContentType: "application/json"})
		default:
			p = g.field(fd, input)
		}
		p.Deprecated = p.Deprecated || deprecatedField(fd)
		s.Properties.Set(name, p)
		if required(fd) && !translatable(fd) {
			s.Required = append(s.Required, name)
		}
	}
	return &MediaType{Schema: s, Encoding: enc}
}

// queryParams are the fields a request takes from the query string: every field not in
// the path or the body, nested messages as a.b. The gateway has no query form for maps
// and lists of messages.
func (g *generator) queryParams(md protoreflect.MessageDescriptor, prefix string, inPath map[string]bool, body string, seen map[protoreflect.FullName]bool) []*Parameter {
	var out []*Parameter
	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		name := prefix + string(fd.Name())
		if inPath[name] || (prefix == "" && string(fd.Name()) == body) {
			continue
		}
		if fd.IsMap() {
			if v := fd.MapValue(); v.Message() == nil || queryWellKnown[v.Message().FullName()] {
				// name[key]=value, one key a pair.
				s := g.field(fd, input)
				paramSchema(s)
				if vs, ok := s.AdditionalProperties.V.(*Schema); ok {
					paramSchema(vs)
				}
				desc := s.Description
				s.Description = ""
				out = append(out, &Parameter{Name: name, In: "query", Style: "deepObject", Explode: ptr(true), Schema: s, Description: desc, Deprecated: deprecatedField(fd)})
			}
			continue
		}
		if msg := fd.Message(); msg != nil && g.wellKnown(msg, input) == nil {
			if fd.IsList() || seen[msg.FullName()] {
				continue
			}
			seen[msg.FullName()] = true
			out = append(out, g.queryParams(msg, name+".", inPath, body, seen)...)
			delete(seen, msg.FullName())
			continue
		}
		if msg := fd.Message(); msg != nil && !queryWellKnown[msg.FullName()] {
			continue
		}
		s := g.field(fd, input)
		paramSchema(s)
		desc := s.Description
		s.Description = ""
		out = append(out, &Parameter{
			Name: name, In: "query", Schema: s, Description: desc,
			Required:   prefix == "" && required(fd) && !translatable(fd),
			Deprecated: deprecatedField(fd),
		})
	}
	return out
}

// queryWellKnown are the well-known types the gateway reads from a query string.
var queryWellKnown = map[protoreflect.FullName]bool{
	"google.protobuf.Timestamp": true, "google.protobuf.Duration": true, "google.protobuf.FieldMask": true,
	"google.protobuf.DoubleValue": true, "google.protobuf.FloatValue": true, "google.protobuf.Int64Value": true,
	"google.protobuf.UInt64Value": true, "google.protobuf.Int32Value": true, "google.protobuf.UInt32Value": true,
	"google.protobuf.BoolValue": true, "google.protobuf.StringValue": true, "google.protobuf.BytesValue": true,
}

// paramSchema is the schema of a value in a path or a query string, where a 64-bit
// integer is a number like any other: the string form of JSON is a body matter.
func paramSchema(s *Schema) {
	if s == nil {
		return
	}
	if is64(s) {
		s.Type = "integer"
		s.Enum = numbersOnly(s.Enum)
		if s.Not != nil {
			s.Not.Enum = numbersOnly(s.Not.Enum)
		}
	}
	paramSchema(s.Items)
	for _, sub := range slices.Concat(s.AnyOf, s.AllOf) {
		if len(sub.Enum) > 0 && sub.Type == nil {
			sub.Enum = numbersOnly(sub.Enum)
		}
		paramSchema(sub)
	}
}

// numbersOnly drops the string forms bothForms added.
func numbersOnly(values []any) []any {
	var out []any
	for _, v := range values {
		if _, ok := v.(string); !ok {
			out = append(out, v)
		}
	}
	return out
}

func ptr[T any](v T) *T { return &v }

func deprecatedField(fd protoreflect.FieldDescriptor) bool {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	return ok && opts.GetDeprecated()
}

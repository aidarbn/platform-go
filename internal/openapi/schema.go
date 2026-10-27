package openapi

import (
	"fmt"
	"strings"

	"github.com/aidarbn/platform-go/kit/i18nx/i18npb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// direction is the side of a message: the gateway reads a request and writes a response
// differently, so a message used on both sides has a schema for each.
type direction int

const (
	output direction = iota // a response: every field present, presence fields nullable
	input                   // a request: required only by protovalidate, rules as keywords
)

const schemaRef = "#/components/schemas/"

// ref is the schema of a message used as a value: a well-known type inline, any other
// message a reference to its component, generated once.
func (g *generator) ref(md protoreflect.MessageDescriptor, dir direction) *Schema {
	if s := g.wellKnown(md, dir); s != nil {
		return s
	}
	name := g.schemaName(md, dir)
	if !g.queued[name] {
		g.queued[name] = true
		g.queue = append(g.queue, queuedSchema{md: md, dir: dir, name: name})
	}
	return &Schema{Ref: schemaRef + name}
}

// schemaName is the full proto name; the request side of a message a response also
// uses gets an Input suffix. A request message nobody returns keeps the plain name.
func (g *generator) schemaName(md protoreflect.MessageDescriptor, dir direction) string {
	name := string(md.FullName())
	if dir == input && g.outputs[md.FullName()] {
		name += "Input"
	}
	return name
}

// drain generates the components the operations referred to, and the ones those refer
// to in turn.
func (g *generator) drain() {
	for len(g.queue) > 0 {
		q := g.queue[0]
		g.queue = g.queue[1:]
		g.schemas[q.name] = g.message(q.md, q.dir, nil)
		g.homes[q.name] = g.home(q.md.ParentFile())
	}
}

// home is the proto file whose yaml holds the schemas of a file: the file itself when
// it is described, common.yaml ("") for a dependency such as googleapis.
func (g *generator) home(f protoreflect.FileDescriptor) string {
	if g.targets[f.Path()] {
		return f.Path()
	}
	return ""
}

// markOutputs records every message a response can hold.
func (g *generator) markOutputs(md protoreflect.MessageDescriptor) {
	if g.outputs[md.FullName()] || wellKnownNames[md.FullName()] {
		return
	}
	g.outputs[md.FullName()] = true
	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if fd.IsMap() {
			fd = fd.MapValue()
		}
		if fd.Message() != nil {
			g.markOutputs(fd.Message())
		}
	}
}

// message is the object schema of a message. exclude leaves fields out: the ones a
// request takes from the path.
func (g *generator) message(md protoreflect.MessageDescriptor, dir direction, exclude map[string]bool) *Schema {
	s := &Schema{Type: "object", Properties: NewOrdered[*Schema]()}
	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		name := string(fd.Name())
		if exclude[name] {
			continue
		}
		s.Properties.Set(name, g.field(fd, dir))
		switch {
		case dir == output && fd.ContainingOneof() == nil:
			// The gateway writes unpopulated fields; oneof members and proto3 optional
			// fields only when they are set.
			s.Required = append(s.Required, name)
		case dir == input && required(fd) && !translatable(fd):
			s.Required = append(s.Required, name)
		}
	}
	var notes []string
	oneofs := md.Oneofs()
	for i := range oneofs.Len() {
		od := oneofs.Get(i)
		if od.IsSynthetic() {
			continue
		}
		var members []string
		for j := range od.Fields().Len() {
			if name := string(od.Fields().Get(j).Name()); !exclude[name] {
				members = append(members, name)
			}
		}
		if len(members) == 0 {
			continue
		}
		exactly := dir == input && oneofRequired(od)
		s.AllOf = append(s.AllOf, oneofSchema(members, exactly))
		quoted := "`" + strings.Join(members, "`, `") + "`"
		if exactly {
			notes = append(notes, "exactly one of "+quoted)
		} else {
			notes = append(notes, "at most one of "+quoted)
		}
	}
	if dir == input {
		if r := messageRules(md); r != nil {
			notes = append(notes, celNotes(r.GetCel(), r.GetCelExpression())...)
			for _, o := range r.GetOneof() {
				quoted := "`" + strings.Join(o.GetFields(), "`, `") + "`"
				if o.GetRequired() {
					notes = append(notes, "exactly one of "+quoted)
				} else {
					notes = append(notes, "at most one of "+quoted)
				}
			}
		}
	}
	s.Description = joinText(comment(md), notesText(notes))
	if opts, ok := md.Options().(*descriptorpb.MessageOptions); ok && opts.GetDeprecated() {
		s.Deprecated = true
	}
	if dir == output {
		s.AdditionalProperties = val(false)
	}
	return s
}

// oneofSchema says that at most one member is set, or exactly one: each member alone
// matches one branch, two members match two, and oneOf takes exactly one.
func oneofSchema(members []string, exactly bool) *Schema {
	var branches []*Schema
	var any []*Schema
	for _, m := range members {
		branches = append(branches, &Schema{Required: []string{m}})
		any = append(any, &Schema{Required: []string{m}})
	}
	if !exactly {
		branches = append(branches, &Schema{Not: &Schema{AnyOf: any}})
	}
	return &Schema{OneOf: branches}
}

// field is the schema of a field with its documentation and, in a request, its rules.
func (g *generator) field(fd protoreflect.FieldDescriptor, dir direction) *Schema {
	var s *Schema
	switch {
	case fd.IsMap():
		s = &Schema{Type: "object", AdditionalProperties: val(g.value(fd.MapValue(), dir))}
		switch fd.MapKey().Kind() {
		case protoreflect.StringKind:
		case protoreflect.BoolKind:
			s.PropertyNames = &Schema{Enum: []any{"true", "false"}}
		default:
			s.PropertyNames = &Schema{Pattern: "^-?[0-9]+$"}
		}
	case fd.IsList():
		s = &Schema{Type: "array", Items: g.value(fd, dir)}
	default:
		s = g.value(fd, dir)
	}
	var notes []string
	if dir == input {
		s, notes = applyRules(s, fd, fieldRules(fd))
	}
	desc := []string{comment(fd)}
	if translatable(fd) {
		if dir == output {
			desc = append(desc, "Translated to the language of the request, Accept-Language.")
		} else {
			desc = append(desc, "Translatable: may be left out when the translations hold it.")
		}
	}
	desc = append(desc, notesText(notes))
	deprecated := false
	if opts, ok := fd.Options().(*descriptorpb.FieldOptions); ok && opts.GetDeprecated() {
		deprecated = true
	}
	if dir == output && !fd.IsList() && !fd.IsMap() && fd.HasPresence() && fd.ContainingOneof() == nil {
		s = nullable(s) // an unset message or proto2 field is written as null
	}
	s.Description = joinText(append([]string{s.Description}, desc...)...)
	s.Deprecated = s.Deprecated || deprecated
	return s
}

// nullable lets a schema be null too.
func nullable(s *Schema) *Schema {
	switch t := s.Type.(type) {
	case string:
		s.Type = []string{t, "null"}
		return s
	case []string:
		s.Type = append(t, "null")
		return s
	}
	if s.Ref == "" && len(s.OneOf) == 0 {
		return s // no type: any value, null included
	}
	return &Schema{OneOf: []*Schema{s, {Type: "null"}}}
}

// value is the schema of one value of a field: the field itself, an item of a list,
// the value of a map.
func (g *generator) value(fd protoreflect.FieldDescriptor, dir direction) *Schema {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return &Schema{Type: "boolean"}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return &Schema{Type: "integer", Format: "int32"}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return &Schema{Type: "integer", Format: "uint32", Minimum: val(0)}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return int64Schema("int64", dir)
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return int64Schema("uint64", dir)
	case protoreflect.FloatKind:
		return &Schema{Type: "number", Format: "float"}
	case protoreflect.DoubleKind:
		return &Schema{Type: "number", Format: "double"}
	case protoreflect.StringKind:
		return &Schema{Type: "string"}
	case protoreflect.BytesKind:
		return &Schema{Type: "string", Format: "byte", ContentEncoding: "base64"}
	case protoreflect.EnumKind:
		return g.enum(fd.Enum())
	default:
		return g.ref(fd.Message(), dir)
	}
}

// int64Schema: JSON writes 64-bit integers as strings and reads a string or a number.
func int64Schema(format string, dir direction) *Schema {
	if dir == input {
		return &Schema{Type: []string{"integer", "string"}, Format: format}
	}
	return &Schema{Type: "string", Format: format}
}

// enum is a reference to the component of an enum: the names of its values, as JSON
// writes them.
func (g *generator) enum(ed protoreflect.EnumDescriptor) *Schema {
	if ed.FullName() == "google.protobuf.NullValue" {
		return &Schema{Type: "null"}
	}
	name := string(ed.FullName())
	if _, ok := g.schemas[name]; !ok {
		s := &Schema{Type: "string"}
		var lines []string
		values := ed.Values()
		for i := range values.Len() {
			v := values.Get(i)
			s.Enum = append(s.Enum, string(v.Name()))
			text := strings.Join(strings.Fields(comment(v)), " ")
			opts, _ := v.Options().(*descriptorpb.EnumValueOptions)
			if opts.GetDeprecated() {
				text = joinSentence("deprecated", text)
			}
			if text != "" {
				lines = append(lines, fmt.Sprintf("- `%s`: %s", v.Name(), text))
			}
		}
		s.Description = joinText(comment(ed), strings.Join(lines, "\n"))
		if opts, ok := ed.Options().(*descriptorpb.EnumOptions); ok && opts.GetDeprecated() {
			s.Deprecated = true
		}
		g.schemas[name] = s
		g.homes[name] = g.home(ed.ParentFile())
	}
	return &Schema{Ref: schemaRef + name}
}

func joinSentence(a, b string) string {
	if b == "" {
		return a
	}
	return a + "; " + b
}

// wellKnownNames are the messages JSON writes in a form of their own.
var wellKnownNames = map[protoreflect.FullName]bool{
	"google.protobuf.Timestamp": true, "google.protobuf.Duration": true, "google.protobuf.FieldMask": true,
	"google.protobuf.Struct": true, "google.protobuf.Value": true, "google.protobuf.ListValue": true,
	"google.protobuf.Any": true, "google.protobuf.Empty": true,
	"google.protobuf.DoubleValue": true, "google.protobuf.FloatValue": true, "google.protobuf.Int64Value": true,
	"google.protobuf.UInt64Value": true, "google.protobuf.Int32Value": true, "google.protobuf.UInt32Value": true,
	"google.protobuf.BoolValue": true, "google.protobuf.StringValue": true, "google.protobuf.BytesValue": true,
}

// wellKnown is the inline schema of a well-known type, nil for any other message.
func (g *generator) wellKnown(md protoreflect.MessageDescriptor, dir direction) *Schema {
	switch md.FullName() {
	case "google.protobuf.Timestamp":
		return &Schema{Type: "string", Format: "date-time"}
	case "google.protobuf.Duration":
		return &Schema{Type: "string", Pattern: `^-?[0-9]+(\.[0-9]{1,9})?s$`, Examples: []any{"1.5s"}}
	case "google.protobuf.FieldMask":
		return &Schema{Type: "string", Description: "Field paths in lowerCamelCase, separated by commas."}
	case "google.protobuf.Struct":
		return &Schema{Type: "object"}
	case "google.protobuf.Value":
		return &Schema{}
	case "google.protobuf.ListValue":
		return &Schema{Type: "array", Items: &Schema{}}
	case "google.protobuf.Any":
		return &Schema{
			Type:       "object",
			Properties: orderedOf("@type", &Schema{Type: "string", Description: "The type of the message, type.googleapis.com/<full name>."}),
			Required:   []string{"@type"},
		}
	case "google.protobuf.Empty":
		s := &Schema{Type: "object"}
		if dir == output {
			s.AdditionalProperties = val(false)
		}
		return s
	}
	if wellKnownNames[md.FullName()] {
		// A wrapper: the value itself.
		return g.value(md.Fields().ByName("value"), dir)
	}
	return nil
}

func orderedOf(kv ...any) *Ordered[*Schema] {
	o := NewOrdered[*Schema]()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1].(*Schema))
	}
	return o
}

// translatable reports whether a field is translated by the i18n module.
func translatable(fd protoreflect.FieldDescriptor) bool {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	return ok && opts != nil && proto.HasExtension(opts, i18npb.E_I18NField)
}

// isFile reports whether a message is a file of a multipart request, the way the
// gateway fills it: content bytes, with a filename or a content type next to them.
func isFile(md protoreflect.MessageDescriptor) bool {
	if md == nil {
		return false
	}
	fields := md.Fields()
	content := fields.ByName("content")
	if content == nil || content.Kind() != protoreflect.BytesKind || content.IsList() {
		return false
	}
	name, typ := fields.ByName("filename"), fields.ByName("content_type")
	return name != nil && name.Kind() == protoreflect.StringKind || typ != nil && typ.Kind() == protoreflect.StringKind
}

func jsonOf(m proto.Message) ([]byte, error) { return protojson.Marshal(m) }

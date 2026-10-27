package convert

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	gwopts "github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2/options"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/aidarbn/platform-go/internal/codemod"
	"github.com/aidarbn/platform-go/internal/openapi"
	"github.com/aidarbn/platform-go/kit/openapipb"
)

// fileConv converts one proto file.
type fileConv struct {
	rel     string
	src     []byte
	pf      *codemod.ProtoFile
	fd      protoreflect.FileDescriptor
	r       *Report
	starts  []int           // the offset of every line
	removed map[int]bool    // openapiv2 options gone from the text, by their start
	added   bool            // a platform option was written
	swagger *gwopts.Swagger // the openapiv2_swagger of the file
	nl      string          // the line ending of the file
	int64s  int             // 64-bit fields whose type: INTEGER went
}

func (c *fileConv) lines() {
	c.starts = []int{0}
	for i, b := range c.src {
		if b == '\n' {
			c.starts = append(c.starts, i+1)
		}
	}
	c.removed = map[int]bool{}
	c.nl = "\n"
	if bytes.Contains(c.src, []byte("\r\n")) {
		c.nl = "\r\n"
	}
}

// line is the 0-based line of an offset.
func (c *fileConv) line(off int) int {
	i, found := slices.BinarySearch(c.starts, off)
	if !found {
		i--
	}
	return i
}

func (c *fileConv) indent(line int) string {
	s := c.starts[line]
	e := s
	for e < len(c.src) && (c.src[e] == ' ' || c.src[e] == '\t') {
		e++
	}
	return string(c.src[s:e])
}

// optionsIn are the options of a name in the text of a descriptor.
func (c *fileConv) optionsIn(name string, d protoreflect.Descriptor) []codemod.ProtoOption {
	loc := c.fd.SourceLocations().ByDescriptor(d)
	var out []codemod.ProtoOption
	for _, o := range c.pf.Options(name) {
		if l := c.line(o.Start); l >= loc.StartLine && l <= loc.EndLine {
			out = append(out, o)
		}
	}
	return out
}

func (c *fileConv) run() error {
	c.messages(c.fd.Messages())
	services := c.fd.Services()
	for i := range services.Len() {
		c.service(services.Get(i))
	}
	c.file()
	c.imports()
	if c.int64s > 0 {
		c.r.note(c.rel, 0, false, "%d 64-bit integer fields lose type: INTEGER: the description says what the gateway writes, a string, and takes a number or a string in a request", c.int64s)
	}
	return nil
}

func (c *fileConv) messages(msgs protoreflect.MessageDescriptors) {
	for i := range msgs.Len() {
		md := msgs.Get(i)
		c.message(md)
		fields := md.Fields()
		for j := range fields.Len() {
			c.field(fields.Get(j))
		}
		c.messages(md.Messages())
	}
}

// field moves openapiv2_field: the description into the comment, the example into
// (platform.openapi.v1.field), a type the generator gives anyway away.
func (c *fileConv) field(fd protoreflect.FieldDescriptor) {
	js, _ := extension(fd.Options(), gwopts.E_Openapiv2Field).(*gwopts.JSONSchema)
	if js == nil {
		return
	}
	at := c.fd.SourceLocations().ByDescriptor(fd).StartLine + 1
	if !c.startsLine(fd) {
		c.r.note(c.rel, at, true, "%s: does not start its line, so a comment cannot document it alone; put it on a line of its own and run again", fd.Name())
		return
	}
	if extension(fd.Options(), openapipb.E_Field) != nil {
		c.r.note(c.rel, at, true, "%s: has (platform.openapi.v1.field) already; merge openapiv2_field into it by hand", fd.Name())
		return
	}
	opts := c.optionsIn(gwField, fd)
	if len(opts) != 1 || !opts[0].Field {
		c.r.note(c.rel, at, true, "%s: the openapiv2_field option is not one [(...) = {...}]; convert it by hand", fd.Name())
		return
	}
	rest := proto.CloneOf(js)
	rest.Description, rest.Example = "", ""
	if len(rest.Type) == 1 && redundantType(rest.Type[0], fd) {
		rest.Type = nil
		c.r.count("field types left to the generator")
		switch fd.Kind() {
		case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
			c.int64s++
		}
	}
	c.comment(fd, "field descriptions", js.GetDescription())
	var parts []string
	if ex := js.GetExample(); ex != "" {
		parts = append(parts, "(platform.openapi.v1.field) = {example: "+quote(ex)+"}")
		c.added = true
		c.r.count("field examples")
	}
	if proto.Size(rest) > 0 {
		parts = append(parts, "("+gwField+") = {"+textOf(rest.ProtoReflect())+"}")
		c.r.note(c.rel, at, true, "%s: openapiv2_field {%s} has no counterpart: make it a protovalidate rule or a comment, then remove it", fd.Name(), textOf(rest.ProtoReflect()))
	} else {
		c.removed[opts[0].Start] = true
	}
	c.replaceFieldOption(opts[0], strings.Join(parts, ", "))
}

// redundantType reports whether an openapiv2 type says what the generator writes
// anyway for the field.
func redundantType(t gwopts.JSONSchema_JSONSchemaSimpleTypes, fd protoreflect.FieldDescriptor) bool {
	if fd.IsList() {
		return t == gwopts.JSONSchema_ARRAY
	}
	switch fd.Kind() {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind, protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		// A 64-bit integer is a string in JSON, a number or a string in a request:
		// the generator says so, which INTEGER did not.
		return t == gwopts.JSONSchema_INTEGER
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return t == gwopts.JSONSchema_NUMBER
	case protoreflect.BoolKind:
		return t == gwopts.JSONSchema_BOOLEAN
	case protoreflect.StringKind, protoreflect.BytesKind, protoreflect.EnumKind:
		return t == gwopts.JSONSchema_STRING
	case protoreflect.MessageKind:
		return t == gwopts.JSONSchema_OBJECT
	}
	return false
}

// message moves openapiv2_schema: the description into the comment, the example into
// (platform.openapi.v1.message).
func (c *fileConv) message(md protoreflect.MessageDescriptor) {
	s, _ := extension(md.Options(), gwopts.E_Openapiv2Schema).(*gwopts.Schema)
	if s == nil {
		return
	}
	at := c.fd.SourceLocations().ByDescriptor(md).StartLine + 1
	if !c.startsLine(md) {
		c.r.note(c.rel, at, true, "%s: does not start its line, so a comment cannot document it alone; put it on a line of its own and run again", md.Name())
		return
	}
	if extension(md.Options(), openapipb.E_Message) != nil {
		c.r.note(c.rel, at, true, "%s: has (platform.openapi.v1.message) already; merge openapiv2_schema into it by hand", md.Name())
		return
	}
	opts := c.statements(gwSchema, md)
	if len(opts) != 1 {
		c.r.note(c.rel, at, true, "%s: the openapiv2_schema option is not one statement; convert it by hand", md.Name())
		return
	}
	rest := proto.CloneOf(s)
	js := rest.GetJsonSchema()
	c.comment(md, "message descriptions", js.GetTitle(), js.GetDescription())
	ex := cmpOr(rest.GetExample(), js.GetExample())
	rest.Example = ""
	if js != nil {
		js.Title, js.Description, js.Example = "", "", ""
		if proto.Size(js) == 0 {
			rest.JsonSchema = nil
		}
	}
	var lines []string
	if ex != "" {
		lines = append(lines, "option (platform.openapi.v1.message) = {example: "+quote(ex)+"};")
		c.added = true
		c.r.count("message examples")
	}
	if proto.Size(rest) > 0 {
		lines = append(lines, "option ("+gwSchema+") = {"+textOf(rest.ProtoReflect())+"};")
		c.r.note(c.rel, at, true, "%s: openapiv2_schema {%s} has no counterpart: convert it by hand", md.Name(), textOf(rest.ProtoReflect()))
	} else {
		c.removed[opts[0].Start] = true
	}
	c.replaceStatement(opts[0], lines)
}

func (c *fileConv) service(sd protoreflect.ServiceDescriptor) {
	if tag, _ := extension(sd.Options(), gwopts.E_Openapiv2Tag).(*gwopts.Tag); tag != nil {
		at := c.fd.SourceLocations().ByDescriptor(sd).StartLine + 1
		if opts := c.statements(gwTag, sd); extension(sd.Options(), openapipb.E_Service) != nil {
			c.r.note(c.rel, at, true, "%s: has (platform.openapi.v1.service) already; merge openapiv2_tag into it by hand", sd.Name())
		} else if len(opts) == 1 {
			rest := proto.CloneOf(tag)
			rest.Name, rest.Description = "", ""
			var lines []string
			if tag.GetName() != "" {
				lines = append(lines, "option (platform.openapi.v1.service) = {tag: "+quote(tag.GetName())+"};")
				c.added = true
				c.r.count("service tags")
			}
			if tag.GetDescription() != "" {
				c.r.note(c.rel, at, false, "%s: the description of the tag %q belongs in the tags of api/openapi/base.yaml", sd.Name(), tag.GetName())
			}
			if proto.Size(rest) > 0 {
				lines = append(lines, "option ("+gwTag+") = {"+textOf(rest.ProtoReflect())+"};")
				c.r.note(c.rel, at, true, "%s: openapiv2_tag {%s} has no counterpart: convert it by hand", sd.Name(), textOf(rest.ProtoReflect()))
			} else {
				c.removed[opts[0].Start] = true
			}
			c.replaceStatement(opts[0], lines)
		} else {
			c.r.note(c.rel, at, true, "%s: the openapiv2_tag option is not one statement; convert it by hand", sd.Name())
		}
	}
	methods := sd.Methods()
	for i := range methods.Len() {
		c.method(methods.Get(i))
	}
}

// method moves openapiv2_operation: summary and description into the comment, the tag
// and x-internal into (platform.openapi.v1.method); multipart content types go, since
// the generator finds file fields itself.
func (c *fileConv) method(m protoreflect.MethodDescriptor) {
	op, _ := extension(m.Options(), gwopts.E_Openapiv2Operation).(*gwopts.Operation)
	if op == nil {
		return
	}
	at := c.fd.SourceLocations().ByDescriptor(m).StartLine + 1
	if !c.startsLine(m) {
		c.r.note(c.rel, at, true, "%s: does not start its line, so a comment cannot document it alone; put it on a line of its own and run again", m.Name())
		return
	}
	if extension(m.Options(), openapipb.E_Method) != nil {
		// A second run, or a method converted by hand: what is left is a person's.
		c.r.note(c.rel, at, true, "%s: has (platform.openapi.v1.method) already; merge openapiv2_operation into it by hand", m.Name())
		return
	}
	opts := c.statements(gwOperation, m)
	if len(opts) != 1 {
		c.r.note(c.rel, at, true, "%s: the openapiv2_operation option is not one statement; convert it by hand", m.Name())
		return
	}
	rest := proto.CloneOf(op)
	rest.Summary, rest.Description = "", ""
	// The summary leads a comment the method has no other of; next to a comment of its
	// own it is the summary option instead of a paragraph at the end.
	loc := c.fd.SourceLocations().ByDescriptor(m)
	had := openapi.CommentText(cmpOr(loc.LeadingComments, loc.TrailingComments))
	parts := []string{op.GetSummary(), op.GetDescription()}
	if had != "" && !says(had, op.GetSummary()) {
		parts = parts[1:]
	}
	final := c.comment(m, "method descriptions", parts...)
	var own []string
	if s := op.GetSummary(); s != "" && openapi.Summary(final) != norm(s) {
		// The comment does not lead with it — another comment came first, or the
		// summary is long: the option keeps it the summary.
		own = append(own, "summary: "+quote(s))
	}
	if tags := op.GetTags(); len(tags) > 0 {
		own = append(own, "tag: "+quote(tags[0]))
		rest.Tags = tags[1:]
		if len(tags) > 1 {
			c.r.note(c.rel, at, true, "%s: a method has one tag, %q; the others stay in openapiv2_operation: %s", m.Name(), tags[0], strings.Join(tags[1:], ", "))
		} else {
			rest.Tags = nil
		}
		c.r.count("method tags")
	}
	multipart := false
	keep := func(types []string) []string {
		var out []string
		for _, t := range types {
			switch t {
			case "multipart/form-data":
				multipart = true
			case "application/json":
			default:
				out = append(out, t)
			}
		}
		return out
	}
	rest.Consumes, rest.Produces = keep(rest.GetConsumes()), keep(rest.GetProduces())
	if v, ok := rest.GetExtensions()["x-multipart-request"]; ok && v != nil {
		multipart = true
		delete(rest.Extensions, "x-multipart-request")
		c.r.note(c.rel, at, false, "%s: x-multipart-request is gone; the generator describes the multipart body from the file fields, its example is not kept", m.Name())
	}
	if v, ok := rest.GetExtensions()["x-internal"]; ok && v.GetBoolValue() {
		delete(rest.Extensions, "x-internal")
		own = append(own, "hidden: true")
		c.r.count("hidden methods")
	}
	if len(rest.Extensions) == 0 {
		rest.Extensions = nil
	}
	if multipart {
		c.r.count("multipart content types left to the generator")
		if !hasFile(m.Input()) {
			c.r.note(c.rel, at, true, "%s: says multipart/form-data, but %s has no file field (content bytes with a filename or a content_type): the generator describes a JSON body only", m.Name(), m.Input().Name())
		}
	}
	var lines []string
	if len(own) > 0 {
		lines = append(lines, "option (platform.openapi.v1.method) = {"+strings.Join(own, ", ")+"};")
		c.added = true
	}
	if proto.Size(rest) > 0 {
		lines = append(lines, "option ("+gwOperation+") = {"+textOf(rest.ProtoReflect())+"};")
		c.r.note(c.rel, at, true, "%s: openapiv2_operation {%s} has no counterpart: convert it by hand", m.Name(), textOf(rest.ProtoReflect()))
	} else {
		c.removed[opts[0].Start] = true
	}
	c.replaceStatement(opts[0], lines)
}

func hasFile(md protoreflect.MessageDescriptor) bool {
	fields := md.Fields()
	for i := range fields.Len() {
		if fd := fields.Get(i); !fd.IsMap() && openapi.IsFile(fd.Message()) {
			return true
		}
	}
	return false
}

// file takes openapiv2_swagger out of the file: the base gets it.
func (c *fileConv) file() {
	sw, _ := extension(c.fd.Options(), gwopts.E_Openapiv2Swagger).(*gwopts.Swagger)
	if sw == nil {
		return
	}
	var opts []codemod.ProtoOption
	for _, o := range c.pf.Options(gwSwagger) {
		if !o.Field {
			opts = append(opts, o)
		}
	}
	if len(opts) != 1 {
		c.r.note(c.rel, 0, true, "the openapiv2_swagger option is not one statement; convert it by hand")
		return
	}
	at := c.line(opts[0].Start) + 1
	c.swagger = sw
	rest := proto.CloneOf(sw)
	rest.Info, rest.Security, rest.Tags, rest.SecurityDefinitions = nil, nil, nil, nil
	rest.Swagger, rest.Schemes, rest.Consumes, rest.Produces, rest.ExternalDocs = "", nil, nil, nil, nil
	delete(rest.Extensions, "x-tagGroups")
	if len(rest.Extensions) == 0 {
		rest.Extensions = nil
	}
	var lines []string
	if proto.Size(rest) > 0 {
		lines = append(lines, "option ("+gwSwagger+") = {"+textOf(rest.ProtoReflect())+"};")
		c.r.note(c.rel, at, true, "openapiv2_swagger {%s} has no counterpart: move it into api/openapi/base.yaml by hand", textOf(rest.ProtoReflect()))
	} else {
		c.removed[opts[0].Start] = true
	}
	c.r.count("files with openapiv2_swagger")
	c.replaceStatement(opts[0], lines)
	if len(lines) == 0 && c.fd.Messages().Len() == 0 && c.fd.Services().Len() == 0 && c.fd.Enums().Len() == 0 && c.fd.Extensions().Len() == 0 {
		c.r.note(c.rel, 0, true, "the file declares nothing but the options it had: delete it and its imports")
	}
}

// imports drops the import of the openapiv2 options when none is left in the file and
// adds the one of the platform's options when the file uses them now.
func (c *fileConv) imports() {
	left := false
	for _, o := range c.pf.Options(strings.TrimSuffix(gwPrefix, ".")) {
		if !c.removed[o.Start] {
			left = true
		}
	}
	imports := c.pf.Imports()
	have := slices.ContainsFunc(imports, func(im codemod.ProtoImport) bool { return im.Path == ownImport })
	for _, im := range imports {
		if (im.Path != gwImport && im.Path != gwImportTypes) || left {
			continue
		}
		if c.added && !have {
			// The import of the old options becomes the one of the new ones.
			c.pf.Replace(im.Start, im.End, `import "`+ownImport+`";`)
			have = true
			continue
		}
		c.deleteLines(im.Start, im.End)
	}
	if c.added && !have && len(imports) > 0 {
		last := imports[len(imports)-1]
		end := c.lineEnd(last.End)
		c.pf.Replace(end, end, c.indent(c.line(last.Start))+`import "`+ownImport+`";`+"\n")
	}
}

// comment puts texts before a descriptor as its leading comment. The comment it has
// stays, and a text it does not say yet — as a whole, a paragraph or a line of it —
// becomes its last paragraph. It returns the comment the descriptor ends up with.
func (c *fileConv) comment(d protoreflect.Descriptor, kind string, texts ...string) string {
	loc := c.fd.SourceLocations().ByDescriptor(d)
	leading := openapi.CommentText(loc.LeadingComments)
	trailing := openapi.CommentText(loc.TrailingComments)
	has := leading
	if has == "" {
		has = trailing
	}
	var missing []string
	for _, t := range texts {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		if says(has, t) {
			c.r.count(kind + " already in the comment")
			continue
		}
		missing = append(missing, t)
	}
	if len(missing) == 0 {
		return has
	}
	text := strings.Join(missing, "\n\n")
	line := loc.StartLine
	indent := c.indent(line)
	block := commentBlock(indent, text, c.nl)
	final := text
	switch {
	case leading != "":
		block = indent + "//" + c.nl + block
		final = leading + "\n\n" + text
		c.r.note(c.rel, line+1, false, "%s: the description is added to the comment it had", d.Name())
		c.r.count(kind + " added to a comment")
	case trailing != "":
		c.r.note(c.rel, line+1, false, "%s: the description goes before it; its trailing comment no longer documents it", d.Name())
		c.r.count(kind)
	default:
		c.r.count(kind)
	}
	start := c.starts[line]
	c.pf.Replace(start, start, block)
	return final
}

// says reports whether a comment says a text already: all of it, one of its
// paragraphs or one of its lines, white space aside. A text inside a longer line does
// not count: "id" is not said by "The provider id".
func says(comment, text string) bool {
	n := norm(text)
	if n == "" || norm(comment) == n {
		return n == "" || norm(comment) == n
	}
	for _, p := range strings.Split(comment, "\n\n") {
		if norm(p) == n {
			return true
		}
	}
	for _, l := range strings.Split(comment, "\n") {
		if norm(l) == n {
			return true
		}
	}
	return false
}

// startsLine reports whether a declaration is the first thing on its line: a comment
// put at the start of the line documents it and nothing else.
func (c *fileConv) startsLine(d protoreflect.Descriptor) bool {
	loc := c.fd.SourceLocations().ByDescriptor(d)
	start := c.starts[loc.StartLine]
	end := min(start+loc.StartColumn, c.lineEnd(start))
	return strings.TrimSpace(string(c.src[start:end])) == ""
}

func commentBlock(indent, text, nl string) string {
	var b strings.Builder
	for l := range strings.SplitSeq(text, "\n") {
		l = strings.TrimRight(l, " \t\r")
		b.WriteString(indent + "//")
		if l != "" {
			b.WriteString(" " + l)
		}
		b.WriteString(nl)
	}
	return b.String()
}

// statements are the statement options of a name inside a descriptor, the ones of its
// children left out.
func (c *fileConv) statements(name string, d protoreflect.Descriptor) []codemod.ProtoOption {
	var out []codemod.ProtoOption
	for _, o := range c.optionsIn(name, d) {
		if !o.Field && c.owner(o, d) {
			out = append(out, o)
		}
	}
	return out
}

// owner reports whether an option sits in a descriptor itself, not in a message or
// a method nested in it.
func (c *fileConv) owner(o codemod.ProtoOption, d protoreflect.Descriptor) bool {
	l := c.line(o.Start)
	inside := func(x protoreflect.Descriptor) bool {
		loc := c.fd.SourceLocations().ByDescriptor(x)
		return l >= loc.StartLine && l <= loc.EndLine
	}
	switch d := d.(type) {
	case protoreflect.MessageDescriptor:
		for i := range d.Messages().Len() {
			if inside(d.Messages().Get(i)) {
				return false
			}
		}
	case protoreflect.ServiceDescriptor:
		for i := range d.Methods().Len() {
			if inside(d.Methods().Get(i)) {
				return false
			}
		}
	}
	return true
}

// replaceFieldOption puts text in place of a field option; an empty text removes the
// option with its comma, or the brackets when it was alone in them.
func (c *fileConv) replaceFieldOption(o codemod.ProtoOption, text string) {
	if text != "" {
		c.pf.Replace(o.Start, o.End, text)
		return
	}
	src := c.src
	j := skipSpace(src, o.End)
	if j < len(src) && src[j] == ',' {
		c.pf.Replace(o.Start, skipSpace(src, j+1), "")
		return
	}
	i := c.before(o.Start)
	switch {
	case i >= 0 && src[i] == ',' && strings.Contains(string(src[i+1:o.Start]), "//"):
		// A comment between the comma and the option stays: the comma goes, and the
		// option with its own line.
		c.pf.Replace(i, i+1, "")
		start := o.Start
		if ls := c.starts[c.line(start)]; strings.TrimSpace(string(src[ls:start])) == "" && ls > 0 {
			start = ls - 1
			if start > 0 && src[start-1] == '\r' {
				start--
			}
		}
		c.pf.Replace(start, o.End, "")
	case i >= 0 && src[i] == ',':
		c.pf.Replace(i, o.End, "")
	case i >= 0 && src[i] == '[' && j < len(src) && src[j] == ']':
		b := i
		for b > 0 && (src[b-1] == ' ' || src[b-1] == '\t') {
			b--
		}
		c.pf.Replace(b, j+1, "")
	default:
		c.pf.Replace(o.Start, o.End, "")
	}
}

// replaceStatement puts statements in place of an option statement, at its
// indentation; none removes it with its line and the comment right above it, which
// documented the option.
func (c *fileConv) replaceStatement(o codemod.ProtoOption, lines []string) {
	if len(lines) == 0 {
		// The comment right above goes too when it is about the option: it would
		// document whatever comes next.
		start := o.Start
		var above []string
		first := c.line(start)
		for l := first - 1; l >= 0; l-- {
			text := strings.TrimSpace(string(c.src[c.starts[l]:c.lineEnd(c.starts[l])]))
			if !strings.HasPrefix(text, "//") {
				break
			}
			above = append(above, text)
			first = l
		}
		if about := strings.ToLower(strings.Join(above, " ")); len(above) > 0 && (strings.Contains(about, "openapi") || strings.Contains(about, "swagger")) {
			start = c.starts[first]
		} else if len(above) > 0 {
			c.r.note(c.rel, first+1, false, "the comment above the removed openapiv2 option stays: check what it documents now")
		}
		c.deleteLines(start, o.End)
		return
	}
	indent := c.indent(c.line(o.Start))
	c.pf.Replace(o.Start, o.End, strings.Join(lines, "\n"+indent))
}

// deleteLines removes a range, and its lines when nothing else is on them.
func (c *fileConv) deleteLines(start, end int) {
	ls := c.starts[c.line(start)]
	if strings.TrimSpace(string(c.src[ls:start])) == "" {
		start = ls
	}
	e := end
	for e < len(c.src) && (c.src[e] == ' ' || c.src[e] == '\t' || c.src[e] == '\r') {
		e++
	}
	if e < len(c.src) && c.src[e] == '\n' && start == ls {
		end = e + 1
		// A blank line on both sides would leave two: one of them goes.
		nl := len(c.nl)
		if c.blankBefore(start) && end+nl <= len(c.src) && string(c.src[end:end+nl]) == c.nl {
			end += nl
		}
	}
	c.pf.Replace(start, end, "")
}

// blankBefore reports whether the line before the one starting at an offset is empty.
func (c *fileConv) blankBefore(lineStart int) bool {
	l := c.line(lineStart)
	return l > 0 && strings.TrimSpace(string(c.src[c.starts[l-1]:lineStart])) == ""
}

// lineEnd is the offset after the newline that ends the line of an offset.
func (c *fileConv) lineEnd(off int) int {
	l := c.line(off)
	if l+1 < len(c.starts) {
		return c.starts[l+1]
	}
	return len(c.src)
}

// before is the offset of the last character before an offset that is neither white
// space nor in a // comment of its own line.
func (c *fileConv) before(off int) int {
	i := off - 1
	for {
		for i >= 0 && isSpace(c.src[i]) {
			i--
		}
		if i < 0 {
			return i
		}
		ls := c.starts[c.line(i)]
		if k := strings.Index(string(c.src[ls:i+1]), "//"); k >= 0 && !strings.Contains(string(c.src[ls:ls+k]), `"`) {
			i = ls + k - 1
			continue
		}
		return i
	}
}

func skipSpace(src []byte, i int) int {
	for i < len(src) && isSpace(src[i]) {
		i++
	}
	return i
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

func norm(s string) string { return strings.Join(strings.Fields(s), " ") }

func joinText(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n\n" + b
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// extension is the value of an option extension, nil without it.
func extension(opts proto.Message, ext protoreflect.ExtensionType) any {
	if opts == nil || !proto.HasExtension(opts, ext) {
		return nil
	}
	return proto.GetExtension(opts, ext)
}

// quote writes a string as a proto string literal; text other than ASCII stays as it is.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// textOf writes a message in the text format of option values, in the order of its
// fields, map keys sorted: the same message always gives the same text.
func textOf(m protoreflect.Message) string {
	var parts []string
	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if !m.Has(fd) {
			continue
		}
		v := m.Get(fd)
		name := string(fd.Name())
		switch {
		case fd.IsMap():
			mp := v.Map()
			keys := map[string]protoreflect.MapKey{}
			mp.Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool {
				keys[k.String()] = k
				return true
			})
			for _, k := range slices.Sorted(maps.Keys(keys)) {
				key := scalarText(fd.MapKey(), keys[k].Value())
				parts = append(parts, name+": {key: "+key+" value: "+scalarText(fd.MapValue(), mp.Get(keys[k]))+"}")
			}
		case fd.IsList():
			l := v.List()
			for j := range l.Len() {
				parts = append(parts, name+": "+scalarText(fd, l.Get(j)))
			}
		default:
			parts = append(parts, name+": "+scalarText(fd, v))
		}
	}
	return strings.Join(parts, " ")
}

func scalarText(fd protoreflect.FieldDescriptor, v protoreflect.Value) string {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return quote(v.String())
	case protoreflect.BytesKind:
		return quote(string(v.Bytes()))
	case protoreflect.EnumKind:
		if ev := fd.Enum().Values().ByNumber(v.Enum()); ev != nil {
			return string(ev.Name())
		}
		return strconv.Itoa(int(v.Enum()))
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return "{" + textOf(v.Message()) + "}"
	}
	return fmt.Sprint(v.Interface())
}

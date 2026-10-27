package codemod

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	gnosticOption = "gnostic.openapi.v3"
	gnosticImport = "gnostic/openapi/v3/annotations.proto"
	platformAPI   = "platform/openapi/v1/options.proto"
)

// GnosticOptions follows v0.10.0, where the platform's own OpenAPI generator is the
// default and reads its own options instead of gnostic annotations: a summary and a
// tag of an operation become (platform.openapi.v1.method), descriptions and titles
// become proto comments, examples become (platform.openapi.v1.field) and
// (platform.openapi.v1.message), deprecated an option of the method. The rest — the
// document option, formats, patterns, responses — goes to a person. Projects that keep
// the connect generator keep their annotations.
func GnosticOptions() Codemod {
	return Codemod{
		Version:     "v0.10.0",
		Name:        "openapi.gnostic",
		Description: "gnostic OpenAPI annotations become the options of the platform's generator",
		Proto:       fixGnostic,
		Skip:        func(p Project) bool { return p.OpenAPIGenerator != "platform" },
	}
}

func fixGnostic(f *ProtoFile) {
	opts := f.Options(gnosticOption)
	if len(opts) == 0 {
		// Annotations removed by hand leave the import, which buf cannot resolve
		// without the dependency.
		f.fixImports(false, false)
		return
	}
	nl := "\n"
	if strings.Contains(string(f.src), "\r\n") {
		nl = "\r\n"
	}
	added, left := false, false
	for _, o := range opts {
		kind := strings.TrimPrefix(o.Name, gnosticOption+".")
		if strings.Contains(f.Text(o.Start, o.ValueStart), ").") {
			f.Report(o.Start, fmt.Sprintf("(%s) set field by field: convert it to the options of platform/openapi/v1/options.proto by hand", o.Name))
			left = true
			continue
		}
		msg, err := parseText(f.Text(o.ValueStart, o.ValueEnd))
		if err != nil {
			f.Report(o.Start, fmt.Sprintf("(%s): %v; convert it by hand", o.Name, err))
			left = true
			continue
		}
		decl := f.Owner(o)
		if kind == "document" || decl.Kind == "file" {
			f.Report(o.Start, "(gnostic.openapi.v3.document): info, servers, security and tags go into api/openapi/base.yaml by hand")
			left = true
			continue
		}
		if !f.startsLine(decl.Start) {
			f.Report(o.Start, fmt.Sprintf("%s does not start its line, so a comment cannot document it alone: convert it by hand", decl.Name))
			left = true
			continue
		}
		if f.hasPlatformOption(decl) {
			f.Report(o.Start, fmt.Sprintf("%s has an option of platform/openapi/v1/options.proto already: merge (%s) into it by hand", decl.Name, o.Name))
			left = true
			continue
		}
		var own []string
		var statements []string
		var texts []string
		switch {
		case kind == "operation" && decl.Kind == "rpc":
			summary, description := msg.take("summary"), msg.take("description")
			had := f.CommentAbove(decl.Start)
			if had == "" {
				texts = append(texts, summary, description)
			} else {
				if summary != "" {
					own = append(own, "summary: "+strconv.Quote(summary))
				}
				texts = append(texts, description)
			}
			if tags := msg.takeList("tags"); len(tags) > 0 {
				own = append(own, "tag: "+strconv.Quote(tags[0]))
				if len(tags) > 1 {
					msg.put("tags", tags[1:])
					f.Report(o.Start, fmt.Sprintf("%s: a method has one tag, %q; the others stay: %s", decl.Name, tags[0], strings.Join(tags[1:], ", ")))
				}
			}
			if msg.take("deprecated") == "true" && !f.deprecated(decl) {
				statements = append(statements, "option deprecated = true;")
			}
			if len(own) > 0 {
				statements = append([]string{"option (platform.openapi.v1.method) = {" + strings.Join(own, ", ") + "};"}, statements...)
			}
		case kind == "schema" && decl.Kind == "message":
			texts = append(texts, msg.take("title"), msg.take("description"))
			if ex, ok := msg.example(); ok {
				statements = append(statements, "option (platform.openapi.v1.message) = {example: "+strconv.Quote(ex)+"};")
			}
		case kind == "property" && decl.Kind == "field":
			texts = append(texts, msg.take("title"), msg.take("description"))
			if ex, ok := msg.example(); ok {
				own = append(own, "(platform.openapi.v1.field) = {example: "+strconv.Quote(ex)+"}")
			}
		default:
			f.Report(o.Start, fmt.Sprintf("(%s) on a %s: convert it by hand", o.Name, decl.Kind))
			left = true
			continue
		}
		f.addComment(decl.Start, texts, nl)
		if !msg.empty() {
			f.Report(o.Start, fmt.Sprintf("(%s) {%s} has no counterpart in the platform's options: make it a protovalidate rule or a comment, then remove it", o.Name, msg))
			left = true
			rest := "(" + o.Name + ") = {" + msg.String() + "}"
			if o.Field {
				own = append(own, rest)
			} else {
				statements = append(statements, "option "+rest+";")
			}
		}
		if len(own) > 0 || len(statements) > 0 {
			added = true
		}
		switch {
		case o.Field && len(own) > 0:
			f.Replace(o.Start, o.End, strings.Join(own, ", "))
		case o.Field:
			f.removeFieldOption(o)
		case len(statements) > 0:
			f.Replace(o.Start, o.End, strings.Join(statements, nl+f.indentOf(o.Start)))
		default:
			f.removeLine(o.Start, o.End)
		}
	}
	if left {
		f.Report(0, "gnostic annotations stay in this file: until they are converted, add buf.build/gnostic/gnostic to api.buf.deps in platformgo.yaml, or buf cannot resolve their import")
	}
	f.fixImports(added, left)
}

// hasPlatformOption reports whether a declaration has an option of the platform's
// OpenAPI options already: a second one would not compile.
func (f *ProtoFile) hasPlatformOption(d Declaration) bool {
	for _, o := range f.Options("platform.openapi.v1") {
		if f.Owner(o).Start == d.Start {
			return true
		}
	}
	return false
}

// deprecated reports whether a block says option deprecated = true itself.
func (f *ProtoFile) deprecated(d Declaration) bool {
	end := f.BlockEnd(d.Start)
	if end < 0 {
		return false
	}
	return deprecatedOption.MatchString(string(f.src[d.Start:end]))
}

var deprecatedOption = regexp.MustCompile(`option\s+deprecated\s*=\s*true`)

// startsLine reports whether nothing but white space comes before an offset on its line.
func (f *ProtoFile) startsLine(off int) bool {
	ls := strings.LastIndexByte(string(f.src[:off]), '\n') + 1
	return strings.TrimSpace(string(f.src[ls:off])) == ""
}

func (f *ProtoFile) indentOf(off int) string {
	ls := strings.LastIndexByte(string(f.src[:off]), '\n') + 1
	e := ls
	for e < len(f.src) && (f.src[e] == ' ' || f.src[e] == '\t') {
		e++
	}
	return string(f.src[ls:e])
}

// addComment puts the texts the comment above a declaration does not say yet at its
// end, as paragraphs.
func (f *ProtoFile) addComment(start int, texts []string, nl string) {
	had := f.CommentAbove(start)
	var missing []string
	for _, t := range texts {
		if t = strings.TrimSpace(t); t != "" && !commentSays(had, t) {
			missing = append(missing, t)
		}
	}
	if len(missing) == 0 {
		return
	}
	indent := f.indentOf(start)
	var b strings.Builder
	if had != "" {
		b.WriteString(indent + "//" + nl)
	}
	for _, line := range strings.Split(strings.Join(missing, "\n\n"), "\n") {
		b.WriteString(indent + "//")
		if line = strings.TrimRight(line, " \t\r"); line != "" {
			b.WriteString(" " + line)
		}
		b.WriteString(nl)
	}
	ls := strings.LastIndexByte(string(f.src[:start]), '\n') + 1
	f.Replace(ls, ls, b.String())
}

// commentSays reports whether a comment says a text: all of it, a paragraph or a line.
func commentSays(comment, text string) bool {
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	n := norm(text)
	if norm(comment) == n {
		return true
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

// removeFieldOption removes a field option with its comma, or the brackets when it was
// alone in them.
func (f *ProtoFile) removeFieldOption(o ProtoOption) {
	src := f.src
	j := o.End
	for j < len(src) && isProtoSpace(src[j]) {
		j++
	}
	if j < len(src) && src[j] == ',' {
		k := j + 1
		for k < len(src) && isProtoSpace(src[k]) {
			k++
		}
		f.Replace(o.Start, k, "")
		return
	}
	i := o.Start - 1
	for i >= 0 && isProtoSpace(src[i]) {
		i--
	}
	switch {
	case i >= 0 && src[i] == ',':
		f.Replace(i, o.End, "")
	case i >= 0 && src[i] == '[' && j < len(src) && src[j] == ']':
		b := i
		for b > 0 && (src[b-1] == ' ' || src[b-1] == '\t') {
			b--
		}
		f.Replace(b, j+1, "")
	default:
		f.Replace(o.Start, o.End, "")
	}
}

// removeLine removes a statement with its line when nothing else is on it.
func (f *ProtoFile) removeLine(start, end int) {
	ls := strings.LastIndexByte(string(f.src[:start]), '\n') + 1
	if strings.TrimSpace(string(f.src[ls:start])) == "" {
		start = ls
	}
	e := end
	for e < len(f.src) && (f.src[e] == ' ' || f.src[e] == '\t' || f.src[e] == '\r') {
		e++
	}
	if e < len(f.src) && f.src[e] == '\n' && start == ls {
		end = e + 1
	}
	f.Replace(start, end, "")
}

// fixImports drops the gnostic import when no annotation is left and imports the
// platform's options when the file uses them now.
func (f *ProtoFile) fixImports(added, left bool) {
	imports := f.Imports()
	have := slices.ContainsFunc(imports, func(im ProtoImport) bool { return im.Path == platformAPI })
	for _, im := range imports {
		if im.Path != gnosticImport || left {
			continue
		}
		if added && !have {
			f.Replace(im.Start, im.End, `import "`+platformAPI+`";`)
			have = true
			continue
		}
		f.removeLine(im.Start, im.End)
	}
	if added && !have && len(imports) > 0 {
		last := imports[len(imports)-1]
		end := strings.IndexByte(string(f.src[last.End:]), '\n')
		at := len(f.src)
		if end >= 0 {
			at = last.End + end + 1
		}
		f.Replace(at, at, f.indentOf(last.Start)+`import "`+platformAPI+`";`+"\n")
	}
}

func isProtoSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// textMsg is an option value in the text format, parsed as far as the codemod needs:
// fields in their order, strings unquoted, nested messages kept.
type textMsg struct{ fields []textField }

type textField struct {
	name  string
	value textValue
}

type textValue struct {
	scalar string   // an identifier or a number, as written
	str    *string  // a string, unquoted
	msg    *textMsg // a message
	list   []textValue
	isList bool
}

func (m *textMsg) empty() bool { return len(m.fields) == 0 }

// take removes a scalar field and returns its value.
func (m *textMsg) take(name string) string {
	for i, f := range m.fields {
		if f.name == name && f.value.msg == nil && !f.value.isList {
			m.fields = slices.Delete(m.fields, i, i+1)
			if f.value.str != nil {
				return *f.value.str
			}
			return f.value.scalar
		}
	}
	return ""
}

// takeList removes a repeated string field, written as a list or field by field.
func (m *textMsg) takeList(name string) []string {
	var out []string
	keep := m.fields[:0]
	for _, f := range m.fields {
		if f.name != name {
			keep = append(keep, f)
			continue
		}
		vals := []textValue{f.value}
		if f.value.isList {
			vals = f.value.list
		}
		var other []textValue
		for _, v := range vals {
			if v.str != nil {
				out = append(out, *v.str)
			} else {
				other = append(other, v)
			}
		}
		if len(other) > 0 {
			// What is not a string stays, for a person.
			keep = append(keep, textField{name: name, value: textValue{list: other, isList: true}})
		}
	}
	m.fields = keep
	return out
}

func (m *textMsg) put(name string, values []string) {
	var list []textValue
	for _, v := range values {
		list = append(list, textValue{str: &v})
	}
	m.fields = append(m.fields, textField{name: name, value: textValue{list: list, isList: true}})
}

// example takes example: {yaml: "..."} of gnostic, an Any with YAML, as JSON.
func (m *textMsg) example() (string, bool) {
	for i, f := range m.fields {
		if f.name != "example" || f.value.msg == nil {
			continue
		}
		inner := f.value.msg
		if len(inner.fields) != 1 || inner.fields[0].name != "yaml" || inner.fields[0].value.str == nil {
			return "", false
		}
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(*inner.fields[0].value.str), &n); err != nil {
			return "", false
		}
		v, ok := yamlValue(&n)
		if !ok {
			return "", false
		}
		b, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		m.fields = slices.Delete(m.fields, i, i+1)
		return string(b), true
	}
	return "", false
}

// yamlValue is a YAML value as JSON would hold it. A scalar keeps the text it was
// written with unless YAML says it is a number, a bool or null: a date stays the date
// it was, not a time of day in UTC.
func yamlValue(n *yaml.Node) (any, bool) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) != 1 {
			return nil, false
		}
		return yamlValue(n.Content[0])
	case yaml.MappingNode:
		m := map[string]any{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			v, ok := yamlValue(n.Content[i+1])
			if !ok {
				return nil, false
			}
			m[n.Content[i].Value] = v
		}
		return m, true
	case yaml.SequenceNode:
		var l []any
		for _, c := range n.Content {
			v, ok := yamlValue(c)
			if !ok {
				return nil, false
			}
			l = append(l, v)
		}
		return l, true
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!int", "!!float":
			return json.Number(n.Value), true
		case "!!bool":
			return n.Value == "true", true
		case "!!null":
			return nil, true
		}
		return n.Value, true
	}
	return nil, false
}

// String writes the message back in the text format, the fields in their order.
func (m *textMsg) String() string {
	var parts []string
	for _, f := range m.fields {
		parts = append(parts, f.name+": "+f.value.String())
	}
	return strings.Join(parts, " ")
}

func (v textValue) String() string {
	switch {
	case v.isList:
		var items []string
		for _, x := range v.list {
			items = append(items, x.String())
		}
		return "[" + strings.Join(items, ", ") + "]"
	case v.msg != nil:
		return "{" + v.msg.String() + "}"
	case v.str != nil:
		return strconv.Quote(*v.str)
	}
	return v.scalar
}

// parseText parses an option value: a message in braces, or a scalar.
func parseText(s string) (*textMsg, error) {
	p := &textParser{src: s}
	p.next()
	if p.tok != "{" {
		return nil, fmt.Errorf("the value is not a message")
	}
	p.next()
	m, err := p.message("}")
	if err != nil {
		return nil, err
	}
	return m, nil
}

type textParser struct {
	src string
	pos int
	tok string // the current token; a string token keeps its quotes
	err error
}

func (p *textParser) next() {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			p.pos++
			continue
		case c == '#' || strings.HasPrefix(p.src[p.pos:], "//"):
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
			continue
		}
		break
	}
	if p.pos >= len(p.src) {
		p.tok = ""
		return
	}
	start := p.pos
	c := p.src[p.pos]
	switch {
	case c == '"' || c == '\'':
		p.pos++
		for p.pos < len(p.src) && p.src[p.pos] != c {
			if p.src[p.pos] == '\\' {
				p.pos++
			}
			p.pos++
		}
		p.pos++
	case strings.ContainsRune("{}[]:,;<>", rune(c)):
		p.pos++
	default:
		for p.pos < len(p.src) && !strings.ContainsRune(" \t\r\n{}[]:,;<>\"'#", rune(p.src[p.pos])) {
			p.pos++
		}
	}
	p.tok = p.src[start:min(p.pos, len(p.src))]
}

func (p *textParser) message(end string) (*textMsg, error) {
	m := &textMsg{}
	for p.tok != end {
		if p.tok == "" {
			return nil, fmt.Errorf("unexpected end of the value")
		}
		name := p.tok
		p.next()
		if p.tok == ":" {
			p.next()
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		m.fields = append(m.fields, textField{name: name, value: v})
		if p.tok == "," || p.tok == ";" {
			p.next()
		}
	}
	p.next()
	return m, nil
}

func (p *textParser) value() (textValue, error) {
	switch p.tok {
	case "{", "<":
		end := map[string]string{"{": "}", "<": ">"}[p.tok]
		p.next()
		m, err := p.message(end)
		return textValue{msg: m}, err
	case "[":
		p.next()
		v := textValue{isList: true}
		for p.tok != "]" {
			if p.tok == "" {
				return v, fmt.Errorf("unexpected end of a list")
			}
			x, err := p.value()
			if err != nil {
				return v, err
			}
			v.list = append(v.list, x)
			if p.tok == "," {
				p.next()
			}
		}
		p.next()
		return v, nil
	case "":
		return textValue{}, fmt.Errorf("unexpected end of the value")
	}
	if c := p.tok[0]; c == '"' || c == '\'' {
		var b strings.Builder
		for p.tok != "" && (p.tok[0] == '"' || p.tok[0] == '\'') {
			s, err := unquoteText(p.tok)
			if err != nil {
				return textValue{}, err
			}
			b.WriteString(s)
			p.next()
		}
		s := b.String()
		return textValue{str: &s}, nil
	}
	v := textValue{scalar: p.tok}
	p.next()
	return v, nil
}

// unquoteText unquotes a string of the text format: C escapes, either quote.
func unquoteText(s string) (string, error) {
	if len(s) < 2 {
		return "", fmt.Errorf("bad string %s", s)
	}
	body := s[1 : len(s)-1]
	if s[0] == '\'' {
		body = strings.ReplaceAll(body, `"`, `\"`)
	}
	// Escapes of the text format Go does not have.
	body = strings.NewReplacer(`\'`, `'`, `\?`, `?`).Replace(body)
	out, err := strconv.Unquote(`"` + body + `"`)
	if err != nil {
		return "", fmt.Errorf("bad string %s", s)
	}
	return out, nil
}

package openapi

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// protovalidate rules as JSON Schema keywords. A rule with a keyword of its own becomes
// that keyword; a rule JSON Schema cannot say — CEL, byte lengths, times relative to
// now — becomes a line of the description, so the reader still sees it.

func fieldRules(fd protoreflect.FieldDescriptor) *validate.FieldRules {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || opts == nil || !proto.HasExtension(opts, validate.E_Field) {
		return nil
	}
	r, _ := proto.GetExtension(opts, validate.E_Field).(*validate.FieldRules)
	if r.GetIgnore() == validate.Ignore_IGNORE_ALWAYS {
		return nil
	}
	return r
}

func messageRules(md protoreflect.MessageDescriptor) *validate.MessageRules {
	opts, ok := md.Options().(*descriptorpb.MessageOptions)
	if !ok || opts == nil || !proto.HasExtension(opts, validate.E_Message) {
		return nil
	}
	r, _ := proto.GetExtension(opts, validate.E_Message).(*validate.MessageRules)
	return r
}

func oneofRequired(od protoreflect.OneofDescriptor) bool {
	opts, ok := od.Options().(*descriptorpb.OneofOptions)
	if !ok || opts == nil || !proto.HasExtension(opts, validate.E_Oneof) {
		return false
	}
	r, _ := proto.GetExtension(opts, validate.E_Oneof).(*validate.OneofRules)
	return r.GetRequired()
}

// required reports whether protovalidate requires the field.
func required(fd protoreflect.FieldDescriptor) bool { return fieldRules(fd).GetRequired() }

// celNotes are the CEL rules of a field or a message, as lines of the description.
func celNotes(rules []*validate.Rule, expressions []string) []string {
	var out []string
	for _, r := range rules {
		switch {
		case r.GetMessage() != "":
			out = append(out, r.GetMessage())
		case r.GetExpression() != "":
			out = append(out, "`"+r.GetExpression()+"`")
		}
	}
	for _, e := range expressions {
		out = append(out, "`"+e+"`")
	}
	return out
}

// notesText is the paragraph of rules a description ends with.
func notesText(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return "Rules:\n- " + strings.Join(notes, "\n- ")
}

// applyRules puts the rules of a field on its schema: the array or the map for a
// repeated field or a map, the value schema otherwise. It returns the rules left for
// the description.
func applyRules(s *Schema, fd protoreflect.FieldDescriptor, r *validate.FieldRules) (*Schema, []string) {
	if r == nil {
		return s, nil
	}
	notes := applyFieldRules(s, fd, r)
	if r.GetIgnore() == validate.Ignore_IGNORE_IF_ZERO_VALUE && (!fd.HasPresence() || fd.IsList() || fd.IsMap()) {
		// The rules do not apply to the zero value: the field may be empty, 0 or
		// false and still pass. A field with presence is left out instead.
		if zero, ok := zeroValue(fd, s); ok {
			desc := s.Description
			s.Description = ""
			s = &Schema{AnyOf: []*Schema{s, zero}, Description: desc}
			notes = append(notes, "the rules do not apply to an empty value")
		}
	}
	return s, notes
}

// zeroValue is the schema of the zero value of a field, which IGNORE_IF_ZERO_VALUE
// lets through.
func zeroValue(fd protoreflect.FieldDescriptor, s *Schema) (*Schema, bool) {
	switch {
	case fd.IsList():
		return &Schema{Const: val([]any{})}, true
	case fd.IsMap():
		return &Schema{Const: val(map[string]any{})}, true
	}
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return nil, false
	case protoreflect.StringKind, protoreflect.BytesKind:
		return &Schema{Const: val("")}, true
	case protoreflect.BoolKind:
		return &Schema{Const: val(false)}, true
	case protoreflect.EnumKind:
		zero := fd.Enum().Values().ByNumber(0)
		if zero == nil {
			return nil, false
		}
		return &Schema{Const: val(string(zero.Name()))}, true
	}
	if is64(s) {
		return &Schema{Enum: []any{0, "0"}}, true
	}
	return &Schema{Const: val(0)}, true
}

// is64 reports whether a schema is a 64-bit integer of a request, a number or a string.
func is64(s *Schema) bool {
	t, ok := s.Type.([]string)
	return ok && slices.Contains(t, "string") && slices.Contains(t, "integer")
}

func applyFieldRules(s *Schema, fd protoreflect.FieldDescriptor, r *validate.FieldRules) []string {
	notes := celNotes(r.GetCel(), r.GetCelExpression())
	switch {
	case fd.IsMap():
		mr := r.GetMap()
		if mr == nil {
			break
		}
		if mr.HasMinPairs() {
			s.MinProperties = u64(mr.GetMinPairs())
		}
		if mr.HasMaxPairs() {
			s.MaxProperties = u64(mr.GetMaxPairs())
		}
		if k := mr.GetKeys(); k != nil {
			// A key is a string in JSON: its string rules apply, numeric bounds do not.
			keys := &Schema{}
			notes = append(notes, prefixed("key: ", elementRules(keys, fd.MapKey(), k, true))...)
			keys.Minimum, keys.Maximum, keys.ExclusiveMinimum, keys.ExclusiveMaximum = nil, nil, nil, nil
			switch {
			case keys.empty():
			case s.PropertyNames == nil:
				s.PropertyNames = keys
			default:
				s.PropertyNames.AllOf = append(s.PropertyNames.AllOf, keys)
			}
		}
		if v := mr.GetValues(); v != nil {
			if vs, ok := s.AdditionalProperties.V.(*Schema); ok {
				notes = append(notes, prefixed("value: ", elementRules(vs, fd.MapValue(), v, true))...)
			}
		}
	case fd.IsList():
		rr := r.GetRepeated()
		if rr == nil {
			break
		}
		if rr.HasMinItems() {
			s.MinItems = u64(rr.GetMinItems())
		}
		if rr.HasMaxItems() {
			s.MaxItems = u64(rr.GetMaxItems())
		}
		s.UniqueItems = rr.GetUnique()
		if it := rr.GetItems(); it != nil && s.Items != nil {
			notes = append(notes, prefixed("item: ", elementRules(s.Items, fd, it, true))...)
		}
	default:
		notes = append(notes, elementRules(s, fd, r, false)...)
	}
	return notes
}

func prefixed(p string, notes []string) []string {
	for i := range notes {
		notes[i] = p + notes[i]
	}
	return notes
}

// elementRules applies the rules of one value: a scalar, an enum or a message.
func elementRules(s *Schema, fd protoreflect.FieldDescriptor, r *validate.FieldRules, nested bool) []string {
	var notes []string
	if nested {
		// The rules of an item, a key or a value; those of the field itself are
		// collected once by applyRules.
		notes = celNotes(r.GetCel(), r.GetCelExpression())
	}
	m := r.ProtoReflect()
	od := m.Descriptor().Oneofs().ByName("type")
	which := m.WhichOneof(od)
	if which == nil {
		return notes
	}
	sub := m.Get(which).Message()
	switch which.Name() {
	case "string":
		notes = append(notes, stringRules(s, r.GetString())...)
	case "bytes":
		notes = append(notes, bytesRules(r.GetBytes())...)
	case "bool":
		if b := r.GetBool(); b.HasConst() {
			s.Const = val(b.GetConst())
		}
	case "enum":
		notes = append(notes, enumRules(s, fd.Enum(), r.GetEnum())...)
	case "timestamp", "duration", "any", "field_mask":
		notes = append(notes, genericNotes(sub)...)
	default:
		notes = append(notes, numberRules(s, sub)...)
	}
	return notes
}

// numberRules maps the rules every numeric type shares, by name: const, lt, lte, gt,
// gte, in, not_in and example.
func numberRules(s *Schema, m protoreflect.Message) []string {
	get := func(name string) (any, bool) {
		f := m.Descriptor().Fields().ByName(protoreflect.Name(name))
		if f == nil || !m.Has(f) {
			return nil, false
		}
		return number(m.Get(f).Interface()), true
	}
	list := func(name string) []any {
		f := m.Descriptor().Fields().ByName(protoreflect.Name(name))
		if f == nil || !m.Has(f) {
			return nil
		}
		var out []any
		l := m.Get(f).List()
		for i := range l.Len() {
			out = append(out, number(l.Get(i).Interface()))
		}
		return out
	}
	wide := is64(s)
	var notes []string
	if v, ok := get("const"); ok {
		if wide {
			s.Enum = bothForms([]any{v})
		} else {
			s.Const = val(v)
		}
	}
	lt, hasLT := get("lt")
	lte, hasLTE := get("lte")
	gt, hasGT := get("gt")
	gte, hasGTE := get("gte")
	upper, lower := lt, gt
	if hasLTE {
		upper = lte
	}
	if hasGTE {
		lower = gte
	}
	if (hasLT || hasLTE) && (hasGT || hasGTE) && less(upper, lower) {
		// gt above lt is a range the value must be outside of: no keyword for that.
		notes = append(notes, fmt.Sprintf("outside of %v…%v", upper, lower))
	} else {
		switch {
		case hasLT:
			s.ExclusiveMaximum = val(lt)
		case hasLTE:
			s.Maximum = val(lte)
		}
		switch {
		case hasGT:
			s.ExclusiveMinimum = val(gt)
		case hasGTE:
			s.Minimum = val(gte)
		}
	}
	if in := list("in"); len(in) > 0 {
		if wide {
			in = bothForms(in)
		}
		s.Enum = in
	}
	if notIn := list("not_in"); len(notIn) > 0 {
		if wide {
			notIn = bothForms(notIn)
		}
		addNot(s, &Schema{Enum: notIn})
	}
	if wide && (s.Minimum != nil || s.Maximum != nil || s.ExclusiveMinimum != nil || s.ExclusiveMaximum != nil) {
		notes = append(notes, "the bounds hold for the string form too, which JSON Schema checks on numbers only")
	}
	if f := m.Descriptor().Fields().ByName("finite"); f != nil && m.Get(f).Bool() {
		notes = append(notes, "a finite number")
	}
	if ex := list("example"); len(ex) > 0 {
		s.Examples = ex
	}
	return notes
}

// number is a rule value as a JSON number. The bounds of a 64-bit integer stay numbers
// though JSON writes the value as a string: JSON Schema checks them on a number only.
func number(v any) any {
	switch n := v.(type) {
	case float32:
		f, _ := strconv.ParseFloat(strconv.FormatFloat(float64(n), 'g', -1, 32), 64)
		return f
	default:
		return n
	}
}

// bothForms lists 64-bit integers as numbers and as the strings JSON writes them as.
func bothForms(values []any) []any {
	out := slices.Clone(values)
	for _, v := range values {
		out = append(out, fmt.Sprint(v))
	}
	return out
}

// addNot adds a schema a value must not match.
func addNot(s *Schema, not *Schema) {
	if s.Not == nil {
		s.Not = not
		return
	}
	s.AllOf = append(s.AllOf, &Schema{Not: not})
}

// addPattern adds a pattern a value must match.
func addPattern(s *Schema, p string) {
	if s.Pattern == "" {
		s.Pattern = p
		return
	}
	s.AllOf = append(s.AllOf, &Schema{Pattern: p})
}

func less(a, b any) bool {
	fa, _ := strconv.ParseFloat(fmt.Sprint(a), 64)
	fb, _ := strconv.ParseFloat(fmt.Sprint(b), 64)
	return fa < fb
}

func stringRules(s *Schema, r *validate.StringRules) []string {
	var notes []string
	if r.HasConst() {
		s.Const = val(r.GetConst())
	}
	if r.HasLen() {
		s.MinLength, s.MaxLength = u64(r.GetLen()), u64(r.GetLen())
	}
	if r.HasMinLen() {
		s.MinLength = u64(r.GetMinLen())
	}
	if r.HasMaxLen() {
		s.MaxLength = u64(r.GetMaxLen())
	}
	if r.HasLenBytes() {
		notes = append(notes, fmt.Sprintf("exactly %d bytes in UTF-8", r.GetLenBytes()))
	}
	if r.HasMinBytes() {
		notes = append(notes, fmt.Sprintf("at least %d bytes in UTF-8", r.GetMinBytes()))
	}
	if r.HasMaxBytes() {
		notes = append(notes, fmt.Sprintf("at most %d bytes in UTF-8", r.GetMaxBytes()))
	}
	var patterns []string
	if r.HasPattern() {
		patterns = append(patterns, r.GetPattern())
	}
	if r.HasPrefix() {
		patterns = append(patterns, "^"+regexp.QuoteMeta(r.GetPrefix()))
	}
	if r.HasSuffix() {
		patterns = append(patterns, regexp.QuoteMeta(r.GetSuffix())+"$")
	}
	if r.HasContains() {
		patterns = append(patterns, regexp.QuoteMeta(r.GetContains()))
	}
	for _, p := range patterns {
		addPattern(s, p)
	}
	if r.HasNotContains() {
		addNot(s, &Schema{Pattern: regexp.QuoteMeta(r.GetNotContains())})
	}
	if in := r.GetIn(); len(in) > 0 {
		s.Enum = toAny(in)
	}
	if notIn := r.GetNotIn(); len(notIn) > 0 {
		addNot(s, &Schema{Enum: toAny(notIn)})
	}
	switch {
	case r.GetEmail():
		s.Format = "email"
	case r.GetHostname():
		s.Format = "hostname"
	case r.GetIpv4():
		s.Format = "ipv4"
	case r.GetIpv6():
		s.Format = "ipv6"
	case r.GetUri():
		s.Format = "uri"
	case r.GetUriRef():
		s.Format = "uri-reference"
	case r.GetUuid():
		s.Format = "uuid"
	case r.GetTuuid():
		addPattern(s, "^[0-9a-fA-F]{32}$")
	case r.GetUlid():
		addPattern(s, "^[0-7][0-9A-HJKMNP-TV-Za-hjkmnp-tv-z]{25}$")
	case r.GetIp():
		notes = append(notes, "an IPv4 or IPv6 address")
	case r.GetAddress():
		notes = append(notes, "a hostname or an IP address")
	case r.GetHostAndPort():
		notes = append(notes, "a host and a port, host:port")
	case r.GetIpWithPrefixlen(), r.GetIpv4WithPrefixlen(), r.GetIpv6WithPrefixlen():
		notes = append(notes, "an IP address with a prefix length")
	case r.GetIpPrefix(), r.GetIpv4Prefix(), r.GetIpv6Prefix():
		notes = append(notes, "an IP prefix")
	case r.GetProtobufFqn(), r.GetProtobufDotFqn():
		notes = append(notes, "a fully qualified protobuf name")
	case r.GetWellKnownRegex() == validate.KnownRegex_KNOWN_REGEX_HTTP_HEADER_NAME:
		notes = append(notes, "an HTTP header name")
	case r.GetWellKnownRegex() == validate.KnownRegex_KNOWN_REGEX_HTTP_HEADER_VALUE:
		notes = append(notes, "an HTTP header value")
	}
	if ex := r.GetExample(); len(ex) > 0 {
		s.Examples = toAny(ex)
	}
	return notes
}

func bytesRules(r *validate.BytesRules) []string {
	var notes []string
	if r.HasLen() {
		notes = append(notes, fmt.Sprintf("exactly %d bytes", r.GetLen()))
	}
	if r.HasMinLen() {
		notes = append(notes, fmt.Sprintf("at least %d bytes", r.GetMinLen()))
	}
	if r.HasMaxLen() {
		notes = append(notes, fmt.Sprintf("at most %d bytes", r.GetMaxLen()))
	}
	if r.HasPattern() {
		notes = append(notes, "matches `"+r.GetPattern()+"`")
	}
	switch {
	case r.GetIp():
		notes = append(notes, "an IP address in bytes")
	case r.GetIpv4():
		notes = append(notes, "an IPv4 address in bytes")
	case r.GetIpv6():
		notes = append(notes, "an IPv6 address in bytes")
	case r.GetUuid():
		notes = append(notes, "a UUID in bytes")
	}
	return notes
}

func enumRules(s *Schema, ed protoreflect.EnumDescriptor, r *validate.EnumRules) []string {
	names := func(nums []int32) []any {
		var out []any
		for _, n := range nums {
			if v := ed.Values().ByNumber(protoreflect.EnumNumber(n)); v != nil {
				out = append(out, string(v.Name()))
			}
		}
		return out
	}
	if r.HasConst() {
		if c := names([]int32{r.GetConst()}); len(c) == 1 {
			s.Const = val(c[0])
		}
	}
	allowed := r.GetIn()
	if len(allowed) == 0 && len(r.GetNotIn()) > 0 {
		for i := range ed.Values().Len() {
			n := int32(ed.Values().Get(i).Number())
			if !slices.Contains(r.GetNotIn(), n) {
				allowed = append(allowed, n)
			}
		}
	}
	if len(allowed) > 0 {
		s.Enum = names(allowed)
	}
	return nil
}

// genericNotes describe the rules of a well-known type by their names and values:
// lt_now, within: 1h and the like have no JSON Schema keyword.
func genericNotes(m protoreflect.Message) []string {
	var notes []string
	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if !m.Has(fd) || fd.Name() == "example" {
			continue
		}
		v := m.Get(fd)
		switch {
		case fd.Kind() == protoreflect.BoolKind:
			if v.Bool() {
				notes = append(notes, string(fd.Name()))
			}
		case fd.Kind() == protoreflect.MessageKind && !fd.IsList():
			notes = append(notes, fmt.Sprintf("%s %s", fd.Name(), wellKnownText(v.Message())))
		case fd.IsList():
			var items []string
			for j := range v.List().Len() {
				items = append(items, fmt.Sprint(v.List().Get(j).Interface()))
			}
			notes = append(notes, fmt.Sprintf("%s %s", fd.Name(), strings.Join(items, ", ")))
		default:
			notes = append(notes, fmt.Sprintf("%s %v", fd.Name(), v.Interface()))
		}
	}
	return notes
}

// wellKnownText writes a Timestamp or a Duration the way JSON does.
func wellKnownText(m protoreflect.Message) string {
	b, err := jsonOf(m.Interface())
	if err != nil {
		return string(m.Descriptor().Name())
	}
	return strings.Trim(string(b), `"`)
}

func toAny[T any](in []T) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

func (s *Schema) empty() bool {
	return s.Pattern == "" && s.Format == "" && s.MinLength == nil && s.MaxLength == nil && len(s.Enum) == 0 &&
		s.Const == nil && s.Not == nil && len(s.AllOf) == 0 && s.Minimum == nil && s.Maximum == nil &&
		s.ExclusiveMinimum == nil && s.ExclusiveMaximum == nil
}

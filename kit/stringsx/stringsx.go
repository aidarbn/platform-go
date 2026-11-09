// Package stringsx applies opt-in string sanitization rules declared via the
// (platform.strings.v1.string_rules) field option, ported from taply's strutil package.
//
// Apply walks a request message, finds string fields annotated with string_rules, and
// applies the configured transformations in a fixed order:
//
//  1. strip_control    — remove \x00-\x1F (except \t \n \r), zero-width, BOM
//  2. nfc              — Unicode NFC normalization
//  3. trim             — TrimSpace (leading and trailing)
//  4. collapse_spaces  — collapse runs of whitespace to a single ASCII space
//  5. lowercase OR uppercase
//  6. nullify_empty    — clear the field when the result is "" (proto3 optional only)
//
// It must run BEFORE idempotency and validation, so that:
//
//   - the idempotency fingerprint is computed on the canonical form
//     (so "Burger" and "Burger " produce the same hash),
//   - protovalidate rules like min_len see the trimmed value
//     (so whitespace-only input fails validation as expected).
//
// The api module does this when it is given the function: api.Normalize(app, stringsx.Apply).
package stringsx

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/aidarbn/platform-go/kit/stringsx/stringspb"
)

// Apply walks the given proto message and applies string_rules to annotated
// fields. It is the function api.Normalize takes.
func Apply(msg proto.Message) {
	if msg == nil {
		return
	}
	applyRules(msg.ProtoReflect())
}

func applyRules(msg protoreflect.Message) {
	if !msg.IsValid() {
		return
	}
	fields := msg.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		applyToField(msg, fd)
	}
}

func applyToField(msg protoreflect.Message, fd protoreflect.FieldDescriptor) {
	// Recurse into nested messages (including repeated and map values).
	// A map field is a message kind (its entries are); one with string values takes the
	// string path below. In taply this check is missing, so its string_rules on a
	// map<string, string> field never applied.
	stringMap := fd.IsMap() && fd.MapValue().Kind() == protoreflect.StringKind
	if (fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind) && !stringMap {
		switch {
		case fd.IsMap():
			mapVal := msg.Get(fd).Map()
			// Only map values of message kind need recursion.
			if fd.MapValue().Kind() == protoreflect.MessageKind {
				mapVal.Range(func(_ protoreflect.MapKey, v protoreflect.Value) bool {
					applyRules(v.Message())
					return true
				})
			}
		case fd.IsList():
			list := msg.Get(fd).List()
			for j := range list.Len() {
				applyRules(list.Get(j).Message())
			}
		default:
			if msg.Has(fd) {
				applyRules(msg.Get(fd).Message())
			}
		}
		return
	}

	if fd.Kind() != protoreflect.StringKind && !stringMap {
		return
	}

	rules := stringRulesOf(fd)
	if rules == nil {
		return
	}

	switch {
	case fd.IsList():
		list := msg.Mutable(fd).List()
		for j := range list.Len() {
			v := list.Get(j).String()
			list.Set(j, protoreflect.ValueOfString(apply(v, rules)))
		}
	case fd.IsMap():
		// String-valued maps: apply rules to values, keys are left alone.
		if fd.MapValue().Kind() != protoreflect.StringKind {
			return
		}
		mapVal := msg.Mutable(fd).Map()
		mapVal.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			mapVal.Set(k, protoreflect.ValueOfString(apply(v.String(), rules)))
			return true
		})
	default:
		// Skip unset optional fields with no value to trim.
		if fd.HasOptionalKeyword() && !msg.Has(fd) {
			return
		}
		v := msg.Get(fd).String()
		out := apply(v, rules)
		if out == "" && rules.GetNullifyEmpty() && fd.HasOptionalKeyword() {
			msg.Clear(fd)
			return
		}
		msg.Set(fd, protoreflect.ValueOfString(out))
	}
}

func stringRulesOf(fd protoreflect.FieldDescriptor) *stringspb.StringRules {
	opts := fd.Options()
	if opts == nil {
		return nil
	}
	ext := proto.GetExtension(opts, stringspb.E_StringRules)
	rules, ok := ext.(*stringspb.StringRules)
	if !ok || rules == nil {
		return nil
	}
	// An unset extension returns a zero-value StringRules — treat that as "no rules".
	if !rules.GetTrim() &&
		!rules.GetCollapseSpaces() &&
		!rules.GetLowercase() &&
		!rules.GetUppercase() &&
		!rules.GetNfc() &&
		!rules.GetStripControl() &&
		!rules.GetNullifyEmpty() {
		return nil
	}
	return rules
}

func apply(s string, r *stringspb.StringRules) string {
	if r.GetStripControl() {
		s = stripControl(s)
	}
	if r.GetNfc() {
		s = norm.NFC.String(s)
	}
	if r.GetTrim() {
		s = strings.TrimSpace(s)
	}
	if r.GetCollapseSpaces() {
		s = collapseSpaces(s)
	}
	switch {
	case r.GetLowercase():
		s = strings.ToLower(s)
	case r.GetUppercase():
		s = strings.ToUpper(s)
	}
	return s
}

// stripControl removes ASCII control characters (except \t \n \r), zero-width
// spaces, and the byte order mark.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\n', '\r':
			return r
		case '\u200B', '\u200C', '\u200D', '\uFEFF':
			return -1
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// collapseSpaces replaces runs of Unicode whitespace with a single ASCII space.
// Leading and trailing whitespace is left alone — combine with `trim` to strip it.
func collapseSpaces(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteRune(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return b.String()
}

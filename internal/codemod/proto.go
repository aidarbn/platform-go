package codemod

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProtoFile is a .proto file a codemod rewrites. There is no syntax tree: a codemod
// finds imports and options through the helpers, which skip comments and strings, and
// replaces byte ranges, so everything else stays as the developer wrote it.
type ProtoFile struct {
	Path string

	src     []byte
	toks    []protoToken
	edits   []edit
	notes   []Note
	codemod string
}

// ProtoImport is an import statement: import "path";
type ProtoImport struct {
	Path       string
	Start, End int // the statement, from import to the semicolon
}

// ProtoOption is a custom option: a statement option (name).field = value; or a field
// option [(name) = value].
type ProtoOption struct {
	Name       string // inside the parentheses, without a leading dot: gnostic.openapi.v3.operation
	Field      bool   // a field or enum value option inside brackets
	Start, End int    // a statement from option to the semicolon; a field option from ( to the value
	ValueStart int    // the value: a scalar or a {} aggregate
	ValueEnd   int
}

// Source returns the whole file.
func (f *ProtoFile) Source() string { return string(f.src) }

// Text returns the source between two offsets.
func (f *ProtoFile) Text(start, end int) string { return string(f.src[start:end]) }

// Replace replaces the source between two offsets; an empty text deletes it.
func (f *ProtoFile) Replace(start, end int, text string) {
	f.edits = append(f.edits, edit{start: start, end: end, text: text})
}

// Report leaves a note for a person at an offset.
func (f *ProtoFile) Report(offset int, msg string) {
	line := 1 + bytes.Count(f.src[:min(offset, len(f.src))], []byte("\n"))
	f.notes = append(f.notes, Note{Path: f.Path, Line: line, Codemod: f.codemod, Message: msg})
}

// Imports lists the import statements.
func (f *ProtoFile) Imports() []ProtoImport {
	var out []ProtoImport
	for i, t := range f.toks {
		if t.text != "import" || !f.statementStart(i) {
			continue
		}
		j := i + 1
		if j < len(f.toks) && (f.toks[j].text == "public" || f.toks[j].text == "weak") {
			j++
		}
		if j+1 >= len(f.toks) || f.toks[j].kind != tokString || f.toks[j+1].text != ";" {
			continue
		}
		out = append(out, ProtoImport{Path: unquoteProto(f.toks[j].text), Start: t.start, End: f.toks[j+1].end})
	}
	return out
}

// Options lists the custom options whose name, inside the parentheses, is name or
// starts with name followed by a dot.
func (f *ProtoFile) Options(name string) []ProtoOption {
	var out []ProtoOption
	for i := 0; i < len(f.toks); i++ {
		t := f.toks[i]
		if t.text != "(" || i == 0 {
			continue
		}
		prev := f.toks[i-1].text
		statement := prev == "option" && f.statementStart(i-1)
		if !statement && prev != "[" && prev != "," {
			continue
		}
		opt, next, ok := f.option(i, statement)
		if !ok {
			continue
		}
		if opt.Name == name || strings.HasPrefix(opt.Name, name+".") {
			out = append(out, opt)
		}
		i = next - 1
	}
	return out
}

// option reads ( name ) .field* = value from the opening parenthesis at i.
func (f *ProtoFile) option(i int, statement bool) (ProtoOption, int, bool) {
	var name strings.Builder
	j := i + 1
	for j < len(f.toks) && f.toks[j].text != ")" {
		if f.toks[j].kind != tokIdent && f.toks[j].text != "." {
			return ProtoOption{}, 0, false
		}
		name.WriteString(f.toks[j].text)
		j++
	}
	j++ // )
	for j+1 < len(f.toks) && f.toks[j].text == "." && f.toks[j+1].kind == tokIdent {
		j += 2
	}
	if j >= len(f.toks) || f.toks[j].text != "=" || j+1 >= len(f.toks) {
		return ProtoOption{}, 0, false
	}
	j++
	valueStart := f.toks[j].start
	if f.toks[j].text == "{" {
		depth := 0
		for ; j < len(f.toks); j++ {
			switch f.toks[j].text {
			case "{":
				depth++
			case "}":
				depth--
			}
			if depth == 0 {
				break
			}
		}
		if j >= len(f.toks) {
			return ProtoOption{}, 0, false
		}
	} else {
		// A scalar: an identifier, a number or a string, maybe signed or concatenated.
		for j+1 < len(f.toks) && f.toks[j+1].text != ";" && f.toks[j+1].text != "," && f.toks[j+1].text != "]" {
			j++
		}
	}
	opt := ProtoOption{Name: strings.TrimPrefix(name.String(), "."), Field: !statement, ValueStart: valueStart, ValueEnd: f.toks[j].end, Start: f.toks[i].start}
	opt.End = opt.ValueEnd
	if statement {
		opt.Start = f.toks[i-1].start
		if j+1 < len(f.toks) && f.toks[j+1].text == ";" {
			j++
			opt.End = f.toks[j].end
		}
	}
	return opt, j + 1, true
}

// statementStart reports whether the token at i begins a statement.
func (f *ProtoFile) statementStart(i int) bool {
	if i == 0 {
		return true
	}
	switch f.toks[i-1].text {
	case ";", "{", "}":
		return true
	}
	return false
}

func runProto(dir, rel string, pending []Codemod) (*Change, []Note, error) {
	var mods []Codemod
	for _, m := range pending {
		if m.Proto != nil {
			mods = append(mods, m)
		}
	}
	if len(mods) == 0 {
		return nil, nil, nil
	}
	src, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		return nil, nil, err
	}
	var applied []string
	var notes []Note
	for _, m := range mods {
		f := &ProtoFile{Path: rel, src: src, toks: lexProto(src), codemod: m.Name}
		m.Proto(f)
		notes = append(notes, f.notes...)
		if len(f.edits) == 0 {
			continue
		}
		out, err := applyEdits(src, f.edits)
		if err != nil {
			return nil, nil, fmt.Errorf("codemod %s: %s: %w", m.Name, rel, err)
		}
		if bytes.Equal(out, src) {
			continue
		}
		applied = append(applied, m.Name)
		src = out
	}
	if len(applied) == 0 {
		return nil, notes, nil
	}
	return &Change{Path: rel, Codemods: applied, Content: src}, notes, nil
}

type tokKind int

const (
	tokPunct tokKind = iota
	tokIdent
	tokString
	tokNumber
)

type protoToken struct {
	kind       tokKind
	text       string
	start, end int
}

// lexProto splits a proto file into tokens, dropping comments and white space.
func lexProto(src []byte) []protoToken {
	var out []protoToken
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := bytes.Index(src[i+2:], []byte("*/"))
			if end < 0 {
				i = len(src)
			} else {
				i += end + 4
			}
		case c == '"' || c == '\'':
			start := i
			for i++; i < len(src) && src[i] != c && src[i] != '\n'; i++ {
				if src[i] == '\\' {
					i++
				}
			}
			i = min(i+1, len(src))
			out = append(out, protoToken{tokString, string(src[start:i]), start, i})
		case isIdentByte(c) || c >= 0x80:
			start := i
			for i < len(src) && (isIdentByte(src[i]) || src[i] >= '0' && src[i] <= '9' || src[i] >= 0x80) {
				i++
			}
			out = append(out, protoToken{tokIdent, string(src[start:i]), start, i})
		case c >= '0' && c <= '9':
			start := i
			for i < len(src) && (isIdentByte(src[i]) || src[i] >= '0' && src[i] <= '9' || src[i] == '.') {
				i++
			}
			out = append(out, protoToken{tokNumber, string(src[start:i]), start, i})
		default:
			out = append(out, protoToken{tokPunct, string(c), i, i + 1})
			i++
		}
	}
	return out
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func unquoteProto(s string) string {
	if len(s) >= 2 {
		return s[1 : len(s)-1]
	}
	return s
}

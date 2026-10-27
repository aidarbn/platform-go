package openapi

import (
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// comment is the documentation of a descriptor: its leading comment, or the trailing
// one when there is no leading comment (field = 1; // the name). Lines lose the
// indentation they share, so lists and code blocks keep theirs, and lines that are
// instructions to tools (buf:lint:ignore, @exclude) are left out.
func comment(d protoreflect.Descriptor) string {
	loc := d.ParentFile().SourceLocations().ByDescriptor(d)
	text := loc.LeadingComments
	if strings.TrimSpace(text) == "" {
		text = loc.TrailingComments
	}
	return cleanComment(text)
}

func cleanComment(text string) string {
	var lines []string
	for line := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "buf:lint:") || strings.HasPrefix(trimmed, "@exclude") {
			continue
		}
		lines = append(lines, strings.TrimRight(line, " \t"))
	}
	indent := -1
	for _, line := range lines {
		if line == "" {
			continue
		}
		n := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	for i, line := range lines {
		if len(line) >= indent && indent > 0 {
			lines[i] = line[indent:]
		}
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// maxSummary is the longest first paragraph that still reads as a summary.
const maxSummary = 120

// summarize splits the comment of a method: a short first paragraph is the summary and
// the rest the description; a long one leaves the summary to the method name.
func summarize(name, text string) (summary, description string) {
	if text == "" {
		return name, ""
	}
	first, rest, _ := strings.Cut(text, "\n\n")
	one := strings.Join(strings.Fields(first), " ")
	if strings.Contains(first, "\n") && strings.ContainsAny(first, "-*`|#") || utf8.RuneCountInString(one) > maxSummary {
		return name, text // a list, a table or a long paragraph: keep it as written
	}
	return one, strings.Trim(rest, "\n")
}

// joinText joins paragraphs, skipping empty ones.
func joinText(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

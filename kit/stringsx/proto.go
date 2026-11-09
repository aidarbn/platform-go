package stringsx

import _ "embed"

// Proto is the source of the string_rules option: a project that annotates its fields
// needs it next to its own proto files as platform/strings/v1/strings.proto, so the
// descriptors of both are the same file.
//
//go:embed proto/platform/strings/v1/strings.proto
var Proto string

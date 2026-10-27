package openapipb

import _ "embed"

// Proto is the source of the options: the platform writes it into a project as
// <proto_dir>/platform/openapi/v1/options.proto when the project uses the platform's
// OpenAPI generator, so the descriptors of both are the same file.
//
//go:embed proto/platform/openapi/v1/options.proto
var Proto string

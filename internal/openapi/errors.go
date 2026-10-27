package openapi

// The error body of the gateway: google.rpc.Status as JSON, with the protovalidate
// violations of a request that failed validation and the ErrorInfo of a failed external
// provider among its details.

const (
	errorSchema   = "Error"
	errorResponse = "Error"
)

// errorDescription lists the HTTP statuses the gateway answers for the gRPC codes.
const errorDescription = `An error. The HTTP status follows the gRPC code of the error:

- 400 — INVALID_ARGUMENT (the details hold the violations of the request), FAILED_PRECONDITION, OUT_OF_RANGE
- 401 — UNAUTHENTICATED
- 403 — PERMISSION_DENIED
- 404 — NOT_FOUND
- 409 — ALREADY_EXISTS, ABORTED
- 413 — the body is larger than the service accepts
- 429 — RESOURCE_EXHAUSTED: the rate limit
- 499 — CANCELLED
- 500 — INTERNAL, UNKNOWN, DATA_LOSS
- 501 — UNIMPLEMENTED
- 502 — UNAVAILABLE because an external provider failed (an ErrorInfo with the reason EXTERNAL_UNAVAILABLE)
- 503 — UNAVAILABLE
- 504 — DEADLINE_EXCEEDED`

func (g *generator) addErrors() {
	str := func(desc string) *Schema { return &Schema{Type: "string", Description: desc} }
	g.schemas[errorSchema] = &Schema{
		Type:        "object",
		Description: "The error body: google.rpc.Status.",
		Properties: orderedOf(
			"code", &Schema{Type: "integer", Format: "int32", Description: "The gRPC code: 3 is INVALID_ARGUMENT, 5 NOT_FOUND, 7 PERMISSION_DENIED, 16 UNAUTHENTICATED."},
			"message", str("What went wrong, in the language of the request when the i18n module translates it."),
			"details", &Schema{
				Type:        "array",
				Description: "Details by their @type: buf.validate.Violations for a request that failed validation, google.rpc.ErrorInfo with a reason.",
				Items: &Schema{AnyOf: []*Schema{
					{Ref: schemaRef + "buf.validate.Violations"},
					{Ref: schemaRef + "google.rpc.ErrorInfo"},
					{Type: "object", Properties: orderedOf("@type", &Schema{Type: "string"}), Required: []string{"@type"}},
				}},
			},
		),
		Required: []string{"code", "message", "details"},
	}
	pathElement := &Schema{
		Type: "object",
		Properties: orderedOf(
			"field_number", &Schema{Type: "integer", Format: "int32"},
			"field_name", &Schema{Type: "string"},
			"field_type", str("The type of the field, TYPE_STRING and the like."),
			"key_type", &Schema{Type: "string"},
			"value_type", &Schema{Type: "string"},
			"index", &Schema{Type: "string", Format: "uint64", Description: "The position in a list."},
			"bool_key", &Schema{Type: "boolean"},
			"int_key", &Schema{Type: "string", Format: "int64"},
			"uint_key", &Schema{Type: "string", Format: "uint64"},
			"string_key", &Schema{Type: "string"},
		),
	}
	fieldPath := &Schema{
		Type:        "object",
		Description: "A path to a field, from the request message down.",
		Properties:  orderedOf("elements", &Schema{Type: "array", Items: pathElement}),
	}
	g.schemas["buf.validate.FieldPath"] = fieldPath
	nullablePath := &Schema{OneOf: []*Schema{{Ref: schemaRef + "buf.validate.FieldPath"}, {Type: "null"}}}
	g.schemas["buf.validate.Violations"] = &Schema{
		Type:        "object",
		Description: "The rules a request breaks.",
		Properties: orderedOf(
			"@type", &Schema{Const: val("type.googleapis.com/buf.validate.Violations")},
			"violations", &Schema{Type: "array", Items: &Schema{
				Type: "object",
				Properties: orderedOf(
					"field", nullablePath,
					"rule", nullablePath,
					"rule_id", str("The rule, string.min_len and the like."),
					"message", str("What is wrong with the field."),
					"for_key", &Schema{Type: "boolean"},
				),
			}},
		),
		Required: []string{"@type"},
	}
	g.schemas["google.rpc.ErrorInfo"] = &Schema{
		Type:        "object",
		Description: "Why the call failed, as a constant a client can check.",
		Properties: orderedOf(
			"@type", &Schema{Const: val("type.googleapis.com/google.rpc.ErrorInfo")},
			"reason", str("EXTERNAL_UNAVAILABLE, EXTERNAL_TIMEOUT or a reason of the service."),
			"domain", &Schema{Type: "string"},
			"metadata", &Schema{Type: "object", AdditionalProperties: val(&Schema{Type: "string"})},
		),
		Required: []string{"@type"},
	}
	g.responses[errorResponse] = &Response{
		Description: errorDescription,
		Content:     orderedMedia("application/json", &MediaType{Schema: &Schema{Ref: schemaRef + errorSchema}}),
	}
}

func orderedMedia(k string, v *MediaType) *Ordered[*MediaType] {
	o := NewOrdered[*MediaType]()
	o.Set(k, v)
	return o
}

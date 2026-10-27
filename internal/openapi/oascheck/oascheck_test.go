package oascheck_test

import (
	"os"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/openapi/oascheck"
)

// TestValidateFile checks a document given by OPENAPI_FILE: a way to validate the
// description of a real project by hand.
func TestValidateFile(t *testing.T) {
	path := os.Getenv("OPENAPI_FILE")
	if path == "" {
		t.Skip("OPENAPI_FILE is not set")
	}
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := oascheck.Validate(doc); err != nil {
		t.Fatal(err)
	}
}

const good = `openapi: 3.1.0
info: {title: t, version: "1"}
paths: {}
components:
  schemas:
    Thing:
      type: object
      properties:
        id: {type: string, format: uuid}
        at: {type: string, format: date-time}
      required: [id]
      additionalProperties: false
    Ref:
      $ref: '#/components/schemas/Thing'
`

func TestValidate(t *testing.T) {
	if err := oascheck.Validate([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]string{
		"not yaml":      "a: [",
		"no info":       "openapi: 3.1.0\npaths: {}\n",
		"old version":   strings.Replace(good, "3.1.0", "3.0.3", 1),
		"bad schema":    strings.Replace(good, "type: object", "type: thing", 1),
		"missing ref":   strings.Replace(good, "schemas/Thing'", "schemas/Gone'", 1),
		"bad path item": strings.Replace(good, "paths: {}", "paths: {/x: {get: {responses: 7}}}", 1),
	} {
		if err := oascheck.Validate([]byte(doc)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestChecker(t *testing.T) {
	c, err := oascheck.NewChecker([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	ok := `{"id":"5f0c6c3e-8a1e-4c4f-9a59-2f9a4c3c2b11","at":"2026-10-27T12:00:00Z"}`
	if err := c.Check("Thing", []byte(ok)); err != nil {
		t.Error(err)
	}
	if err := c.Check("Ref", []byte(ok)); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{`{}`, `{"id":"x"}`, `{"id":"5f0c6c3e-8a1e-4c4f-9a59-2f9a4c3c2b11","more":1}`, `not json`} {
		if err := c.Check("Thing", []byte(bad)); err == nil {
			t.Errorf("%s passes", bad)
		}
	}
	if err := c.Check("Gone", []byte(ok)); err == nil {
		t.Error("a missing schema passes")
	}
	if _, err := oascheck.NewChecker([]byte("a: [")); err == nil {
		t.Error("garbage is a document")
	}
}

package spec_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/spec"
)

// The published schema must be exactly what the registry gives: a module added to the
// registry without regenerating the file fails here. Regenerate with:
//
//	go run ./cmd/platformgo schema > schema/platformgo.schema.json
func TestPublishedSchemaIsCurrent(t *testing.T) {
	want, err := spec.JSONSchema()
	if err != nil {
		t.Fatalf("JSONSchema: %v", err)
	}
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "..", "..", "schema", "platformgo.schema.json")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the published schema: %v", err)
	}
	if string(got) != string(want) {
		t.Fatal("schema/platformgo.schema.json is stale: go run ./cmd/platformgo schema > schema/platformgo.schema.json")
	}
}

func TestSchemaDescribesModulesAndOptions(t *testing.T) {
	raw, err := spec.JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Properties struct {
			Modules struct {
				Properties map[string]struct {
					Properties           map[string]any `json:"properties"`
					AdditionalProperties bool           `json:"additionalProperties"`
					Description          string         `json:"description"`
				} `json:"properties"`
				AdditionalProperties bool `json:"additionalProperties"`
			} `json:"modules"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	mods := doc.Properties.Modules
	for _, name := range []string{"admin", "api", "postgres", "river", "s3", "settings"} {
		if _, ok := mods.Properties[name]; !ok {
			t.Errorf("module %s is missing from the schema", name)
		}
	}
	if mods.AdditionalProperties {
		t.Error("an unknown module must be rejected by the schema")
	}
	if _, ok := mods.Properties["settings"].Properties["schema"]; !ok {
		t.Error("the schema option of settings is missing")
	}
	if !strings.Contains(mods.Properties["admin"].Description, "requires postgres") {
		t.Errorf("admin = %+v", mods.Properties["admin"])
	}
}

func TestUnknownModuleOption(t *testing.T) {
	_, err := spec.Parse([]byte("schema: 1\nproject:\n  module: x.com/a\nmodules:\n  postgres:\n    migrations: db\n  settings:\n    shema: s.yaml\n"))
	if err == nil {
		t.Fatal("unknown options were accepted")
	}
	for _, want := range []string{
		`module postgres: unknown option "migrations", known options: none`,
		`module settings: unknown option "shema", known options: schema`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err lacks %q: %v", want, err)
		}
	}

	if _, err := spec.Parse([]byte("schema: 1\nproject:\n  module: x.com/a\nmodules:\n  postgres: {}\n  settings:\n    schema: 5\n")); err == nil ||
		!strings.Contains(err.Error(), "must be a string") {
		t.Errorf("a number as the schema path: %v", err)
	}
}

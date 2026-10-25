package spec_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/registry"
	"github.com/aidarbn/platform-go/internal/spec"
)

// Files written by earlier platform versions keep parsing the same way: typed options
// add keys, they do not change what schema 1 means.
func TestOldFilesStillParse(t *testing.T) {
	want := map[string]string{
		"modelwatch.yaml":          "api,postgres,river,settings,web",
		"sendkz.yaml":              "postgres,admin,api,river,settings,web",
		"scaffold-no-modules.yaml": "",
		"every-module.yaml":        "postgres,admin,api,enums,i18n,monitoring,rbac,river,s3,settings,web",
	}
	for name, modules := range want {
		t.Run(name, func(t *testing.T) {
			f, err := spec.Load(filepath.Join("testdata", "old", name))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, m := range f.EnabledModules() {
				got = append(got, m.Name)
			}
			if strings.Join(got, ",") != modules {
				t.Errorf("modules = %v, want %s", got, modules)
			}
		})
	}
}

func TestStringOption(t *testing.T) {
	f, err := spec.Load(filepath.Join("testdata", "old", "every-module.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.StringOption("settings", "schema", "settings.yaml"); got != "config/settings.yaml" {
		t.Errorf("settings.schema = %q", got)
	}
	if got := f.StringOption("api", "nope", "def"); got != "def" {
		t.Errorf("an unset option = %q", got)
	}
	if got := f.StringOption("absent", "x", "def"); got != "def" {
		t.Errorf("a disabled module = %q", got)
	}
}

func TestDecodeOptions(t *testing.T) {
	f, err := spec.Parse([]byte("schema: 1\nproject:\n  module: x.com/a\nmodules:\n  postgres: {}\n  settings:\n    schema: s.yaml\n  enums:\n    domain: internal/d\n  api: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Schema string `yaml:"schema"`
	}
	if err := f.DecodeOptions("settings", &s); err != nil || s.Schema != "s.yaml" {
		t.Errorf("settings = %+v, %v", s, err)
	}

	// No section, or an empty one, leaves the defaults the caller filled in.
	s.Schema = "default.yaml"
	if err := f.DecodeOptions("postgres", &s); err != nil || s.Schema != "default.yaml" {
		t.Errorf("an empty section = %+v, %v", s, err)
	}
	if err := f.DecodeOptions("river", &s); err != nil || s.Schema != "default.yaml" {
		t.Errorf("a disabled module = %+v, %v", s, err)
	}

	// A struct that lags behind the registry fails rather than dropping an option.
	var lagging struct {
		Other string `yaml:"other"`
	}
	if err := f.DecodeOptions("enums", &lagging); err == nil || !strings.Contains(err.Error(), "module enums options") {
		t.Errorf("a struct without the field: %v", err)
	}
}

func TestOptionErrorsNameTheLine(t *testing.T) {
	src := "schema: 1\nproject:\n  module: x.com/a\nmodules:\n  postgres: {}\n  settings:\n    schema: 1.5\n  enums:\n    domain: [a]\n  api: {}\n"
	_, err := spec.Parse([]byte(src))
	if err == nil {
		t.Fatal("wrong types were accepted")
	}
	for _, want := range []string{
		`line 7: module settings: option schema must be a string, got a number 1.5: quote it, "1.5"`,
		"line 9: module enums: option domain must be a string, got a list",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err lacks %q:\n%v", want, err)
		}
	}
	if _, err := spec.Parse([]byte("schema: 1\nproject:\n  module: x.com/a\nmodules: [postgres]\n")); err == nil {
		t.Error("modules as a list was accepted")
	}
	if _, err := spec.Parse([]byte("schema: 1\nproject: {module: x.com/a}\nmodules:\n  settings: {schema: a}\n  postgres: {}\n  x: &a\n")); err == nil {
		t.Error("an unknown module was accepted")
	}
}

// The registry is what the published schema and the validator are built from: every
// module described there passes the well-formedness check.
func TestRegistryOptionsWellFormed(t *testing.T) {
	for _, m := range registry.All() {
		if err := registry.CheckOptions(m); err != nil {
			t.Error(err)
		}
	}
}

func TestParseRejectsBrokenYAML(t *testing.T) {
	if _, err := spec.Parse([]byte("schema: 1\nproject:\n  module: [\n")); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Errorf("err = %v", err)
	}
}

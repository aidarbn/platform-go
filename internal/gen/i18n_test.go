package gen_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/gen"
)

// The option file a project gets must be exactly the one kit/i18nx is generated from:
// otherwise the descriptors differ and the project's code registers a conflicting file.
func TestI18nProtoMatchesKit(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "kit", "i18nx", "proto", "platform", "i18n", "v1", "i18n.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != gen.I18nProto {
		t.Fatal("internal/gen.I18nProto differs from kit/i18nx/proto/platform/i18n/v1/i18n.proto")
	}
}

func TestI18nFiles(t *testing.T) {
	withI18n := "schema: 1\nproject:\n  module: github.com/aidarbn/shop-api\nmodules:\n  postgres: {}\n  api: {}\n  i18n: {}\n"
	files, err := gen.Wiring(mustParse(t, withI18n))
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}
	for _, path := range []string{gen.MessagesGoPath, gen.I18nProtoPath} {
		if _, ok := files[path]; !ok {
			t.Errorf("%s is missing", path)
		}
	}
	bufGen := string(files[gen.BufGenPath])
	for _, want := range []string{"path: platform", "exclude_paths:", "- proto/platform"} {
		if !strings.Contains(bufGen, want) {
			t.Errorf("buf.gen.yaml lacks %q:\n%s", want, bufGen)
		}
	}
	if !strings.Contains(string(files[gen.ModulesPath]), "i18n.New(cfg.I18n, i18n.WithMessages(i18nmessages.Messages))") {
		t.Errorf("modules.gen.go:\n%s", files[gen.ModulesPath])
	}

	// Without i18n the API generates everything under proto.
	plain, err := gen.Wiring(mustParse(t, withAPI))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain[gen.BufGenPath]), "exclude_paths") {
		t.Error("the platform proto directory is excluded without the i18n module")
	}
}

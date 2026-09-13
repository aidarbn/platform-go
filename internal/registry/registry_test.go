package registry_test

import (
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/registry"
)

func TestPostgresDescribed(t *testing.T) {
	m, ok := registry.Get("postgres")
	if !ok {
		t.Fatal("postgres must be in the registry")
	}
	if m.ConfigType() != "postgres.Config" || m.LoadCall() != "postgres.Load(l)" || m.NewCall() != "postgres.New(cfg.Postgres, postgres.WithMigrations(migrations.FS))" {
		t.Errorf("calls: %s / %s / %s", m.ConfigType(), m.LoadCall(), m.NewCall())
	}
	if len(m.Env) == 0 || m.Env[0].Key != "DATABASE_URL" || !m.Env[0].Required {
		t.Errorf("environment variables: %+v", m.Env)
	}
}

func TestUnknownModule(t *testing.T) {
	if _, ok := registry.Get("no-such-module"); ok {
		t.Error("an unknown module must not be found")
	}
}

func TestEveryModuleDescribedFully(t *testing.T) {
	for _, m := range registry.All() {
		if m.Name == "" || m.Import == "" || m.Package == "" || m.Field == "" {
			t.Errorf("module is described only partially: %+v", m)
		}
		for _, dep := range m.Requires {
			if _, ok := registry.Get(dep); !ok {
				t.Errorf("module %s requires unknown module %s", m.Name, dep)
			}
		}
		if !strings.HasPrefix(m.Import, "github.com/aidarbn/platform-go/") {
			t.Errorf("module %s: unexpected import path %s", m.Name, m.Import)
		}
	}
}

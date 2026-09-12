package registry_test

import (
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/registry"
)

func TestPostgresDescribed(t *testing.T) {
	m, ok := registry.Get("postgres")
	if !ok {
		t.Fatal("модуль postgres должен быть в реестре")
	}
	if m.ConfigType() != "postgres.Config" || m.LoadCall() != "postgres.Load(l)" || m.NewCall() != "postgres.New(cfg.Postgres)" {
		t.Errorf("вызовы: %s / %s / %s", m.ConfigType(), m.LoadCall(), m.NewCall())
	}
	if len(m.Env) == 0 || m.Env[0].Key != "DATABASE_URL" || !m.Env[0].Required {
		t.Errorf("переменные окружения: %+v", m.Env)
	}
}

func TestUnknownModule(t *testing.T) {
	if _, ok := registry.Get("нет такого"); ok {
		t.Error("неизвестный модуль не должен находиться")
	}
}

func TestEveryModuleDescribedFully(t *testing.T) {
	for _, m := range registry.All() {
		if m.Name == "" || m.Import == "" || m.Package == "" || m.Field == "" {
			t.Errorf("модуль описан не полностью: %+v", m)
		}
		for _, dep := range m.Requires {
			if _, ok := registry.Get(dep); !ok {
				t.Errorf("модуль %s требует неизвестный %s", m.Name, dep)
			}
		}
		if !strings.HasPrefix(m.Import, "github.com/aidarbn/platform-go/") {
			t.Errorf("модуль %s: неожиданный путь пакета %s", m.Name, m.Import)
		}
	}
}

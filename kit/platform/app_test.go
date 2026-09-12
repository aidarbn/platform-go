package platform_test

import (
	"testing"

	"github.com/aidarbn/platform-go/kit/platform"
)

type pool struct{ dsn string }

type queue interface{ Kind() string }

type riverQueue struct{}

func (riverQueue) Kind() string { return "river" }

func TestProvideAndGet(t *testing.T) {
	app := platform.NewApp(nil)

	platform.Provide(app, &pool{dsn: "postgres://"})
	got := platform.Get[*pool](app)

	if got.dsn != "postgres://" {
		t.Errorf("dsn = %q", got.dsn)
	}
}

func TestProvideInterface(t *testing.T) {
	app := platform.NewApp(nil)

	platform.Provide[queue](app, riverQueue{})

	got, ok := platform.Lookup[queue](app)
	if !ok || got.Kind() != "river" {
		t.Errorf("Lookup = %v, %v", got, ok)
	}
}

func TestLookupMissing(t *testing.T) {
	app := platform.NewApp(nil)

	if _, ok := platform.Lookup[*pool](app); ok {
		t.Error("в пустом контейнере ничего не должно находиться")
	}
}

func TestGetMissingPanics(t *testing.T) {
	app := platform.NewApp(nil)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("ждали паники: отсутствие зависимости — ошибка сборки приложения")
		}
	}()
	_ = platform.Get[*pool](app)
}

func TestProvideReplaces(t *testing.T) {
	app := platform.NewApp(nil)

	platform.Provide(app, &pool{dsn: "первый"})
	platform.Provide(app, &pool{dsn: "второй"})

	if got := platform.Get[*pool](app).dsn; got != "второй" {
		t.Errorf("dsn = %q, ждали второй", got)
	}
}

func TestMetricsRegistry(t *testing.T) {
	app := platform.NewApp(nil)

	if app.Metrics() == nil {
		t.Fatal("реестр метрик не создан")
	}
	families, err := app.Metrics().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(families) == 0 {
		t.Error("ждали метрики Go и процесса из коробки")
	}
}

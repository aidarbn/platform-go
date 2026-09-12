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
		t.Error("an empty container must hold nothing")
	}
}

func TestGetMissingPanics(t *testing.T) {
	app := platform.NewApp(nil)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("want a panic: a missing dependency is a wiring mistake")
		}
	}()
	_ = platform.Get[*pool](app)
}

func TestProvideReplaces(t *testing.T) {
	app := platform.NewApp(nil)

	platform.Provide(app, &pool{dsn: "first"})
	platform.Provide(app, &pool{dsn: "second"})

	if got := platform.Get[*pool](app).dsn; got != "second" {
		t.Errorf("dsn = %q, want second", got)
	}
}

func TestMetricsRegistry(t *testing.T) {
	app := platform.NewApp(nil)

	if app.Metrics() == nil {
		t.Fatal("metrics registry is missing")
	}
	families, err := app.Metrics().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(families) == 0 {
		t.Error("want Go and process metrics out of the box")
	}
}

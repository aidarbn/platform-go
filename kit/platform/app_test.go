package platform_test

import (
	"fmt"
	"testing"

	"github.com/aidarbn/platform-go/kit/logx"
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

func TestAlertsCounted(t *testing.T) {
	app := platform.NewApp(nil)
	app.DeclareAlerts("pool_empty", "code_send_failed")

	app.Logger().With("module", "registration").Error("send failed", logx.AlertKey, "code_send_failed")
	app.Logger().Error("unknown reason", logx.AlertKey, "unknown_reason")

	got := map[string]float64{}
	families, err := app.Metrics().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "app_alerts_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			got[m.GetLabel()[0].GetValue()] = m.GetCounter().GetValue()
		}
	}
	want := map[string]float64{"pool_empty": 0, "code_send_failed": 1, "unknown_reason": 1}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("app_alerts_total = %v, want %v: the discarding logger still counts, declared kinds start at zero", got, want)
	}
}

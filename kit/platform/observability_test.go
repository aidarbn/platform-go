package platform_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/aidarbn/platform-go/kit/platform"
)

func scrapeOps(t *testing.T, addr, accept string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/metrics", nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics = %d: %s", resp.StatusCode, raw)
	}
	return string(raw)
}

// An aliased metric is served under both names with the same values, so dashboards
// written for the old name keep working; a real metric wins over an alias of its name.
func TestMetricAliasesAtMetrics(t *testing.T) {
	calls := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "api_deprecated_calls_total", Help: "deprecated calls"}, []string{"method"})
	taken := prometheus.NewCounter(prometheus.CounterOpts{Name: "taken_total", Help: "a real metric"})
	wire := func(app *platform.App) error {
		app.Metrics().MustRegister(calls, taken)
		app.AliasMetric("api_deprecated_calls_total", "maas_deprecated_rpc_calls_total")
		app.AliasMetric("api_deprecated_calls_total", "maas_deprecated_rpc_calls_total") // twice is once
		app.AliasMetric("api_deprecated_calls_total", "old_calls_total")
		app.AliasMetric("app_alerts_total", "taken_total")     // a real metric of that name wins
		app.AliasMetric("not_registered_total", "ghost_total") // nothing to copy
		return nil
	}
	addr, stop := runUntilStarted(t, testConfig(nil), nil, wire)
	defer func() { _ = stop() }()

	calls.WithLabelValues("/a.v1.S/Old").Add(3)
	taken.Add(7)
	body := scrapeOps(t, addr, "")
	for _, want := range []string{
		`api_deprecated_calls_total{method="/a.v1.S/Old"} 3`,
		`maas_deprecated_rpc_calls_total{method="/a.v1.S/Old"} 3`,
		`old_calls_total{method="/a.v1.S/Old"} 3`,
		`# HELP maas_deprecated_rpc_calls_total deprecated calls (alias of api_deprecated_calls_total)`,
		"taken_total 7",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	if strings.Count(body, "# TYPE taken_total") != 1 || strings.Contains(body, "ghost_total") {
		t.Errorf("an alias clashes with a real metric or copies nothing:\n%s", body)
	}
	if strings.Index(body, "maas_deprecated") > strings.Index(body, "old_calls_total") {
		t.Error("families are not sorted by name")
	}
}

func TestAliasMetricRejectsBadNames(t *testing.T) {
	app := platform.NewApp(nil)
	app.AliasMetric("a_total", "b_total")
	for name, fn := range map[string]func(){
		"bad name":     func() { app.AliasMetric("a-total", "b_total") },
		"bad alias":    func() { app.AliasMetric("a_total", "") },
		"itself":       func() { app.AliasMetric("a_total", "a_total") },
		"alias of two": func() { app.AliasMetric("c_total", "b_total") },
		"starts digit": func() { app.AliasMetric("a_total", "1b") },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
			}()
			fn()
		})
	}
}

// METRICS_OPENMETRICS answers a scraper asking for OpenMetrics in that format; without
// it /metrics stays in the classic text format whatever the scraper asks.
func TestOpenMetricsOptIn(t *testing.T) {
	const accept = "application/openmetrics-text;version=1.0.0"
	addr, stop := runUntilStarted(t, testConfig(nil), nil, func(app *platform.App) error {
		if app.OpenMetrics() {
			t.Error("OpenMetrics is on by default")
		}
		return nil
	})
	if body := scrapeOps(t, addr, accept); strings.Contains(body, "# EOF") {
		t.Error("OpenMetrics is served by default")
	}
	_ = stop()

	t.Setenv("METRICS_OPENMETRICS", "true")
	addr, stop = runUntilStarted(t, testConfig(nil), nil, func(app *platform.App) error {
		if !app.OpenMetrics() {
			t.Error("METRICS_OPENMETRICS=true is not reported to modules")
		}
		return nil
	})
	defer func() { _ = stop() }()
	if body := scrapeOps(t, addr, accept); !strings.Contains(body, "# EOF") {
		t.Errorf("OpenMetrics is not served with METRICS_OPENMETRICS=true:\n%.200s", body)
	}
	if body := scrapeOps(t, addr, ""); strings.Contains(body, "# EOF") {
		t.Error("a scraper that does not ask for OpenMetrics gets it")
	}
}

func TestBadOpenMetricsVariableStopsStart(t *testing.T) {
	t.Setenv("METRICS_OPENMETRICS", "maybe")
	if err := platform.RunContext(t.Context(), testConfig(nil), nil, nil); err == nil {
		t.Error("a bad METRICS_OPENMETRICS is accepted")
	}
}

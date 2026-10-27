package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/platform"
)

const (
	oldEcho = "/platformtest.v1.EchoService/OldEcho"
	newEcho = "/platformtest.v1.EchoService/Echo"
	fail    = "/platformtest.v1.EchoService/Fail"
)

// With replacements the deprecated metric gets taply's replacement label, a listed
// method counts as deprecated without the proto option, and the log names the
// replacement; a method the API does not serve is reported.
func TestDeprecatedReplacements(t *testing.T) {
	extraWire = func(app *platform.App) {
		api.DeprecatedReplacements(app, map[string]string{oldEcho: newEcho})
		api.DeprecatedReplacements(app, map[string]string{fail: "", "/gone.v1.S/M": "x"})
		app.AliasMetric("api_deprecated_calls_total", "maas_deprecated_rpc_calls_total")
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{})

	s.do("POST", "/v1/old-echo", `{"message_text":"x"}`, nil)
	s.do("GET", "/v1/fail/none", "", nil)
	s.do("POST", "/v1/echo", `{"message_text":"x"}`, nil)

	metrics := scrape(t, s.ops)
	for _, want := range []string{
		`api_deprecated_calls_total{method="` + oldEcho + `",replacement="` + newEcho + `"} 1`,
		`api_deprecated_calls_total{method="` + fail + `",replacement=""} 1`,
		// Taply's dashboards read the old name while they move to the platform's.
		`maas_deprecated_rpc_calls_total{method="` + oldEcho + `",replacement="` + newEcho + `"} 1`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
	if strings.Contains(metrics, `api_deprecated_calls_total{method="`+newEcho+`"`) {
		t.Error("a current method is counted as deprecated")
	}
	logs := s.logs.String()
	for _, want := range []string{`"replacement":"` + newEcho + `"`, `names a method the API does not serve`, `/gone.v1.S/M`} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %s", want)
		}
	}
}

// API_METRICS_TENANT_HEADER labels the call metrics with tenant_name from the header,
// unknown without it, for REST and gRPC alike; by default there is no such label.
func TestTenantLabel(t *testing.T) {
	s := start(t, api.Config{})
	s.do("POST", "/v1/echo", `{"message_text":"x"}`, map[string]string{"Tenant-Name": "acme"})
	if metrics := scrape(t, s.ops); strings.Contains(metrics, "tenant_name") || !strings.Contains(metrics,
		`grpc_server_handled_total{grpc_code="OK",grpc_method="Echo",grpc_service="platformtest.v1.EchoService",grpc_type="unary"} 1`) {
		t.Error("the default call metrics changed")
	}

	s2 := start(t, api.Config{MetricsTenantHeader: "Tenant-Name"})
	s2.do("POST", "/v1/echo", `{"message_text":"x"}`, map[string]string{"Tenant-Name": "acme"})
	s2.do("POST", "/v1/echo", `{"message_text":"x"}`, map[string]string{"Tenant-Name": "acme"})
	s2.do("POST", "/v1/echo", `{"message_text":"x"}`, nil)
	metrics := scrape(t, s2.ops)
	for _, want := range []string{
		`grpc_server_handled_total{grpc_code="OK",grpc_method="Echo",grpc_service="platformtest.v1.EchoService",grpc_type="unary",tenant_name="acme"} 2`,
		`grpc_server_handled_total{grpc_code="OK",grpc_method="Echo",grpc_service="platformtest.v1.EchoService",grpc_type="unary",tenant_name="unknown"} 1`,
		`grpc_server_handling_seconds_count{grpc_method="Echo",grpc_service="platformtest.v1.EchoService",grpc_type="unary",tenant_name="acme"} 2`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
}

func scrapeOpenMetrics(t *testing.T, ops string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://"+ops+"/metrics", nil)
	req.Header.Set("Accept", "application/openmetrics-text;version=1.0.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}

// With METRICS_OPENMETRICS a sampled call leaves its trace id as an exemplar of the call
// metrics; without it nothing is attached.
func TestTraceExemplars(t *testing.T) {
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	const exemplar = `# {traceID="4bf92f3577b34da6a3ce929d0e0e4736"}`

	s := start(t, api.Config{})
	s.do("POST", "/v1/echo", `{"message_text":"x"}`, map[string]string{"traceparent": traceparent})
	if strings.Contains(scrapeOpenMetrics(t, s.ops), "traceID") {
		t.Error("exemplars without METRICS_OPENMETRICS")
	}

	t.Setenv("METRICS_OPENMETRICS", "true")
	s2 := start(t, api.Config{})
	s2.do("POST", "/v1/echo", `{"message_text":"x"}`, map[string]string{"traceparent": traceparent})
	// An unsampled call has no trace worth linking.
	s2.do("POST", "/v1/echo", `{"message_text":"x"}`, map[string]string{"traceparent": "00-11111111111111111111111111111111-00f067aa0ba902b7-00"})
	metrics := scrapeOpenMetrics(t, s2.ops)
	if !strings.Contains(metrics, exemplar) {
		t.Errorf("no trace exemplar:\n%s", metrics)
	}
	if strings.Contains(metrics, "11111111111111111111111111111111") {
		t.Error("an unsampled call left an exemplar")
	}
}

// A failed request on a masked path keeps its bodies out of the access log; other
// paths log them as before.
func TestMaskLogBodies(t *testing.T) {
	extraWire = func(app *platform.App) { api.MaskLogBodies(app, "/v1/old-echo") }
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{AccessLog: true, LogBodies: true, LogBodiesMaskPaths: []string{"", "/v1/things"}})

	s.do("POST", "/v1/old-echo", `{"message_text":"ok"}`, nil) // succeeds: no bodies logged at all
	s.do("POST", "/v1/old-echo", `{"message_text":""}`, nil)
	s.do("POST", "/v1/things", `{"secret-two":`, nil)
	s.do("POST", "/v1/echo", `{"message_text":""}`, nil)

	logs := s.logs.String()
	if strings.Contains(logs, "secret-two") || strings.Count(logs, `"req_body":"[REDACTED]"`) < 2 || !strings.Contains(logs, `"res_body":"[REDACTED]"`) {
		t.Errorf("masked bodies leak into the log:\n%s", logs)
	}
	if !strings.Contains(logs, `"req_body":"{\"message_text\":\"\"}","res_body":"{`) {
		t.Errorf("bodies of other paths are not logged:\n%s", logs)
	}
}

func TestObservabilityFromEnvironment(t *testing.T) {
	t.Setenv("API_METRICS_TENANT_HEADER", "Tenant-Name")
	t.Setenv("API_LOG_BODIES_MASK_PATHS", "/kaspi-certificate, /v1/keys")
	l := confx.New("")
	cfg := api.Load(l)
	if l.Err() != nil || cfg.MetricsTenantHeader != "Tenant-Name" || len(cfg.LogBodiesMaskPaths) != 2 || cfg.LogBodiesMaskPaths[1] != "/v1/keys" {
		t.Errorf("%+v %v", cfg, l.Err())
	}

	t.Setenv("API_METRICS_TENANT_HEADER", "tenant name")
	l = confx.New("")
	api.Load(l)
	if l.Err() == nil {
		t.Error("a bad header name is accepted")
	}

	t.Setenv("API_METRICS_TENANT_HEADER", "")
	t.Setenv("API_LOG_BODIES_MASK_PATHS", "")
	if cfg := api.Load(confx.New("")); cfg.MetricsTenantHeader != "" || cfg.LogBodiesMaskPaths != nil {
		t.Errorf("defaults: %+v", cfg)
	}
}

// The deprecated metric is registered at start; a project metric of the same name stops
// the start instead of being silently shadowed.
func TestDeprecatedMetricClashStopsStart(t *testing.T) {
	m := api.New(api.Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	cfg := platform.Config{Service: "echo", OpsAddr: "127.0.0.1:0", Logger: logx.New(logx.Options{Writer: io.Discard})}
	err := platform.RunContext(t.Context(), cfg, []platform.Module{m}, func(app *platform.App) error {
		app.Metrics().MustRegister(prometheus.NewCounter(prometheus.CounterOpts{Name: "api_deprecated_calls_total", Help: "mine"}))
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "api metrics") {
		t.Errorf("Run = %v", err)
	}
}

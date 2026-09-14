package gen_test

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"

	"github.com/aidarbn/platform-go/internal/gen"
)

const withMonitoring = "schema: 1\nproject:\n  module: github.com/aidarbn/shop-api\nmodules:\n  monitoring: {}\n"

const withMonitoringAll = "schema: 1\nproject:\n  module: github.com/aidarbn/shop-api\nmodules:\n  monitoring: {}\n  api: {}\n  postgres: {}\n  river: {}\n  settings: {}\n"

func TestMonitoringStaysOutOfWiring(t *testing.T) {
	files, err := gen.FilesFrom(mustParse(t, withMonitoring), withSettingsFS())
	if err != nil {
		t.Fatalf("FilesFrom: %v", err)
	}
	for _, path := range []string{gen.ConfigPath, gen.ModulesPath} {
		if strings.Contains(string(files[path]), "onitoring") {
			t.Errorf("%s mentions the monitoring module:\n%s", path, files[path])
		}
	}
	if !strings.Contains(string(files[gen.EnvPath]), "OTEL_EXPORTER_OTLP_ENDPOINT=") {
		t.Errorf(".env.example lacks the collector endpoint:\n%s", files[gen.EnvPath])
	}
	if _, ok := files[gen.MonitoringStack]; !ok {
		t.Error("the monitoring stack is not generated")
	}
}

func TestMonitoringFilesParse(t *testing.T) {
	files, err := gen.FilesFrom(mustParse(t, withMonitoringAll), withSettingsFS())
	if err != nil {
		t.Fatalf("FilesFrom: %v", err)
	}
	var stack int
	for path, content := range files {
		if !strings.HasPrefix(path, "monitoring/") {
			continue
		}
		stack++
		switch {
		case strings.HasSuffix(path, ".json"):
			var v map[string]any
			if err := json.Unmarshal(content, &v); err != nil {
				t.Errorf("%s: %v", path, err)
			}
		case strings.HasSuffix(path, ".yml"), strings.HasSuffix(path, ".yaml"):
			var v map[string]any
			if err := yaml.Unmarshal(content, &v); err != nil {
				t.Errorf("%s: %v\n%s", path, err, content)
			}
		}
		if path != "monitoring/.env.example" && !gen.IsGenerated(content) && !strings.HasSuffix(path, ".json") {
			t.Errorf("%s lacks the generated mark", path)
		}
	}
	if stack < 10 {
		t.Errorf("only %d files in the stack", stack)
	}
}

func alertNames(t *testing.T, content []byte) map[string]string {
	t.Helper()
	var rules struct {
		Groups []struct {
			Rules []struct {
				Alert string `yaml:"alert"`
				Expr  string `yaml:"expr"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal(content, &rules); err != nil {
		t.Fatalf("alerts.yml: %v", err)
	}
	out := map[string]string{}
	for _, g := range rules.Groups {
		for _, r := range g.Rules {
			out[r.Alert] += r.Expr + "\n"
		}
	}
	return out
}

func TestMonitoringAlertsFollowModules(t *testing.T) {
	bare, err := gen.FilesFrom(mustParse(t, withMonitoring), withSettingsFS())
	if err != nil {
		t.Fatal(err)
	}
	alerts := alertNames(t, bare["monitoring/alerts.yml"])
	if _, ok := alerts["ServiceDown"]; !ok {
		t.Error("ServiceDown is missing")
	}
	for _, name := range []string{"HTTP5xx", "GRPCErrors", "DatabasePoolExhausted", "JobsFailing", "SettingsStale"} {
		if _, ok := alerts[name]; ok {
			t.Errorf("%s is generated without its module", name)
		}
	}
	if strings.Contains(string(bare["monitoring/grafana/dashboards/platform.json"]), "http_gateway") {
		t.Error("the dashboard shows HTTP without the api module")
	}

	full, err := gen.FilesFrom(mustParse(t, withMonitoringAll), withSettingsFS())
	if err != nil {
		t.Fatal(err)
	}
	alerts = alertNames(t, full["monitoring/alerts.yml"])
	for _, name := range []string{"HTTP5xx", "HTTPLatency", "HTTPEndpointLatency", "GRPCErrors", "GRPCLatency", "GRPCMethodLatency", "Panics", "DatabasePoolExhausted", "JobsFailing", "SettingsStale"} {
		if _, ok := alerts[name]; !ok {
			t.Errorf("%s is missing", name)
		}
	}
	// taply's default thresholds.
	if !strings.Contains(alerts["HTTPLatency"], "> 3") || !strings.Contains(alerts["HTTPEndpointLatency"], "> 5") || !strings.Contains(alerts["HTTP5xx"], "> 1") {
		t.Errorf("default thresholds are not taply's: %v", alerts)
	}
}

func TestMonitoringThresholds(t *testing.T) {
	project := withSettingsFS()
	project[gen.MonitoringPath] = &fstest.MapFile{Data: []byte(`go-http:
  overall_latency: 2
  5xx_rate: 0.5
  endpoint_latency_overrides:
    /v1/reports: 20
go-grpc:
  latency_by_endpoint: 7
`)}
	files, err := gen.FilesFrom(mustParse(t, withMonitoringAll), project)
	if err != nil {
		t.Fatal(err)
	}
	alerts := alertNames(t, files["monitoring/alerts.yml"])
	for name, want := range map[string]string{
		"HTTPLatency":         "> 2",
		"HTTP5xx":             "> 0.5",
		"GRPCMethodLatency":   "> 7",
		"GRPCLatency":         "> 3",
		"HTTPEndpointLatency": `path!~"/v1/reports"`,
	} {
		if !strings.Contains(alerts[name], want) {
			t.Errorf("%s lacks %q:\n%s", name, want, alerts[name])
		}
	}
	if !strings.Contains(alerts["HTTPEndpointLatency"], `{path="/v1/reports"}[5m]))) > 20`) {
		t.Errorf("the override has no rule of its own:\n%s", alerts["HTTPEndpointLatency"])
	}
}

func TestMonitoringRejectsUnknownKeys(t *testing.T) {
	for name, content := range map[string]string{
		"section": "go-kafka:\n  overall_latency: 1\n",
		"key":     "go-http:\n  p99: 1\n",
	} {
		project := fstest.MapFS{gen.MonitoringPath: {Data: []byte(content)}}
		if _, err := gen.FilesFrom(mustParse(t, withMonitoring), project); err == nil {
			t.Errorf("unknown %s is accepted", name)
		}
	}
}

func TestMonitoringExampleIsTheDefault(t *testing.T) {
	empty, err := gen.FilesFrom(mustParse(t, withMonitoringAll), withSettingsFS())
	if err != nil {
		t.Fatal(err)
	}
	example, err := gen.FilesFrom(mustParse(t, withMonitoringAll), withMonitoringExample())
	if err != nil {
		t.Fatal(err)
	}
	if string(empty["monitoring/alerts.yml"]) != string(example["monitoring/alerts.yml"]) {
		t.Error("the example file changes the defaults")
	}
}

func withSettingsFS() fstest.MapFS {
	return fstest.MapFS{"settings.yaml": {Data: []byte(gen.SettingsExample)}}
}

func withMonitoringExample() fstest.MapFS {
	project := withSettingsFS()
	project[gen.MonitoringPath] = &fstest.MapFile{Data: []byte(gen.MonitoringExample)}
	return project
}

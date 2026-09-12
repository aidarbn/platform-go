package platform_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/platform"
)

// journal records module calls so tests can assert the lifecycle order.
type journal struct {
	mu     sync.Mutex
	events []string
}

func (j *journal) add(event string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.events = append(j.events, event)
}

func (j *journal) list() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.events...)
}

type fakeModule struct {
	name      string
	journal   *journal
	initErr   error
	startErr  error
	stopErr   error
	healthErr error
}

func (m *fakeModule) Name() string { return m.name }

func (m *fakeModule) Init(_ context.Context, _ *platform.App) error {
	m.journal.add("init:" + m.name)
	return m.initErr
}

func (m *fakeModule) Start(context.Context) error {
	m.journal.add("start:" + m.name)
	return m.startErr
}

func (m *fakeModule) Stop(context.Context) error {
	m.journal.add("stop:" + m.name)
	return m.stopErr
}

func (m *fakeModule) Health(context.Context) error { return m.healthErr }

// bareModule implements only the required part of Module.
type bareModule struct {
	name    string
	journal *journal
}

func (m *bareModule) Name() string { return m.name }

func (m *bareModule) Init(_ context.Context, _ *platform.App) error {
	m.journal.add("init:" + m.name)
	return nil
}

func testConfig(started func(string)) platform.Config {
	return platform.Config{
		Service:         "test",
		OpsAddr:         "127.0.0.1:0",
		ShutdownTimeout: 5 * time.Second,
		Logger:          logx.New(logx.Options{Writer: io.Discard}),
		OnStarted:       started,
	}
}

// runUntilStarted boots the application, waits for startup and returns the ops address
// together with a stop function that waits for Run to return.
func runUntilStarted(t *testing.T, cfg platform.Config, modules []platform.Module, wire platform.Wire) (string, func() error) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan string, 1)
	errCh := make(chan error, 1)

	prev := cfg.OnStarted
	cfg.OnStarted = func(addr string) {
		if prev != nil {
			prev(addr)
		}
		addrCh <- addr
	}

	go func() { errCh <- platform.RunContext(ctx, cfg, modules, wire) }()

	select {
	case addr := <-addrCh:
		return addr, func() error {
			cancel()
			select {
			case err := <-errCh:
				return err
			case <-time.After(10 * time.Second):
				return errors.New("Run did not return after the context was cancelled")
			}
		}
	case err := <-errCh:
		cancel()
		t.Fatalf("Run returned before startup: %v", err)
		return "", nil
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("the application did not start")
		return "", nil
	}
}

func TestLifecycleOrder(t *testing.T) {
	j := &journal{}
	modules := []platform.Module{
		&fakeModule{name: "postgres", journal: j},
		&bareModule{name: "settings", journal: j},
		&fakeModule{name: "river", journal: j},
	}

	_, stop := runUntilStarted(t, testConfig(nil), modules, func(*platform.App) error {
		j.add("wire")
		return nil
	})
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{
		"init:postgres", "init:settings", "init:river",
		"wire",
		"start:postgres", "start:river",
		"stop:river", "stop:postgres", // reverse order
	}
	got := j.list()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("call order:\n got  %v\n want %v", got, want)
	}
}

func TestStartErrorStopsInitialized(t *testing.T) {
	j := &journal{}
	modules := []platform.Module{
		&fakeModule{name: "postgres", journal: j},
		&fakeModule{name: "river", journal: j, startErr: errors.New("queue unavailable")},
	}

	err := platform.RunContext(context.Background(), testConfig(nil), modules, nil)
	if err == nil || !strings.Contains(err.Error(), "queue unavailable") {
		t.Fatalf("want a start error, got %v", err)
	}
	if got := strings.Join(j.list(), ","); !strings.Contains(got, "stop:river,stop:postgres") {
		t.Errorf("modules that are up must be stopped: %v", j.list())
	}
}

func TestInitErrorStopsPrevious(t *testing.T) {
	j := &journal{}
	modules := []platform.Module{
		&fakeModule{name: "postgres", journal: j},
		&fakeModule{name: "settings", journal: j, initErr: errors.New("schema is missing")},
		&fakeModule{name: "river", journal: j},
	}

	err := platform.RunContext(context.Background(), testConfig(nil), modules, nil)
	if err == nil || !strings.Contains(err.Error(), "schema is missing") {
		t.Fatalf("want an init error, got %v", err)
	}
	got := j.list()
	if strings.Join(got, ",") != "init:postgres,init:settings,stop:postgres" {
		t.Errorf("after a failed init: %v", got)
	}
}

func TestWireErrorStopsModules(t *testing.T) {
	j := &journal{}
	modules := []platform.Module{&fakeModule{name: "postgres", journal: j}}

	err := platform.RunContext(context.Background(), testConfig(nil), modules,
		func(*platform.App) error { return errors.New("handler is missing") })

	if err == nil || !strings.Contains(err.Error(), "wire domain") {
		t.Fatalf("want a wiring error, got %v", err)
	}
	if got := strings.Join(j.list(), ","); got != "init:postgres,stop:postgres" {
		t.Errorf("modules must be stopped: %v", j.list())
	}
}

func TestHealthEndpoint(t *testing.T) {
	j := &journal{}
	modules := []platform.Module{
		&fakeModule{name: "postgres", journal: j},
		&bareModule{name: "settings", journal: j}, // no Health, so it is absent from the response
	}

	addr, stop := runUntilStarted(t, testConfig(nil), modules, nil)

	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer resp.Body.Close()

	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StatusCode != http.StatusOK || body.Status != "ok" || body.Checks["postgres"] != "ok" {
		t.Errorf("health = %d %+v", resp.StatusCode, body)
	}
	if _, ok := body.Checks["settings"]; ok {
		t.Error("a module without HealthChecker must not appear in health")
	}
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestHealthReportsFailure(t *testing.T) {
	j := &journal{}
	modules := []platform.Module{
		&fakeModule{name: "river", journal: j, healthErr: errors.New("no connection")},
	}

	addr, stop := runUntilStarted(t, testConfig(nil), modules, nil)

	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(raw), "no connection") {
		t.Errorf("health = %d %s", resp.StatusCode, raw)
	}
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	addr, stop := runUntilStarted(t, testConfig(nil), nil, nil)

	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "go_goroutines") {
		t.Errorf("metrics = %d, body: %.120s", resp.StatusCode, raw)
	}
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestWireSeesContainer(t *testing.T) {
	j := &journal{}
	type repo struct{ name string }

	modules := []platform.Module{&providerModule{journal: j}}
	var got *repo

	_, stop := runUntilStarted(t, testConfig(nil), modules, func(app *platform.App) error {
		got = &repo{name: platform.Get[string](app)}
		return nil
	})
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got == nil || got.name != "from the module" {
		t.Errorf("wiring did not see the value from the container: %+v", got)
	}
}

// providerModule puts a value into the container during Init.
type providerModule struct{ journal *journal }

func (m *providerModule) Name() string { return "provider" }

func (m *providerModule) Init(_ context.Context, app *platform.App) error {
	m.journal.add("init:provider")
	platform.Provide(app, "from the module")
	return nil
}

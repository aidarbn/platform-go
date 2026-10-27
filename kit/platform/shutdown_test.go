package platform_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/kit/platform"
)

// phasedModule is a module whose shutdown is split into phases, as the api module
// splits HTTP from gRPC.
type phasedModule struct {
	fakeModule
	phases []platform.StopPhase
}

func (m *phasedModule) StopPhases() []platform.StopPhase { return m.phases }

func phase(j *journal, module, name string, order int, err error) platform.StopPhase {
	return platform.StopPhase{Name: name, Order: order, Stop: func(context.Context) error {
		j.add("phase:" + module + "." + name)
		return err
	}}
}

// The modules of a project in the order the generator lists them: api first, the
// database after it.
func phasedModules(j *journal, apiErr error) []platform.Module {
	return []platform.Module{
		&phasedModule{fakeModule: fakeModule{name: "api", journal: j}, phases: []platform.StopPhase{
			phase(j, "api", "http", platform.PhaseHTTP, nil),
			phase(j, "api", "grpc", platform.PhaseGRPC, apiErr),
		}},
		&fakeModule{name: "postgres", journal: j},
		&phasedModule{fakeModule: fakeModule{name: "river", journal: j}, phases: []platform.StopPhase{
			phase(j, "river", "queue", platform.PhaseQueue, nil),
		}},
		&bareModule{name: "settings", journal: j},
		&phasedModule{fakeModule: fakeModule{name: "admin", journal: j}, phases: []platform.StopPhase{
			phase(j, "admin", "http", platform.PhaseHTTP, nil),
		}},
		&fakeModule{name: "web", journal: j},
	}
}

func stops(events []string) string {
	var out []string
	for _, e := range events {
		if strings.HasPrefix(e, "stop:") || strings.HasPrefix(e, "phase:") {
			out = append(out, e)
		}
	}
	return strings.Join(out, ",")
}

// Without phased shutdown the modules stop as before: Stop in reverse order of
// initialisation, phases unused.
func TestShutdownWithoutPhasesKeepsReverseOrder(t *testing.T) {
	t.Setenv("SHUTDOWN_PHASES", "")
	j := &journal{}
	_, stop := runUntilStarted(t, testConfig(nil), phasedModules(j, nil), nil)
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "stop:web,stop:admin,stop:river,stop:postgres,stop:api"
	if got := stops(j.list()); got != want {
		t.Errorf("stop order:\n got  %s\n want %s", got, want)
	}
}

// Phased shutdown stops the HTTP servers, then the queue, then gRPC, as taply does, and
// the modules without phases last, so the database outlives what uses it.
func TestShutdownPhasesOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		code bool
	}{
		{name: "from the environment", env: "true"},
		{name: "from the config", code: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHUTDOWN_PHASES", tc.env)
			j := &journal{}
			cfg := testConfig(nil)
			cfg.ShutdownPhases = tc.code
			_, stop := runUntilStarted(t, cfg, phasedModules(j, nil), nil)
			if err := stop(); err != nil {
				t.Fatalf("Run: %v", err)
			}
			want := "phase:admin.http,phase:api.http,phase:river.queue,phase:api.grpc,stop:web,stop:postgres"
			if got := stops(j.list()); got != want {
				t.Errorf("stop order:\n got  %s\n want %s", got, want)
			}
		})
	}
}

// A failing phase is reported with its module and phase, and shutdown goes on.
func TestShutdownPhaseErrorIsReported(t *testing.T) {
	j := &journal{}
	cfg := testConfig(nil)
	cfg.ShutdownPhases = true
	_, stop := runUntilStarted(t, cfg, phasedModules(j, errors.New("grpc stuck")), nil)
	err := stop()
	if err == nil || !strings.Contains(err.Error(), "module api: stop grpc: grpc stuck") {
		t.Fatalf("err = %v", err)
	}
	if got := stops(j.list()); !strings.HasSuffix(got, "stop:web,stop:postgres") {
		t.Errorf("shutdown stopped at the failing phase: %s", got)
	}
}

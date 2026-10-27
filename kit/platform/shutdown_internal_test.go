package platform

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type stopRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *stopRecorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

type plainStopper struct {
	name string
	rec  *stopRecorder
}

func (m *plainStopper) Name() string                     { return m.name }
func (m *plainStopper) Init(context.Context, *App) error { return nil }
func (m *plainStopper) Stop(context.Context) error {
	m.rec.add("stop:" + m.name)
	return nil
}

type phasedStopper struct {
	plainStopper
	phases []StopPhase
}

func (m *phasedStopper) StopPhases() []StopPhase { return m.phases }

// A phase that hangs is bounded by SHUTDOWN_TIMEOUT, so the phases after it still run
// and the process exits before the orchestrator kills it. The clock is the fake one of
// synctest: the test takes no real time.
func TestShutdownPhasesShareTheTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &stopRecorder{}
		var hung time.Duration
		inited := []Module{
			&phasedStopper{plainStopper: plainStopper{name: "api", rec: rec}, phases: []StopPhase{{
				Name: "grpc", Order: PhaseGRPC,
				Stop: func(ctx context.Context) error {
					dl, _ := ctx.Deadline()
					rec.add("phase:api.grpc left " + time.Until(dl).String())
					return nil
				},
			}}},
			&plainStopper{name: "postgres", rec: rec},
			&phasedStopper{plainStopper: plainStopper{name: "river", rec: rec}, phases: []StopPhase{{
				Name: "queue", Order: PhaseQueue,
				Stop: func(ctx context.Context) error {
					start := time.Now()
					<-ctx.Done()
					hung = time.Since(start)
					return ctx.Err()
				},
			}}},
		}
		cfg := Config{ShutdownTimeout: 3 * time.Second}
		run, cancel := context.WithCancel(context.Background())
		cancel()
		ctx, done := shutdownContext(run, cfg)
		defer done()

		err := stopAll(ctx, NewApp(nil), inited, true)
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "module river: stop queue") {
			t.Fatalf("err = %v", err)
		}
		if hung != 3*time.Second {
			t.Errorf("the hanging phase waited %s, want the shutdown timeout", hung)
		}
		if got := strings.Join(rec.events, ","); got != "phase:api.grpc left 0s,stop:postgres" {
			t.Errorf("after the hanging phase: %s", got)
		}
	})
}

// A PhasedStopper that returns no phases is stopped with Stop, so its resources are
// still released.
func TestShutdownPhasedStopperWithoutPhasesIsStopped(t *testing.T) {
	rec := &stopRecorder{}
	inited := []Module{
		&phasedStopper{plainStopper: plainStopper{name: "empty", rec: rec}},
		&phasedStopper{plainStopper: plainStopper{name: "api", rec: rec}, phases: []StopPhase{{
			Name: "http", Order: PhaseHTTP,
			Stop: func(context.Context) error { rec.add("phase:api.http"); return nil },
		}}},
	}
	if err := stopAll(context.Background(), NewApp(nil), inited, true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rec.events, ","); got != "phase:api.http,stop:empty" {
		t.Errorf("stops: %s", got)
	}
}

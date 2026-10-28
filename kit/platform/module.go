package platform

import (
	"context"
	"errors"
)

// Module is an infrastructure part of the application: database, queues, API, admin UI.
//
// Only Name and Init are required. Everything else is optional: the platform checks
// for Starter, Stopper and HealthChecker and calls them when implemented, so a module
// without background work stays a few lines long.
type Module interface {
	// Name identifies the module in logs, health output and errors.
	Name() string

	// Init prepares the module and puts whatever it shares into the container.
	// Modules are initialised in the order they are listed.
	Init(ctx context.Context, app *App) error
}

// Starter runs background work: queue workers, servers, subscriptions. It is called
// after the domain is wired so that every handler is registered before traffic starts.
type Starter interface {
	Start(ctx context.Context) error
}

// Stopper releases resources. Modules are stopped in reverse order of initialisation,
// sharing one shutdown timeout.
type Stopper interface {
	Stop(ctx context.Context) error
}

// HealthChecker adds the module to the /health response.
type HealthChecker interface {
	Health(ctx context.Context) error
}

// Degraded marks a health check failure of a part the instance can work without, such
// as optional storage: /health reports the check as "degraded: <error>" and the whole
// response as "degraded" with status 200, not 503, so a probe does not take the
// instance out or restart it. A nil error stays nil.
func Degraded(err error) error {
	if err == nil {
		return nil
	}
	return degradedError{err}
}

// IsDegraded reports whether err, or an error it wraps, came from Degraded.
func IsDegraded(err error) bool {
	var d degradedError
	return errors.As(err, &d)
}

type degradedError struct{ err error }

func (e degradedError) Error() string { return e.err.Error() }
func (e degradedError) Unwrap() error { return e.err }

// Shutdown phases a module can place its steps in. With phased shutdown on
// (SHUTDOWN_PHASES), steps run in this order across all modules, as in taply: the HTTP
// servers stop taking requests first, the job queue drains next while gRPC clients are
// still served, and gRPC goes last. Any number in between orders a step of its own.
const (
	PhaseHTTP  = 100
	PhaseQueue = 200
	PhaseGRPC  = 300
)

// StopPhase is one step of a module's shutdown.
type StopPhase struct {
	Name  string // shown in logs and errors: "http", "queue", "grpc"
	Order int    // PhaseHTTP, PhaseQueue, PhaseGRPC or a number between them
	Stop  func(ctx context.Context) error
}

// PhasedStopper splits a module's shutdown into steps that phased shutdown interleaves
// with the steps of other modules. Its phases together must do what Stop does: without
// phased shutdown the platform calls Stop, with it only the phases.
//
// Modules that are not PhasedStopper stop after every phase, in reverse order of
// initialisation, so the database outlives the servers and the queue that use it.
type PhasedStopper interface {
	Stopper
	StopPhases() []StopPhase
}

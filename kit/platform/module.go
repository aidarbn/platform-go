package platform

import "context"

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

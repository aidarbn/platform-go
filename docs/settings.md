# Levels of settings

A project has four kinds of settings. They live in different places because different people change them at different times.

| Level | What | Where | Who changes it | When it takes effect |
|---|---|---|---|---|
| **project layout** | which modules are enabled and their technical parameters: paths, addresses, generation, optional services; linters, CI | `platformgo.yaml` | developer | `platformgo apply`, deploy |
| **environment** | database and service addresses, secrets, pool sizes, worker counts per environment | environment variables and deployment secrets; `.env.example` is generated | operations | application start |
| **business settings** | schedules, limits, timeouts, flags, retry counts, use case parameters | the `settings` module: a `settings.yaml` schema in the project, values in the database, edited from the admin UI | administrator, product | immediately, without a deploy |
| **declarations in code** | which jobs and queues exist, which buckets, which interceptors and in what order | Go code of the project: registration in `cmd/app/wire.go` and adapters | developer | build |

## Business settings: the `settings` module

The schema describes the settings, their types, defaults and constraints. `platformgo generate` turns it into typed code; values are stored in the database and cached, and the admin UI edits them.

```yaml
# settings.yaml — the business settings schema of the project
settings:
  orders.cleanup:
    enabled:  { type: bool,     default: true }
    schedule: { type: cron,     default: "0 3 * * *" }
  orders.create:
    max_attempts: { type: int,      default: 5, min: 1, max: 20 }
    timeout:      { type: duration, default: 30s }
  api.ratelimit:
    rps:   { type: int, default: 50 }
    burst: { type: int, default: 100 }
```

```go
// generated from settings.yaml
s := settings.From(app)
s.OrdersCreate().MaxAttempts() // 5 until the database says otherwise
s.OrdersCleanup().Schedule()   // "0 3 * * *"
```

## Declarations in code

Jobs, queues and their link to business settings live in project code, not in the platform config:

```go
// cmd/app/wire.go
func wireDomain(app *platform.App) error {
	s := settings.From(app)

	river.AddWorker(app, workers.NewCleanupExpired(repo))
	river.Periodic(app, jobs.CleanupExpiredArgs{},
		river.ScheduleFrom(s.OrdersCleanup().Schedule), // schedule from business settings
		river.EnabledFrom(s.OrdersCleanup().Enabled))
	return nil
}
```

Worker counts per queue are an environment parameter (`RIVER_QUEUES=default=5,notify=3`): production and staging differ in capacity while the meaning of the jobs stays the same.

## Why this split

- **The platform knows nothing about the domain.** `platformgo.yaml` looks the same in every project; business logic does not leak into the tool.
- **Business settings change without a developer and without a deploy.** An administrator edits a schedule or a limit in the admin UI: no `apply`, no release.
- **Environment is separate from meaning.** The same code and the same business settings run on different environments with different resources.

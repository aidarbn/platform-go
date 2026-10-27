# Levels of settings

A project has four kinds of settings. They live in different places because different people change them at different times.

| Level | What | Where | Who changes it | When it takes effect |
|---|---|---|---|---|
| **project layout** | which modules are enabled and their technical parameters: paths, addresses, generation, optional services; linters, CI | `platformgo.yaml` | developer | `platformgo apply`, deploy |
| **environment** | database and service addresses, secrets, pool sizes, worker counts per environment | environment variables and deployment secrets; `.env.example` is generated | operations | application start |
| **business settings** | schedules, limits, timeouts, flags, retry counts, use case parameters | the `settings` module: a `settings.yaml` schema in the project, values in the database, edited from the admin UI | administrator, product | immediately, without a deploy |
| **declarations in code** | which jobs and queues exist, which buckets, which interceptors and in what order | Go code of the project: registration in `cmd/app/wire.go` and adapters | developer | build |

## Business settings: the `settings` module

The schema describes the settings, their types, defaults and constraints. `platformgo generate` turns it into typed code in `internal/settings/settings.gen.go`; values are stored in the database and cached, and the admin UI edits them.

```yaml
# platformgo.yaml — technical side of the module
modules:
  settings:
    schema: settings.yaml   # where the schema lives; this is the default
```

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

The format is taply's configuration schema, so a taply `configs/schema.yaml` works unchanged:

```yaml
configs:                     # or settings:
  sync:
    _description: "Menu synchronisation with external systems"
    queue_workers:
      type: int
      default: 3
      description: "Workers of the sync queue"
      requires_restart: true   # the admin panel warns that the value applies after a restart
  payments:
    kaspi:                     # groups nest: payments.kaspi.fee_percent
      fee_percent: { type: float, default: 0.95, min: 0, max: 100 }
```

Types: `bool`, `int`, `int64`, `float`, `duration`, `string` (with optional `options`) and `cron`. Numbers and durations accept `min` and `max`; every value is validated against the schema before it is stored, so the admin UI cannot write a limit the code would choke on. Descriptions of groups and settings are shown in the admin panel and in the comments of the generated accessors.

```go
// internal/settings/settings.gen.go, generated from settings.yaml
import appsettings "github.com/me/shop-api/internal/settings"

s := appsettings.From(app)
s.OrdersCreate().MaxAttempts() // int:           5 until the database says otherwise
s.OrdersCreate().Timeout()     // time.Duration: 30s
s.OrdersCleanup().Schedule()   // string:        "0 3 * * *"
```

Only overrides are stored, so a setting nobody touched keeps following the default from the schema across releases. A value that stopped matching the schema — a removed setting, a limit now out of bounds — is ignored with a warning rather than breaking the application.

Reads never touch the database: values are cached, `SETTINGS_REFRESH_INTERVAL` (15s by default) says how often the cache is refreshed, which is how a change made on one instance reaches the others. `Store().Watch` reports the keys that changed, and that is what lets a schedule or a limit take effect without a restart.

Project tests need no database:

```go
store := settingsx.NewTestStore(appsettings.Schema, map[string]string{
	"orders.create.max_attempts": "1",
})
s := appsettings.New(store)
```

## Moving existing settings

A project that kept its settings elsewhere — taply's `app_configs`, a file, an older configuration service — moves them with `settings.Import` (the settings module must be initialised, so it runs from `wireDomain` or a command built on the platform). Only overrides are stored, and that matters: taply's `EnsureDefaults` writes a row for every key, defaults included, and copying the table as is would freeze every default at today's value.

```go
report, err := settings.Import(ctx, app, values, "import:app_configs") // values map[string]string
```

For a table there is `settings.ImportQuery`: the query returns the key and the value, and a NULL value means "use the default", the way `app_configs` stores it since taply's migration `20260127120000_add_app_configs`.

```go
// a one-off command of the project, or wireDomain of a single release
report, err := settings.ImportQuery(ctx, app, postgres.Pool(app),
	"SELECT key, value FROM app_configs", "import:app_configs")
if err != nil {
	return err
}
app.Logger().Info(report.String())
```

What happens to each key, and the list of the report it lands in:

| Key | List | Stored |
|---|---|---|
| a value that differs from the default | `Imported` | yes |
| equal to the default in any spelling (`60s` = `1m`, `t` = `true`, `05` = `5`, `1.50` = `1.5`, extra spaces in a schedule), or NULL | `Defaults` | no |
| an equal override is already stored | `Unchanged` | no |
| a different override is already stored (NULL included) | `Kept` | no: the stored value wins |
| the schema refuses the value: does not parse, out of bounds, not an option | `Rejected`, with the reason, and a warning in the log | no |
| the schema does not describe the key | `Unknown` | no |

Import never overwrites a stored override, and the check and the insert are one statement (`ON CONFLICT DO NOTHING`), so an administrator's change or another instance importing at the same moment is not lost. Values are stored one by one; after a failure a repeated run carries on. The actor (`by`) is recorded in `updated_by` of every imported value — the authors of the old table are not carried over.

**Run it once.** A repeated run stores nothing twice and keeps every value set since, so starting several instances with the import is safe. But a setting an administrator resets to its default has no override any more, and the next run would bring the old value back: remove the import after the first successful run.

**Read the rejected keys before dropping the source.** A value that does not parse was ignored by taply too, so nothing changes for it. A value out of bounds, outside the options or a schedule the platform refuses did take effect in taply, and after the import the default applies instead, until someone sets a valid value.

The comparison is `settingsx.Equal(def, a, b)`, built on `settingsx.Normalize(def, v)`, which gives the canonical text of a value for each of the seven types; both are there for project code that compares values too. Schedules are compared after removing extra spaces and the case of a shorthand (`@DAILY`); `MON` and `mon` still differ, which at worst stores one more override.

For taply the order is: deploy the import, check the report in the log and `platform_settings` on production, remove the import and drop `app_configs` in the next release.

## Declarations in code

Jobs, queues and their link to business settings live in project code, not in the platform config:

```go
// cmd/app/wire.go
func wireDomain(app *platform.App) error {
	s := appsettings.From(app)

	river.AddWorker(app, workers.NewCleanupExpired(repo))
	river.Periodic(app, jobs.CleanupExpiredArgs{},
		river.ScheduleFrom(s.OrdersCleanup().Schedule), // schedule from business settings
		river.EnabledFrom(s.OrdersCleanup().Enabled))
	return nil
}
```

Worker counts per queue are an environment parameter (`RIVER_QUEUES=default=5,notify=3`): production and staging differ in capacity while the meaning of the jobs stays the same. A project that wants an administrator to size the queues, as taply does with its `*.queue_workers` settings, reads them from business settings with `riverx.QueuesFrom`; mark those settings `requires_restart`, since the queues are sized once at start, and `RIVER_QUEUES`, when set, still wins. The attempts of a job kind can come from business settings the same way, with `riverx.MaxAttemptsFrom`, read on every insert. See the [`river` module](modules.md#the-river-module).

## Why this split

- **The platform knows nothing about the domain.** `platformgo.yaml` looks the same in every project; business logic does not leak into the tool.
- **Business settings change without a developer and without a deploy.** An administrator edits a schedule or a limit in the admin UI: no `apply`, no release.
- **Environment is separate from meaning.** The same code and the same business settings run on different environments with different resources.

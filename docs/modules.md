# Module catalogue

A module is declared as a section in `platformgo.yaml` and applied with `platformgo apply`. Module code lives in `kit/<module>` and is imported; a project only receives settings, generated wiring, managed tool files and owned scaffolds.

| Module | What it gives the project |
|---|---|
| **core** (always) | settings from the environment, slog, `/metrics`, `/health`, graceful shutdown, tracing, profiling, Makefile, Dockerfile, linters, CI |
| **postgres** | pgx pool, goose migrations, application migrations, database in docker-compose, query generation (sqlc and jet) |
| **river** | queues, periodic jobs, workers, riverui, test helpers |
| **api** | gRPC server with an interceptor chain, REST gateway, multipart, OpenAPI 3.1, documentation page |
| **admin** | admin shell: login, roles, TOTP, audit log; pages for settings, jobs and state; a place for project pages |
| **settings** | business settings: the `settings.yaml` schema, values in the database, cache, change notifications, typed access from code |
| **s3** | object storage (MinIO), file uploads, service in docker-compose |
| **keycloak** | Keycloak integration, authorisation interceptor, service in docker-compose |
| **rbac** | casbin: roles and policies |
| **i18n** | translations, locale in context, interceptors, dictionary migration |
| **enums** | enum catalogue from markers in the domain |
| **monitoring** | alert thresholds for an external monitoring agent |

## The `admin` module

It runs together with the application, inside the same binary, on its own port.

```yaml
modules:
  postgres: {}   # required: accounts, sessions and the log live in the database
  settings: {}   # optional: adds the business settings pages
  admin: {}
```

Everything else is an environment parameter: `ADMIN_ADDR` (`127.0.0.1:8081` by default, so the panel is not on the public network), `ADMIN_SESSION_TTL`, `ADMIN_INSECURE_COOKIES` for local development over plain HTTP, and `ADMIN_BOOTSTRAP_EMAIL` with `ADMIN_BOOTSTRAP_PASSWORD`, which create the first account while there are none.

Out of the box: sign in with a password and an optional one time code, roles, accounts, an append-only audit log, business settings pages built from the schema, and an overview. The panel is server rendered HTML with no external assets — it opens on a locked down network — and it creates no files in the project.

Security of the panel: passwords are PBKDF2-HMAC-SHA256, only the hash of a session token is stored, a used one time code cannot be used again, forms are protected by a double submit token, the cookie is `HttpOnly` and `SameSite=Strict`, and disabling an account revokes its sessions at once.

Project pages are added from project code:

```go
admin.AddPage(app, admin.Page{
	Title:   "Orders",
	Path:    "/orders",
	Roles:   []string{"support", "admin"},
	Handler: adminui.NewOrders(orders),
})
```

Removing the admin panel means deleting the section and running `apply`.

## The `postgres` module

A PostgreSQL pool with a health check and pool metrics, the local database in `docker-compose.yml`, and migrations.

Migrations are goose SQL files in `db/migrations`:

```
$ platformgo migrate create create_orders
created db/migrations/20260914073005_create_orders.sql
```

The directory is embedded into the binary by the generated `db/migrations/migrations.gen.go`, and the module applies pending migrations during startup, before any other module touches the database. A session advisory lock makes several instances starting at once apply each migration exactly once. Set `DATABASE_MIGRATE=false` when migrations run as a separate deploy step.

Queries come in two kinds, as in taply:

| Kind | Tool | Where | How to regenerate |
|---|---|---|---|
| static — the query text is known in advance | sqlc | `db/queries/*.sql` → `internal/db/sqlcgen` | `make generate`; no database needed |
| dynamic — filters, sorting, pagination built at run time | jet | the migrated schema → `internal/db/jetgen/{model,table}` | `make db-generate`: migrates the local database and reads its schema |

Both generators are pinned as `tool` in `go.mod` by `platformgo apply` (sqlc v1.31.1, jet v2.16.0) and removed with the module. `db/sqlc.yaml` is generated: schema from the migrations, `pgx/v5`, `numeric` as `decimal.Decimal`, `uuid` as `uuid.UUID`, `jsonb` as `json.RawMessage`, `timestamptz` and `date` as `time.Time`, nullable columns as pointers. `verify` fails when the sqlc output is stale or left from a removed query file.

```go
// static: sqlc on the pool
q := sqlcgen.New(postgres.Pool(app))
order, err := q.GetOrder(ctx, id)

// dynamic: jet on database/sql over the same pool
o := table.Orders
stmt := o.SELECT(o.AllColumns).WHERE(o.Customer.EQ(postgres.String(name))).LIMIT(20)
var orders []model.Orders
err := stmt.QueryContext(ctx, postgres.SQL(app), &orders)
```

jet writes its output under the database name; platformgo moves the packages straight under `internal/db/jetgen`, so import paths are the same for every developer.

## The `settings` module

Business settings of the project: the schema in `settings.yaml`, the values in the database, typed access generated into `internal/settings`.

```yaml
modules:
  postgres: {}       # required: the values live in the database
  settings:
    schema: settings.yaml
```

The module creates its own table (`SETTINGS_TABLE`, `platform_settings` by default), loads the overrides during startup, refreshes them every `SETTINGS_REFRESH_INTERVAL` and reports through `/health` when it can no longer read them. The levels of settings and the generated API are described in [settings.md](settings.md).

## Process roles

One binary, different sets of modules:

| Start | What runs |
|---|---|
| default | every declared module |
| `--role=worker` | queues and what they need; no API, no admin UI |
| `--role=api` | API and admin UI; no workers |

## Dependencies between modules

`river` requires `postgres`, `admin` requires `settings` for the settings pages, `settings` requires `postgres`. `platformgo plan` reports missing modules and offers to add them.

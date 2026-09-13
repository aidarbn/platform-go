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

## The `api` module

A gRPC server and a REST gateway in front of it, generated from the proto files of the project.

```yaml
modules:
  api: {}
```

Proto files go into `proto/`; `go_package` is set by buf managed mode, so a file needs only its package, imports and HTTP annotations:

```proto
syntax = "proto3";
package shop.v1;

import "buf/validate/validate.proto";
import "google/api/annotations.proto";

service OrdersService {
  rpc CreateOrder(CreateOrderRequest) returns (CreateOrderResponse) {
    option (google.api.http) = {post: "/v1/orders", body: "*"};
  }
}

message CreateOrderRequest {
  string customer_name = 1 [(buf.validate.field).string.min_len = 1];
}
```

`make generate` runs buf with the plugins pinned in `go.mod` by `apply` (buf, protoc-gen-go, protoc-gen-go-grpc, protoc-gen-grpc-gateway, protoc-gen-connect-openapi): Go code into `internal/api/gen`, an OpenAPI 3.1 description with the protovalidate rules into `api/openapi/openapi.yaml`. `verify` generates into a scratch directory and fails when the committed code is stale.

The handler is registered from `wire.go`:

```go
api.Register(app, api.Service{
	GRPC:    func(s *grpc.Server) { shopv1.RegisterOrdersServiceServer(s, orders.NewHandler(repo)) },
	Gateway: shopv1.RegisterOrdersServiceHandler,
})
api.AddUnaryInterceptor(app, auth.Interceptor(tokens))           // authentication and the like
api.HandleHTTP(app, "POST /webhooks/kaspi", kaspi.WebhookHandler) // routes that are not gRPC
```

What the module does for every call, REST included — the gateway calls the local gRPC server, so both go through one chain: metrics (`api_grpc_requests_total`, `api_grpc_request_duration_seconds`), logging of failed calls, panic recovery, errors without internal details (a plain error becomes `Internal`, a status keeps its code), project interceptors, protovalidate validation with the violations as details. The REST side speaks snake_case JSON as in the proto files, returns every field, ignores unknown request fields and forwards request headers as gRPC metadata. Also: gRPC health, optional reflection, CORS, `/openapi.yaml` and `/docs`, graceful stop.

The description marks messages with `additionalProperties: false`: that is the contract for clients, while the gateway is more forgiving and ignores unknown fields.

| Variable | Default | |
|---|---|---|
| `API_HTTP_ADDR` | `:8080` | REST gateway |
| `API_GRPC_ADDR` | `127.0.0.1:9091` | gRPC |
| `API_CORS_ORIGINS` | none | allowed browser origins, `*` for any |
| `API_DOCS` | `true` | `/openapi.yaml` and `/docs` |
| `API_REFLECTION` | `false` | gRPC reflection |
| `API_MAX_RECV_MB`, `API_MAX_SEND_MB` | 16, 32 | message size limits |

## The `river` module

Background jobs on River, in the database of the `postgres` module. The module applies River's migrations on start, runs the client, removes finished jobs after their retention period and exports `river_jobs_total{kind,outcome}` and `river_job_duration_seconds`.

Jobs follow the conventions of taply: the arguments carry their own insert options, so every place that queues a job gets the same queue, priority and attempts.

```go
type OrderCleanupArgs struct{}

func (OrderCleanupArgs) Kind() string { return "order_cleanup" }

func (OrderCleanupArgs) InsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Queue: "maintenance", MaxAttempts: 1}
}
```

Declared from `wire.go`:

```go
s := appsettings.From(app)
q := riverx.QueueFrom(app) // domain services keep it; inserts work once the module has started

riverx.AddWorker(app, workers.NewOrderCleanup(repo))
riverx.Periodic(app, riverx.PeriodicJob{
	Name:     "order_cleanup",
	Schedule: s.OrdersCleanup().Schedule, // business settings: changed in the admin panel,
	Enabled:  s.OrdersCleanup().Enabled,  // rescheduled without a restart
	Args:     func() river.JobArgs { return jobs.OrderCleanupArgs{} },
})
riverx.AtStart(app, func(ctx context.Context, q *riverx.Queue) error {
	_, err := q.Insert(ctx, jobs.BootstrapArgs{}, nil) // queued on every start
	return err
})
```

`Queue` has `Insert`, `InsertTx` (the job exists exactly when the domain change commits), `InsertAt` and `InsertMany`; `Client()` gives the River client for the rest. Periodic jobs run on the elected leader only, so several instances do not run them twice.

| Variable | Default | |
|---|---|---|
| `RIVER_QUEUES` | `default=10` | queues and their capacity: an environment parameter, staging and production differ in it |
| `RIVER_WORK` | `true` | `false` makes an instance insert only, for splitting API and worker instances |
| `RIVER_JOB_TIMEOUT` | `1m` | |
| `RIVER_COMPLETED_RETENTION`, `RIVER_CANCELLED_RETENTION`, `RIVER_DISCARDED_RETENTION` | `24h`, `24h`, `168h` | |

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

One binary, split by `APP_ROLE`, the way taply runs API and worker instances:

| `APP_ROLE` | What runs |
|---|---|
| `all` (default) | everything |
| `api` | the API and the admin panel; River only inserts jobs |
| `worker` | River works jobs and runs periodic jobs; the API and the admin panel are not served |

Every role runs the same `wireDomain`: services, pages and workers are registered everywhere, and each module decides what to start. `/health` and `/metrics` are served in every role.

Other platform variables, read when the code does not set them: `OPS_ADDR` (`:9090`), `SHUTDOWN_TIMEOUT` (`20s`), `LOG_LEVEL` (`info`), `LOG_FORMAT` (`json` or `text`). A bad value stops the start, listed together with every other bad variable.

## Dependencies between modules

`river` requires `postgres`, `admin` requires `settings` for the settings pages, `settings` requires `postgres`. `platformgo plan` reports missing modules and offers to add them.

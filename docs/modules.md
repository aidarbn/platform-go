# Module catalogue

A module is declared as a section in `platformgo.yaml` and applied with `platformgo apply`. Module code lives in `kit/<module>` and is imported; a project only receives generated files (wiring, compose, generator configuration) and owned files it keeps.

| Module | What it gives the project |
|---|---|
| **core** (always) | settings from the environment, slog, alerts from log lines, `/metrics`, `/health`, graceful shutdown, tracing, profiling, Makefile, Dockerfile, linters, CI |
| **postgres** | pgx pool, goose migrations, application migrations, database in docker-compose, query generation (sqlc and jet) |
| **river** | queues, periodic jobs, workers, riverui, test helpers |
| **api** | gRPC server with an interceptor chain, REST gateway, multipart, OpenAPI 3.1, documentation page |
| **admin** | admin shell: login, roles, audit log; pages for settings, jobs and state; a place for project pages |
| **settings** | business settings: the `settings.yaml` schema, values in the database, cache, change notifications, typed access from code |
| **s3** | object storage (MinIO), file uploads, service in docker-compose |
| **rbac** | access to gRPC methods by role: taply's casbin model and policy format by default, any casbin model and adapter as an option |
| **i18n** | translations, locale in context, interceptors, dictionary migration |
| **enums** | enum catalogue from markers in the domain |
| **web** | server rendered pages next to the API: templ components, htmx fragments, CSRF, sealed cookies, hashed static assets, strict CSP |
| **monitoring** | a self-contained observability stack: OpenTelemetry collector, Prometheus, Alertmanager, Loki, Tempo, Grafana with a dashboard and alerts for the enabled modules |

## The `admin` module

It runs together with the application, inside the same binary, on its own port.

```yaml
modules:
  postgres: {}   # required: accounts, sessions and the log live in the database
  settings: {}   # optional: adds the business settings pages
  admin: {}
```

Everything else is an environment parameter: `ADMIN_ADDR` (`127.0.0.1:8081` by default, so the panel is not on the public network), `ADMIN_SESSION_TTL`, `ADMIN_INSECURE_COOKIES` for local development over plain HTTP, and `ADMIN_BOOTSTRAP_EMAIL` with `ADMIN_BOOTSTRAP_PASSWORD`, which create the first account while there are none.

Out of the box: sign in with a password, roles, accounts, an append-only audit log, business settings pages built from the schema, and an overview. The panel is server rendered HTML with no external assets — it opens on a locked down network — and it creates no files in the project. The menu is a sidebar that turns into a bar on top on a phone, and the panel follows the light or dark theme of the system unless a project theme pins it (below).

Security of the panel: passwords are PBKDF2-HMAC-SHA256, only the hash of a session token is stored, forms are protected by a double submit token, the cookie is `HttpOnly` and `SameSite=Strict`, and disabling an account revokes its sessions at once.

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

**Project pages** are registered with `admin.AddPage` and drawn inside the panel: `admin.Render` puts the body the project renders — `html/template`, templ, anything that writes HTML — under the menu, the account and the `?ok=` / `?error=` banners. Forms carry `admin.CSRFField` with `admin.CSRFToken(r)`; `admin.Record(r, action, target, details)` writes to the audit log; `admin.UserFrom(ctx)` tells who is acting. A path ending in `/` takes the whole subtree, so a list and its detail pages are one page. Plain tables, forms and buttons are styled by the panel; the helper classes are `row` (a line of controls), `muted`, `banner` and `banner error`, `secret` (a block of code or a payload, scrolls when long), and `cards` with `card` for figures.

```go
admin.AddPage(app, admin.Page{Title: "Customers", Path: "/customers/", Roles: []string{"support"}, Handler: customers})

func (h *Customers) show(w http.ResponseWriter, r *http.Request) {
	admin.Render(w, r, "Customer", func(w io.Writer) error { return views.Customer(c).Render(r.Context(), w) })
}
```

`ADMIN_LANGUAGE` (`en` or `ru`) sets the language of the panel itself.

**A theme** gives the panel the look of the project. It is code, not an environment parameter: `admin.SetTheme` in `wireDomain`, next to `AddPage`.

```go
admin.SetTheme(app, admin.Theme{
	Brand: "dămdala", Caption: "админка", BrandColor: "#F7931E",
	Menu: "#343A4D", MenuText: "#FFFFFF", MenuMuted: "rgba(255, 255, 255, .6)", MenuLine: "rgba(255, 255, 255, .12)",
	Active: "rgba(247, 147, 30, .18)", ActiveText: "#FFB566",
	Button: "#E84E1B", ButtonHover: "#C9400F", ButtonText: "#FFFFFF",
	Background: "#F4F4F4", Radius: "14px", ControlRadius: "10px",
	PagesFirst: true,
	CSS: viewsCSS, // the project's own classes for its pages
})
```

| Field | What it changes |
|---|---|
| `Brand`, `Caption` | the name in the menu and on the sign in card instead of the service name, and a small word next to it; the title of the tab keeps the service name |
| `Background`, `Surface`, `Text`, `Muted`, `Line`, `Accent`, `AccentHover` | the content: the page, cards and tables, text, borders, links and focus |
| `Menu`, `MenuText`, `MenuMuted`, `MenuHover`, `MenuLine`, `Active`, `ActiveText`, `BrandColor` | the menu: its background and items, the account, the current item, the name |
| `Button`, `ButtonHover`, `ButtonText` | the primary buttons; buttons inside tables stay quiet |
| `Radius`, `ControlRadius` | cards, tables and blocks; buttons, inputs and menu items |
| `PagesFirst` | the project pages at the top of the menu, the overview, settings, accounts and audit under a separator |
| `CSS` | rules for the project pages, added after the panel's own |

Every empty field keeps the panel's value, and without a theme the pages are byte for byte what they were. A theme with any colour pins the light scheme. The rules go into the stylesheet the page already has — no extra request, no `style` attributes — and a value that could end its declaration (`;`, `{`, `<`) panics on start, like a bad page path.

## The `monitoring` module

A monitoring stack of the service's own, for a project that has no shared monitoring to join. The module has no Go code: it generates `monitoring/` and leaves the service as it is, because the platform already exposes everything — `/metrics` on the ops port, traces over OTLP, JSON logs with `trace_id`.

```yaml
modules:
  monitoring: {}
```

What runs, from `monitoring/docker-compose.yml`:

| Service | Role |
|---|---|
| `otel-collector` | the agent next to the service: scrapes `/metrics`, receives traces over OTLP on `127.0.0.1:4317`, reads the logs of the Docker containers |
| `prometheus` | metrics (through remote write from the collector) and alert rules |
| `alertmanager` | groups firing alerts; notifications are not configured yet, the alerts are seen in Grafana and Prometheus |
| `loki`, `tempo` | logs and traces; a log line links to its trace and a trace to its logs |
| `grafana` | `127.0.0.1:3000`, data sources and the service dashboard provisioned |

```sh
cp monitoring/.env.example monitoring/.env    # Grafana password
make monitoring-up
# the service sends traces to the collector:
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317 make run
```

The collector finds the service at `APP_METRICS_TARGET`. A service on the host is reached through `host.docker.internal:9090` (the ops port must listen on an address the Docker bridge reaches, `OPS_ADDR=:9090` behind a firewall); a service in a container joins the external network `<service>-monitoring` and is reached by name, `app:9090`.

The storage can live elsewhere: run only `otel-collector` next to the service and point `PROMETHEUS_REMOTE_WRITE_ENDPOINT`, `LOKI_OTLP_ENDPOINT` and `TEMPO_OTLP_ENDPOINT` at the stack on the other server.

**Alerts** follow the enabled modules: `ServiceDown` always; with `api` the 5xx share, p99 of the service and of every route for HTTP and gRPC (the `go-http` and `go-grpc` rules of taply's monitoring) and recovered panics; with `postgres` pool exhaustion; with `river` failing jobs; with `settings` failing reloads. The **dashboard** has the same sections.

**Thresholds** live in `monitoring.yml`, created once and owned by the project, in taply's format — the same keys taply's monitoring agent reads, so the numbers mean the same:

```yaml
go-http:
  overall_latency: 3        # p99 of the service, seconds
  latency_by_endpoint: 5    # p99 of any route
  5xx_rate: 1               # percent of 5xx answers
  endpoint_latency_overrides:
    /v1/reports: 20         # a route slow by design gets its own rule
go-grpc:
  overall_latency: 3
  latency_by_endpoint: 5
  5xx_rate: 1               # percent of Internal, Unavailable, Unknown, DataLoss
  endpoint_latency_overrides: {}
```

An unknown section or key fails generation. After editing run `make generate`.

**Alerts of the project's own metrics** go into `monitoring.rules.yml`, created once and owned by the project, in Prometheus rule format. Its groups are appended to the generated `monitoring/alerts.yml`; a group may not take the name of the service, which the platform group uses.

```yaml
groups:
  - name: shop-orders
    rules:
      - alert: OrdersStuck
        expr: increase(orders_stuck_total[15m]) > 0
        labels: { severity: warning }
        annotations: { summary: "orders are stuck" }
```

## The `postgres` module

A PostgreSQL pool with a health check and pool metrics, `numeric` as `decimal.Decimal`, connection hooks for the types of the project, the local database in `docker-compose.yml`, and migrations.

Migrations are goose SQL files in `db/migrations`:

```
$ platformgo migrate create create_orders
created db/migrations/20260914073005_create_orders.sql
```

The directory is embedded into the binary by the generated `db/migrations/migrations.gen.go`, and the module applies pending migrations during startup, before any other module touches the database. A session advisory lock makes several instances starting at once apply each migration exactly once. Set `DATABASE_MIGRATE=false` when migrations run as a separate deploy step.

### The local database

`docker-compose.yml` gets a `postgres` service — `postgres:18-alpine`, user, password and database `app`, on `127.0.0.1:5432`, data in the `postgres-data` volume, a `pg_isready` health check — and `.env.example` its address in `DATABASE_URL`. A project with another local database says so in the `compose` option; every key is optional and the defaults are that service, byte for byte:

```yaml
modules:
  postgres:
    compose:
      image: postgres:16-alpine          # default postgres:18-alpine
      db: be-maas-api                    # default app
      user: app                          # default app
      password: app                      # default app
      port: 5544                         # on 127.0.0.1; default 5432
      data_dir: /var/lib/postgresql/data # where postgres-data goes; default /var/lib/postgresql
      command: [-c, max_connections=300] # arguments of the server; default none
      init: deploy/postgres/keycloak.sql # into /docker-entrypoint-initdb.d; default none
      enabled: true                      # false: no service, for one of the project's own
```

- `DATABASE_URL` in `.env.example` follows `user`, `password`, `port` and `db`, escaped as a URL; `platformgo db generate` reads it from there when nothing else gives an address.
- `data_dir` is where the image keeps its data: `/var/lib/postgresql` from postgres 18, `/var/lib/postgresql/data` before it. With the wrong one the data lands in an anonymous volume and is lost when the container is recreated.
- `init` is a folder or a `.sql`, `.sql.gz`, `.sql.xz`, `.sql.zst` or `.sh` file, relative to the project root and inside it, mounted read only: a file into `/docker-entrypoint-initdb.d/<its name>`, a folder as `/docker-entrypoint-initdb.d`. The image runs the scripts once, on an empty database: after a change, `docker compose down -v` to see it.
- `enabled: false` leaves the service and its volume out, for a database service of the project's own in `docker-compose.override.yml`; `DATABASE_URL` still follows the other keys.
- Values are quoted where YAML, the shell of the health check or the `$` interpolation of compose would read them differently; the port is 1 to 65535.

### Pool, types and connection hooks

The pool is configured by environment variables:

| Variable | Default | What it is |
|---|---|---|
| `DATABASE_URL` | required | database address |
| `DATABASE_MAX_CONNS` | 10 | connection limit |
| `DATABASE_MIN_CONNS` | 0 | connections kept open |
| `DATABASE_MAX_CONN_LIFETIME` | 1h | a connection is replaced after this time |
| `DATABASE_MAX_CONN_IDLE_TIME` | 30m | an idle connection is closed after this time |
| `DATABASE_CONNECT_TIMEOUT` | 5s | timeout of a new connection and of the start check |
| `DATABASE_MIGRATE` | true | apply migrations on start |

Every connection gets the types of the platform first: `numeric` is read and written as `decimal.Decimal` (`github.com/shopspring/decimal`, the type `db/sqlc.yaml` maps it to) with the binary codec of `pgx-shopspring-decimal`, as in taply. Money and percentages never pass through `float64`.

The module is built by the generated `modules.gen.go`, so a project passes its own options in `main.go` with `postgres.Apply`, before `platform.Run`:

```go
modules := platformModules(cfg)
if err := postgres.Apply(modules,
	// sizes of the application: they replace the defaults of the platform,
	// while a DATABASE_* variable that is set still wins
	postgres.WithDefaults(postgres.PoolSizes{MaxConns: 25, MinConns: 5, MaxConnIdleTime: 10 * time.Minute}),
	// runs on every new connection, after the platform types: enums, composite types
	postgres.WithAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
		t, err := conn.LoadType(ctx, "order_status")
		if err != nil {
			return err
		}
		conn.TypeMap().RegisterType(t)
		return nil
	}),
	// anything else of pgxpool.Config: a tracer, BeforeAcquire, runtime parameters
	postgres.WithPoolConfig(func(c *pgxpool.Config) {
		c.ConnConfig.RuntimeParams["application_name"] = serviceName
	}),
); err != nil {
	log.Fatal(err)
}
if err := platform.Run(platform.Config{Service: serviceName}, modules, wireDomain); err != nil {
	log.Fatal(err)
}
```

- For each pool size the first of these wins: the `DATABASE_*` variable, if it is set (even to `0`), or a value the project put into `cfg.Postgres` after `LoadConfig`; the value of `WithDefaults`; the default of the platform. A zero field of `PoolSizes` keeps the platform default.
- Hooks of `WithAfterConnect` and functions of `WithPoolConfig` run in the order they were added. A failing hook discards the connection; on start that fails the database check.
- `Apply` fails when the modules have no `postgres` module, so hooks the project relies on are never dropped silently.
- Without the module, `pgdb.Open` takes the same through `pgdb.Config`: the sizes, and `Hooks` with `AfterConnect` and `ConfigurePool`. `pgdb.RegisterTypes` registers the platform types on a connection opened by other means.

Queries come in two kinds, as in taply:

| Kind | Tool | Where | How to regenerate |
|---|---|---|---|
| static — the query text is known in advance | sqlc | `db/queries/*.sql` → `internal/db/sqlcgen` | `make generate`; no database needed |
| dynamic — filters, sorting, pagination built at run time | jet | the migrated schema → `internal/db/jetgen/{model,table}` (`jet.out`) | `make db-generate`: migrates the local database and reads its schema |

Both generators are pinned as `tool` in `go.mod` by `platformgo apply` (sqlc v1.31.1, jet v2.16.0) and removed with the module. `db/sqlc.yaml` is generated: schema from the migrations, `pgx/v5`, `numeric` as `decimal.Decimal`, `uuid` as `uuid.UUID`, `jsonb` as `json.RawMessage`, `timestamptz` and `date` as `time.Time`, nullable columns as pointers. `verify` fails when the sqlc output is stale or left from a removed query file.

The layout and the type mapping are options of the module in `platformgo.yaml`; every key is optional and the defaults are the layout above:

```yaml
modules:
  postgres:
    sqlc:
      dir: db/sqlc                   # where sqlc.yaml is generated; default db
      queries: queries               # relative to dir; default queries
      out: internal/adapters/out/db/sqlcgen   # default internal/db/sqlcgen
      package: sqlcgen
      emit_interface: false          # default true
      overrides:                     # before the platform's; sqlc takes the first match
        - {db_type: int4, go_type: int64}
        - {db_type: int4, nullable: true, go_type: {type: int64, pointer: true}}
        - {column: orders.meta, go_type: {import: example.com/shop/internal/domain, type: Meta}}
      renames: {url: URL}
    jet:
      out: internal/adapters/out/db/jet   # default internal/db/jetgen
```

- The schema is always `db/migrations`: `migrations.gen.go` embeds it, so the generated `sqlc.yaml` points there from wherever `dir` is.
- An override takes `db_type` or `column`, `nullable`, and `go_type` as a name (`int64`) or an object (`import`, `package`, `type`, `pointer`, `slice`), as in sqlc. Overrides of the project come first, so one for `uuid` or `pg_catalog.numeric` replaces the platform's.
- Moving `dir` moves the generated file: `apply` writes the new one and deletes the old. `queries` is relative to `dir`, so move the query files along; until then `generate` finds no queries and removes the code generated from them. Moving `out` does not move code already generated: delete the old folder after `make generate`.
- `out` must not hold or sit in the queries, the migrations or the jet folder, nor hold `dir`: sqlc output is cleaned before every run.
- `jet.out` is where `make db-generate` writes the query builder; it must not hold or sit in the queries or the migrations, and sqlc's `out` must stay out of it: jet's old output is cleaned before every run. The builder is not a file `apply` writes, so moving the folder changes nothing in `platformgo.lock`; move or delete the old folder and run `make db-generate`.

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

jet writes its output under the database name; platformgo moves the packages straight under `internal/db/jetgen` (or `jet.out`), so import paths are the same for every developer.

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

`make generate` runs buf with the plugins pinned in `go.mod` by `apply` (buf, protoc-gen-go, protoc-gen-go-grpc, protoc-gen-grpc-gateway, and the platform's `protoc-gen-platform-openapi`): Go code into `internal/api/gen`, an OpenAPI 3.1 description of the REST API into `api/openapi`, see [The platform's OpenAPI generator](#the-platforms-openapi-generator). The human part of that description — title, intro, tags, security scheme — lives in `api/openapi/base.yaml`, created once on `apply` and owned by the project from then on: only the project knows what to tell the reader of its API, and the proto files cannot say it. Operation ids are `<Service>_<Method>`. `verify` generates into a scratch directory and fails when the committed code is stale.

`buf.yaml` and `buf.gen.yaml` are generated. Options of the module in `platformgo.yaml` adjust them; every key is optional and the defaults give the files above:

```yaml
modules:
  api:
    proto_dir: api                   # the buf module; default proto
    buf:
      deps: [buf.build/grpc-ecosystem/grpc-gateway]   # added to googleapis and protovalidate
      breaking_except: [FILE_NO_DELETE, RPC_NO_DELETE, MESSAGE_NO_DELETE, FIELD_NO_DELETE]
    openapi:
      generator: platform            # platform | connect | none
```

- `proto_dir` is the buf module: imports are relative to it, `make generate` and `lint proto` read it, and the i18n option of the platform is generated under it (`<proto_dir>/platform/i18n/v1/i18n.proto`). Moving it leaves the old copy of that file, which has no generated mark: `apply` lists it as kept, delete it.
- `buf.deps` are added after the platform's; a platform dependency listed again is left out, with a label of its own it is an error. A label (`buf.build/grpc-ecosystem/grpc-gateway:v2.27.0`) is written as it is; `buf.lock` pins the commit, so after changing a label run `go tool buf dep update`. The Go code of a dependency must come from a module in `go.mod`, so its `go_package` is left out of managed mode, as for the platform's.
- `proto_dir` holds only proto files for buf; it must not be inside `internal/api/gen` or `api/openapi`, where buf writes.
- `buf.breaking_except` goes into `breaking.except` next to `use: [FILE]`; `lint --against` uses it.
- `openapi.generator: platform`, the default since v0.10.0, writes the description with the platform's own plugin, `protoc-gen-platform-openapi`, pinned in `go.mod` by `apply`; it is a package of the platform module, so its version is the platform's. See [The platform's OpenAPI generator](#the-platforms-openapi-generator).
- `openapi.generator: connect` keeps the description of the versions before v0.10.0: protoc-gen-connect-openapi writes one `api/openapi/openapi.yaml` merged into `base.yaml`, gnostic annotations give sections and titles, and `buf.yaml` has the gnostic dependency. It stays for projects that are not ready to move; new features of the description come to `platform` only.
- `openapi.documents` cuts descriptions for one audience each out of the whole one, `generator: platform` only; see [Documents for audiences](#documents-for-audiences).
- `openapi.generator: none` takes the OpenAPI plugin out of `buf.gen.yaml`. The project then keeps `api/openapi/openapi.yaml` itself; the module serves it as before, `generate` never removes it and `verify` does not compare it. `api/openapi/base.yaml` is still created: with `none` nothing reads it, and it keeps the embed of `api/openapi` compiling before the project adds its description.

### The platform's OpenAPI generator

The platform's generator describes the REST API the way the gateway of the module serves it, and a test of the platform starts the gateway and checks its real answers against the description, so the two cannot drift apart:

- paths and methods from `google.api.http` with `additional_bindings`, `body: "*"`, `body: "<field>"` and `response_body`; path variables, `{name=shelves/*}` as `{name}` with a pattern; every field that is neither in the path nor in the body is a query parameter, nested ones as `a.b`, a map of scalars as `m[key]=value`; lists and maps of messages have no query form. Two bindings of one method that differ only in the segments a variable matches are one operation whose pattern takes both. Methods without `google.api.http` are not served over REST and are left out; a stream is one JSON object a line;
- JSON as the gateway writes it: proto field names, enums by name, 64-bit integers as strings (a request takes a number too), bytes in base64, maps as objects, well-known types in their JSON form (Timestamp, Duration, FieldMask, Struct, Value, ListValue, Any, Empty, the wrappers), `google.api.HttpBody` as the raw body;
- a response and a request differ. In a response every field is present and required, since the gateway writes unpopulated fields, except members of a `oneof` and proto3 `optional` fields, which it writes only when set; a message field, a wrapper or a proto2 field is `null` when unset; no other field may appear. In a request a field is required only when protovalidate says so, and translatable fields of the i18n module never are. A message used on both sides has a schema per side, the request one named `<message>Input`;
- protovalidate rules as JSON Schema keywords — lengths, patterns, prefixes, bounds, `in` and `not_in` (a 64-bit integer in both its forms), formats such as `uuid`, `email`, `uri`, item and pair counts, unique items, examples — and the rest, CEL among it, as a list under the description; `ignore: IGNORE_IF_ZERO_VALUE` lets the zero value through; a `oneof` as at most one member set, exactly one when it is required;
- a request with a field of a file message — one with `content` bytes and a `filename` or `content_type` — takes `multipart/form-data` as well as JSON: a file part per file field, several parts or `images[0]`, `images[2]` for a list, JSON parts for message fields, well-known types too (a time is a quoted string), text parts for the rest, as the decoder of the module accepts them;
- a body the HTTP rule takes (`body: "*"`, `body: "<field>"`) is `required: true`. The gateway reads an empty body as `{}` and validates that, so this asks only for what a client can always send; a `body: "*"` whose fields all sit in the path, or that has none (`google.protobuf.Empty`), has nothing to send and is optional;
- one error body, `Error` (`google.rpc.Status` with the protovalidate violations and `google.rpc.ErrorInfo` among the details), as the `default` answer of every operation, with the HTTP statuses the gateway answers for the gRPC codes. It is the body the module's default error handler writes — `code`, `message`, `details`, no request id, which goes in the `X-Request-ID` header — and the contract test checks real answers against it. A project with `api.HTTPErrorHandler` writes another body: it describes it in `base.yaml` as `components.schemas.Error`, and the statuses as `components.responses.Error`; the generator then keeps those, leaves out the detail schemas only its own body names, and every operation's `default` points at them (in the split layout they stay in the root, `openapi.yaml`);
- documentation from the leading comments of services, methods, messages, fields and enum values, the trailing one when there is none; a short first paragraph of a method is its summary. `deprecated` options become `deprecated: true`. A tag per service, named by the short name unless two services share it.

`base.yaml` keeps what it says: info, servers, security, its tags and their descriptions, its own components; the generator adds tags it found, the paths and the schemas, and a schema of the same name replaces the one in the base — except the error body: an `Error` schema or response in the base wins, see above. The output is deterministic, so `verify` compares bytes.

What the proto files cannot say goes into the options of the platform, `platform/openapi/v1/options.proto`, which `apply` writes under the proto folder with `generator: platform` (it is imported, not generated; its Go code is `kit/openapipb`). Back on another generator `apply` keeps the file, as it keeps the i18n option, and says so: delete it once no proto file imports it.

```proto
import "platform/openapi/v1/options.proto";

service MenuService {
  option (platform.openapi.v1.service) = {tag: "Menu", tag_group: "Public API"};

  rpc GetMenu(GetMenuRequest) returns (Menu) {
    option (platform.openapi.v1.method) = {summary: "The menu of a branch"};
    option (google.api.http) = {get: "/v1/menus/{id}"};
  }
  rpc Debug(GetMenuRequest) returns (Menu) {
    option (platform.openapi.v1.method) = {hidden: true};
  }
}

message Menu {
  string id = 1 [(platform.openapi.v1.field) = {example: "\"m-1\""}];
}
```

- `tag` of a service or a method is its section; the service name by default. The description of a section is the comment of its service or the `tags` of `base.yaml`, which win.
- `tag_group` puts a section into a group of the sidebar, `x-tagGroups` (Scalar and Redoc). A section without a group then gets a group of its own name, since both hide a section that is in no group. Groups in `base.yaml` replace the generated ones.
- `summary` replaces the first paragraph of the comment as the title of a method; `hidden` leaves a method or a whole service out.
- `example` of a field or a message is JSON: `"\"Latte\""` for a string, `3`, `{"id": "m-1"}`.

The generator reads neither gnostic nor openapiv2 annotations.

#### Moving from protoc-gen-openapiv2

A project that described its API with the `openapiv2_*` options of grpc-gateway, as taply does, converts them once:

```
platformgo openapi convert [--dir .] [--proto api/proto] [--base api/openapi/base.yaml] [--dry-run]
```

- `openapiv2_field`: the description becomes the leading comment of the field (a different comment it had stays, the description becomes its last paragraph), the example `(platform.openapi.v1.field)`, a `type` the generator gives anyway (`INTEGER` on an integer, `STRING` on a string…) goes;
- `openapiv2_operation`: summary and description become the comment of the method — the summary also `(platform.openapi.v1.method).summary` when the comment would not lead with it —, the first tag `tag`, `x-internal: true` `hidden`; `consumes`/`produces` of `multipart/form-data` and `x-multipart-request` go, since the generator finds file fields itself (the report names a method that says multipart without a file field);
- `openapiv2_schema` and `openapiv2_tag` likewise for messages and services;
- `openapiv2_swagger`: info, security schemes and requirements, tags and `x-tagGroups` go into `base.yaml` (next to it as `base.openapiv2.yaml` when the project has one), an API key in the `Authorization` header as the bearer scheme of HTTP;
- the import of the openapiv2 options goes from a file that has none left, the import of the platform's options comes where they are used.

Anything else — a `pattern` or a `title` of a field, a second tag, an `operation_id`, `host`, the info, tags, security or `x-tagGroups` of a later file that differ from an earlier one, a declaration that shares its line with another — stays as it was or is reported for a person, with the file and the line; nothing is dropped silently. The converted files are compiled before anything is written; `--dry-run` reports only. On taply's 29 annotated files it moves 1016 field options (989 descriptions into new comments, 26 added to comments the fields had, 454 examples, 146 redundant types), 125 operations (125 tags, one `x-internal` as hidden) and three `openapiv2_swagger`, leaving three places for a person: deleting `openapi_options.proto`, which declares nothing else, and two files whose `Bearer` scheme is described differently from the central one. The description it then gets keeps every field description and example of the fields both descriptions share and every operation summary.

The description is split the way the proto files are, so a change to a method shows up in the yaml of its proto file and review stays readable:

```
api/openapi/
  base.yaml                    # the project's: info, intro, tags, security
  openapi.yaml                 # generated root: base, tags and a $ref to every path
  shop/v1/shop.yaml            # one per proto file (proto/shop/v1/shop.proto): its paths and schemas
  common.yaml                  # the error body and the messages of dependencies such as googleapis
  openapi.bundle.yaml          # everything in one file, no external $ref
  openapi.gen.go               # embeds the folder
```

References between files are relative (`../../common.yaml#/components/responses/Error`); a path whose methods come from several proto files is written into the root itself. The service serves the bundle at `/openapi.yaml`, so `/docs` and clients keep their URL; the split files are for people and for review, and clients are generated from the bundle. `generate` removes every file with the generator's mark before buf writes the current ones, so the yaml of a deleted proto file goes with it, and `verify` compares all of them; back on `connect`, the marked files are left over and removed the same way, so a stale bundle is never served. With `none` the folder is the project's and nothing in it is removed: a project that keeps the platform's files as its own deletes their first line, the mark. A proto file whose yaml would land on one of the files of the description, such as `common.proto` at the root of the proto folder, fails generation.

#### Documents for audiences

One API often has several readers: the tablet in a branch, the manager of a restaurant, the superadmin, the public. Each of them needs only its part of the API, and a public document must not show the admin endpoints. `api.openapi.documents` in `platformgo.yaml` describes such documents; the whole description stays as it is:

```yaml
modules:
  api:
    openapi:
      documents:
        - name: tablet                       # openapi.tablet.bundle.yaml, /openapi/tablet.yaml, /docs/tablet
          title: Taply — the tablet API      # info.title; the base's when empty
          description: What the tablet calls. # info.description; the base's when empty
          include:
            tag_groups: [API Планшета]       # groups of x-tagGroups, base.yaml's included
        - name: superadmin
          include:
            tag_groups: [API Суперадмина]
            services: [proto.superadmin.v1.SuperAdminService]   # gRPC services by full name
        - name: partner
          exclude:                           # without include: everything but what is excluded
            tags: [Суперадмин]
```

- An operation goes into a document when it matches `include` — its tag is listed in `tags`, its tag is in a group listed in `tag_groups`, or its method belongs to a service listed in `services` — and does not match `exclude`, which is matched the same way and wins. Without `include` everything goes in. Groups are those of the description, so groups written in `base.yaml` count, and a section that is in no group (Scalar and Redoc hide it in the sidebar) is reached by its tag or its service.
- A group, a tag or a service the description does not have fails generation, as does a document that selects no operation: a typo would otherwise give an empty or a wrong document. A name is lower case letters, digits, `-` and `_`; `all` is the whole description.
- The generator writes `api/openapi/openapi.<name>.bundle.yaml`, a bundle like `openapi.bundle.yaml`: the base with the document's title and description, the selected operations, the paths, tags and groups that still have one, and only the components they use — schemas, responses and the rest by reference (a discriminator's mapping by bare name too), security schemes by the security requirements; `x-` sections of the components stay. A tag of `base.yaml` with no operation, an introduction, goes from every document; a path item of `base.yaml` without operations stays. `services` selects the operations generated from the proto files; an operation `base.yaml` writes itself is selected by its tag or group. `common.yaml` and the files per proto file stay shared and are not repeated per document.
- `apply` adds `documents=platformgo.yaml` to the options of the plugin in `buf.gen.yaml`, and the plugin reads the documents from `platformgo.yaml` itself, so a changed document needs `make generate` only. Without documents `buf.gen.yaml` and the description are the same as without the option.
- The bundles carry the generator's mark: `generate` removes the bundle of a document that is gone, and `verify` compares every one of them.

The module serves every document at `/openapi/<name>.yaml` with its own page at `/docs/<name>`; the whole description stays at `/openapi.yaml` and `/docs`. `API_DOCS=false` serves none of them. `API_DOCS_DOCUMENTS` lists the ones to serve, `all` for the whole one: `API_DOCS_DOCUMENTS=public` on production serves the public document alone, and `all,public` both; a name the description does not have is logged at start. Without `API_DOCS_DOCUMENTS` every document is served, as the whole description already is, with everything the documents hold: on a stand that must not show the admin endpoints, set it. Who may read them is up to the project:

```go
api.DocsAccess(app, func(doc string, next http.Handler) http.Handler {
	if doc == "public" {
		return next
	}
	return staffOnly(next) // a password, a token, a role: the project's own check
})
```

`DocsAccess` gets the document name (`api.AllDocuments` for the whole one) and the handler of its description or its page, and returns the handler to serve. The descriptions are plain HTTP routes, not gRPC calls, so the interceptors of `rbac` do not see them; a check by role reads the token in the handler it returns. The page in the browser loads its description without an `Authorization` header, so a guarded page works with what a browser sends by itself — a cookie or basic auth.

The handler is registered from `wire.go`:

```go
api.Register(app, api.Service{
	GRPC:    func(s *grpc.Server) { shopv1.RegisterOrdersServiceServer(s, orders.NewHandler(repo)) },
	Gateway: shopv1.RegisterOrdersServiceHandler,
})
api.AddUnaryInterceptor(app, auth.Interceptor(tokens))           // authentication and the like
api.HandleHTTP(app, "POST /webhooks/kaspi", kaspi.WebhookHandler) // routes that are not gRPC
```

What the module does for every call, REST included — the gateway calls the local gRPC server, so both go through one chain: panic recovery, metrics (taply's `grpc_server_handled_total`, `grpc_server_handling_seconds` and `grpc_req_panics_recovered_total`, which the go-grpc dashboard and alerts of the monitoring agent read), logging of failed calls, errors without internal details (a plain error becomes `Internal`, a status keeps its code), project interceptors, protovalidate validation with the violations as details. The REST side speaks snake_case JSON as in the proto files, returns every field, ignores unknown request fields and forwards request headers as gRPC metadata. Also: gRPC health, optional reflection, CORS, `/openapi.yaml` and `/docs`, graceful stop.

**The interceptor chain** is ordered by phase, as taply orders its interceptors; the order is the application's, not the order the modules start in. From the outside in:

1. `PhaseRecover` — the outer recovery: a panic in any interceptor answers `Internal`;
2. `PhaseObserve` — the client address, metrics, logging, errors without details;
3. `PhaseLimit` — the rate limit by address, deprecated methods;
4. `PhaseErrorsI18n` — the translation of error messages (`i18n`), outside authentication so its refusals are translated too;
5. `PhaseAuthenticate` — the project's authentication; `api.AddUnaryInterceptor` and `api.AddStreamInterceptor` add here;
6. `PhaseAuthorize` — the access check (`rbac`, `api.Authorize`);
7. `PhaseGuard` — checks that need the caller, such as a tenant guard; the rate limit per principal;
8. `PhaseTransform` — changes to the request and the response with the caller known: the translation of response fields and the base fields of editor requests taken from their translations (`i18n`), then, at `PhaseTransform+50`, the project's normalisation of requests (`api.Normalize`);
9. `PhaseValidate` — protovalidate;
10. `PhaseAfterValidate` — what must see a valid request: the copy of base fields into the default translation (`i18n`);
11. `PhaseIdempotency` — idempotency;
12. `PhaseHandlerRecover` — the inner recovery: a panic of the handler becomes `Internal` before idempotency, so the key is not left in progress and the metrics see the `Internal` the client gets.

`api.AddUnaryInterceptorAt(app, api.PhaseGuard, "restaurant", guard)` and `api.AddStreamInterceptorAt` place a project interceptor in a phase; the phases are a hundred apart, so `api.PhaseGuard+10` runs after every guard. Within a phase the platform's interceptors come first and the project's keep the order they were added in. `api.Chain(app)` and `api.StreamChain(app)` list the chain as `phase/name` once the module has started — a project test can pin it — and the start logs it. Streams have the same phases without the rate limit, normalisation, validation and idempotency.

`api.Normalize(app, func(proto.Message))` changes every request message after the guards and before validation — trimming strings, Unicode normalisation, as taply's `strutil` does — so a normalised value passes the same checks as one sent clean. The functions run in the order they were added.

The HTTP side, following taply's gateway:

- **File uploads**: a `multipart/form-data` request fills the request message. A part named after a field of a message with `filename`, `content_type` and `content` becomes a file; a repeated field takes several parts in order (`images`, `images`) or by index (`images[0]`, `images[3]`, with empty entries for skipped ones); a JSON part fills a message field; other parts set scalar fields.
- **Request id and client address**: `X-Request-ID` is taken from the request or generated, returned in the response and passed to gRPC handlers — `api.RequestID(ctx)`. The client address — `api.ClientIP(ctx)` — is the connection address, or, when the connection comes from `API_TRUSTED_PROXIES`, the last `X-Forwarded-For` hop: the one the proxy itself saw, read from the last header line, since HAProxy adds a line of its own. Only that hop is taken, never one further left: the trusted proxies default to every private network, where a client may sit too. The gateway hands the address to the gRPC server under its own metadata key, `api.ClientIPMetadataKey` (`x-platform-client-ip`), and drops an incoming header of that name, so a client cannot choose it. The old `X-Client-IP` header is overwritten with the resolved address, so plain routes of `api.HandleHTTP` can still read it, and the gateway passes the address under the old `x-client-ip` metadata key as well, for handlers that read it themselves. The gRPC server believes the key only from loopback — where the gateway of the process connects from —, from its own listen address, or from networks set in `API_TRUSTED_PROXIES`, so a gRPC proxy listed there may set it; the default of every private network does not count here, so a gRPC port open to the private network does not let its hosts name themselves. A trusted peer that sends only the old `x-client-ip` key — a proxy or a client written for an older release — is still believed, with one warning in the log. Both keys of the incoming metadata are rewritten to the checked address before any interceptor of the project, so a handler that reads them itself never sees what an untrusted peer sent. A proxy on a trusted address that passes the gRPC metadata of its clients through, such as nginx with `grpc_pass`, must drop or set both keys itself. A direct gRPC client anywhere else is its peer address whatever metadata it sends. `x-forwarded-for` in gRPC metadata is not an address to trust: grpc-gateway appends the HTTP peer to whatever the client sent.
- **Limits and headers**: a body over `API_MAX_RECV_MB` answers 413 — `api.MaxRecvSize(app, bytes)` in wireDomain sets the limit from code instead, for a project that keeps it in its own settings; taply's security headers (`nosniff`, `DENY`, `no-store`, HSTS).
- **CORS** — off until `API_CORS_ORIGINS` lists origins: exact ones, `https://*.example.com` for any subdomain, `http://localhost:*` for any port, `*` for any. An allowed origin gets `Access-Control-Allow-Origin` on every response and `X-Request-ID` in `Access-Control-Expose-Headers`; a preflight answers 204 with the methods, the headers and `Access-Control-Max-Age`, while a foreign origin goes on to the API with no CORS headers. Every response but the literal `*` carries `Vary: Origin`, so a shared cache keeps the answers apart. A browser may send `Authorization`, `Content-Type`, `Accept`, `X-Request-ID` and `Idempotency-Key`; headers the API cannot work without are registered in code with `api.CORSHeaders(app, "X-Restaurant-Id")` in wireDomain, and a stand adds its own with `API_CORS_HEADERS`. The preflight answers this fixed list rather than echoing `Access-Control-Request-Headers`, so a header the browser refuses is one missing from it. By default the answer echoes the origin with `Access-Control-Allow-Credentials: true`, so the page may send cookies — with `*` too, which the module warns about on start. A public API, taply's mode, sets `API_CORS_ORIGINS=*` and `API_CORS_CREDENTIALS=false`: the answer is then a literal `*` without credentials. `API_CORS_CREDENTIALS=false` with listed origins echoes them without credentials. `API_CORS_PREFLIGHT_NO_CONTENT=true` (`Config.CORSPreflightNoContent`) answers every `OPTIONS` request the policy does not — a foreign origin, no origin, or no `API_CORS_ORIGINS` at all — with 204 and no CORS headers, as taply does, instead of passing it to the gateway, which answers 404 or 405 with a body; the browser refuses the call either way.
- **Metrics and access log**: taply's `http_gateway_requests_total{method,path,status}`, `http_gateway_request_duration_seconds`, `http_gateway_response_size_bytes` and `http_gateway_requests_in_flight`, which the go-http dashboard and alerts read, labelled by the route template — `/v1/orders/{id}`, never the concrete path — with unmatched paths as `unknown`; an access log line per request with the route, status, duration, client address and request id — the concrete path is logged only when no route matched, because a path segment can be a secret (a link whose token is the credential) and the log is shipped off the box, plus the bodies of failed requests with binary and multipart bodies summarised instead of dumped. Paths that take secrets keep their bodies out: `api.MaskLogBodies(app, "/certificate")` in code or `API_LOG_BODIES_MASK_PATHS` per stand log `[REDACTED]` for any path containing one of the parts, as taply does for certificate uploads.
- **Call metrics per tenant** — `API_METRICS_TENANT_HEADER=tenant-name` labels `grpc_server_*` with `tenant_name` from that request header (gRPC metadata or a REST header), `unknown` without it, as taply's do. Off by default, and the metrics keep their labels. Every distinct value is a new series of every call metric, and the client sends the header before it is authenticated, so the value is bounded: up to 64 letters, digits, `.`, `-` and `_`, anything else is counted as `invalid`, and past 256 distinct tenants in a process a new one is counted as `other`. Use it where the tenants are a short, known list, such as taply's restaurants behind its own proxy.
- **Trace exemplars** — with `METRICS_OPENMETRICS=true` a sampled call leaves its trace id as an exemplar (`traceID`, taply's label) of the call counters and the latency histogram, so a Grafana panel links a slow bucket to its trace. Prometheus stores them with `--enable-feature=exemplar-storage`.
- **Routing errors** name the method and the path: `GET /v1/nothing: route not found`.
- **Status codes** — the gateway maps gRPC codes to HTTP as grpc-gateway does: `InvalidArgument`, `FailedPrecondition` and `OutOfRange` 400, `Unauthenticated` 401, `PermissionDenied` 403, `NotFound` 404, `AlreadyExists` and `Aborted` 409, `ResourceExhausted` 429, `Canceled` 499, `Internal` and `Unknown` 500, `Unimplemented` 501, `Unavailable` 503, `DeadlineExceeded` 504. `401` is for the token itself — missing, malformed, expired, with a bad signature; a valid token that grants nothing here is `403` (see `rbac`). A request that fails validation answers `400` with the violations as details; an error without a status, from a handler that returned a plain `error`, answers `500` with `internal error`, and the error itself goes to the log.
- **Failures of external providers** — one rule on top of the mapping, taken from taply: an `Unavailable` that an external provider caused is a 502, not the 503 that nginx takes for a dead replica. A handler returns `api.ExternalUnavailable("kaspi", "payment service unavailable")` — `Unavailable` with a `google.rpc.ErrorInfo` of reason `EXTERNAL_UNAVAILABLE` and the provider in its metadata — and `api.ExternalTimeout` for a provider that did not answer in time, a 504 with reason `EXTERNAL_TIMEOUT`. Any `Unavailable` with that reason counts, whatever the domain of its `ErrorInfo`, so a project that builds its own keeps it; another code with the reason is mapped as usual. The message goes to the client as `provider: message`: a short fixed text, never the provider's answer, which belongs in the log. The i18n module leaves these messages untranslated unless [`i18n.WithExternalErrors`](#external-provider-messages) is on, and idempotency lets a repeat run again. With `api.HTTPErrorHandler` the 502 is the project's: `api.IsExternalUnavailable(err)` tells the failure.
- **Error contract** — an API with its own error body replaces the gateway's writer: `api.HTTPErrorHandler(app, handler)` receives every REST error — handler statuses, decoding, routing, and a body over `API_MAX_RECV_MB` as a `*runtime.HTTPStatusError` with 413 — and writes the status and the body the contract wants. A middleware that reads the body itself, such as a request signature check, asks `api.BodyTooLarge(r)`. Plain routes answer 413 on their own.

And on every call, following taply's interceptors:

- **Rate limit** — a token bucket per client address, 50 calls a second with a burst of 100, and a separate 30/60 for public methods (the ones `rbac` marks public, or `api.PublicMethods`). An IPv6 client is limited by its /64, the block one subscriber gets, so it cannot take a new bucket with every address of the block; `API_RATE_IPV6_PREFIX` changes the length, `128` limits every address on its own. A refused call answers `ResourceExhausted`, 429 over REST, and is counted in `api_rate_limited_total`. It runs before project interceptors, so a flood never reaches token validation. A second bucket limits each principal, 50/100 by default, once the project names it: `api.RateLimitKey(app, func(ctx) string)` returns the user, API client or device the authentication put into the context. It runs after the project interceptors, counts refusals in `api_principal_rate_limited_total`, and leaves a call with an empty key to the limit by address; the limit by address applies to every call. Streams are not limited.
- **Deprecated methods** — `option deprecated = true` in the proto file is enough: calls are counted in `api_deprecated_calls_total{method}` and logged with the caller's address and user agent, so the method can go once nobody calls it. taply keeps this list in code. `api.DeprecatedReplacements(app, map[string]string{"/shop.v1.Orders/GetOld": "/shop.v1.Orders/Get"})` in wireDomain adds taply's `replacement` label to the metric and the log line, and counts a listed method as deprecated even without the proto option; without the call the metric has only `method`.
- **Idempotency** — a call with an `Idempotency-Key` header is remembered in the database of the `postgres` module, with the semantics of taply after its fixes: a repeat gets the saved answer without running the handler again, the same key with a different payload answers `AlreadyExists`, a repeat while the first call runs answers `Aborted`. A key counts only for a caller `api.IdempotencyUser` names — without it, or for an anonymous caller, the header is ignored, since anonymous callers would share one space of keys — and only on the methods it is meant for: by default every method that changes something (its HTTP binding is not a GET and its `idempotency_level` is not `NO_SIDE_EFFECTS`); `api.IdempotencyMethods(app, methods...)` replaces that with a list, as taply keeps one. `api.RequireIdempotency(app, methods...)` makes a key mandatory, as taply does for every create method; a required method without a key answers `InvalidArgument` for anonymous callers too. Only final outcomes are remembered: a success and the client errors a repeat cannot fix (`InvalidArgument`, `NotFound`, `AlreadyExists`, `FailedPrecondition`, `PermissionDenied`, `Unauthenticated`, `OutOfRange`, `Unimplemented`), with the `google.rpc.Status` the client got, details included; anything else — `Internal`, `Unknown`, `Canceled`, `Unavailable`, `DeadlineExceeded`, `ResourceExhausted`, `Aborted`, a panic — lets the next repeat run. A `RetryableError` decides either way. A running call holds its key with a lease (`API_IDEMPOTENCY_LEASE`, 2 minutes) that it extends every third of it, so a long handler keeps its key, and a call that died frees it when the lease runs out; one repeat takes it over with a compare and set. The outcome is written only while the call still holds its lease — a call whose key was taken over does not overwrite the new owner's — on a context cut loose from the call with a five second budget, so a client that hangs up does not leave the key in progress. A failed write is retried twice within that budget; a key that is not printable ASCII or is longer than 255 characters answers `InvalidArgument`. `api.IdempotencyMessages(app, api.IdempotencyKeyMessages{…})` replaces the texts of a missing, too long or unprintable key — taply's are `Idempotency-Key is required in header` and `Idempotency-Key is too long`; an empty one keeps the platform's. Calls are counted in `api_idempotency_total{method,result}` (`replay`, `aborted`, `mismatch`, `lease_lost`, `stored`, `retry`, `save_failed`); expired records are removed at start and hourly, and an outcome past its retention is never replayed in between: the repeat that takes it over with the same compare and set runs the call again, a racing one answers `Aborted` while it runs. The leases are written from the clock of the process and taken over by the clock of the database, so the two must agree within a lease less a third of it, as NTP keeps them; a handler whose success is not a proto message is not remembered. Without the `postgres` module keys are ignored, and a required key stops the start.

The description marks messages with `additionalProperties: false`: that is the contract for clients, while the gateway is more forgiving and ignores unknown fields.

| Variable | Default | |
|---|---|---|
| `API_HTTP_ADDR` | `:8080` | REST gateway |
| `API_GRPC_ADDR` | `127.0.0.1:9091` | gRPC |
| `API_CORS_ORIGINS` | none | allowed browser origins, `*` for any |
| `API_CORS_HEADERS` | none | request headers a browser may send on top of the platform's and `api.CORSHeaders` |
| `API_CORS_EXPOSE` | none | response headers a page may read on top of `X-Request-ID` |
| `API_CORS_CREDENTIALS` | `true` | `Access-Control-Allow-Credentials`; `false` with `*` answers a literal `*` |
| `API_CORS_MAX_AGE` | `86400` | seconds a browser may cache a preflight |
| `API_CORS_PREFLIGHT_NO_CONTENT` | `false` | answer an `OPTIONS` the CORS policy does not with 204 and no CORS headers |
| `API_DOCS` | `true` | `/openapi.yaml` and `/docs`, and the documents of `api.openapi.documents` |
| `API_DOCS_DOCUMENTS` | all | the documents served by name, `all` for the whole description: `public`, `all,tablet` |
| `API_REFLECTION` | `false` | gRPC reflection |
| `API_MAX_RECV_MB`, `API_MAX_SEND_MB` | 16, 32 | message size limits; the request limit also caps HTTP bodies |
| `API_RATE_RPS`, `API_RATE_BURST` | 50, 100 | per client; `0` turns the limit off |
| `API_PUBLIC_RATE_RPS`, `API_PUBLIC_RATE_BURST` | 30, 60 | per client, public methods |
| `API_PRINCIPAL_RATE_RPS`, `API_PRINCIPAL_RATE_BURST` | 50, 100 | per principal named by `api.RateLimitKey`; without it, or with `0`, there is no such limit |
| `API_RATE_IPV6_PREFIX` | 64 | leading bits of an IPv6 address that make one client of the rate limit; `128` limits every address on its own |
| `API_IDEMPOTENCY_RETENTION`, `API_IDEMPOTENCY_LEASE` | `24h`, `2m` | how long a final outcome is remembered; the lease of a running call, extended while it runs. `API_IDEMPOTENCY_LOCK`, the old name of the lease, is still read, with a warning |
| `API_ACCESS_LOG`, `API_LOG_BODIES`, `API_SECURITY_HEADERS` | `true` | |
| `API_HTTP_STOP_TIMEOUT`, `API_GRPC_STOP_TIMEOUT` | none | on shutdown, how long running REST calls, then running gRPC calls, may finish before they are cut off; none waits as long as `SHUTDOWN_TIMEOUT` allows |
| `API_LOG_BODIES_MASK_PATHS` | none | path parts whose bodies are logged as `[REDACTED]`, on top of `api.MaskLogBodies` |
| `API_METRICS_TENANT_HEADER` | none | request header for the `tenant_name` label of the call metrics |
| `API_TRUSTED_PROXIES` | loopback and private networks | whose `X-Forwarded-For` names the client; networks set here, not the default, may also name it over gRPC with `api.ClientIPMetadataKey`; a request from elsewhere keeps its connection address, so an allowlist by address cannot be bypassed with a forged header. `none` ignores the header; the gRPC side still trusts loopback, for the gateway. Narrow it to the real proxies when clients can reach the service from a private network |

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

**Queues and attempts from business settings**, as taply sizes its queues and attempts from `app_configs`. Both are opt-in; without them the module behaves as before.

```go
riverx.QueuesFrom(app, func(ctx context.Context) (map[string]int, error) {
	return map[string]int{"sync": s.Sync().QueueWorkers(), "orders": s.Orders().QueueWorkers(), "payments": 3}, nil
})
riverx.MaxAttemptsFrom(app, func(_ context.Context, kind string) (int, bool) {
	if kind == (jobs.CreateOrderArgs{}).Kind() {
		return s.CreateOrder().MaxAttempts(), true
	}
	return 0, false // other kinds keep their own attempts
})
```

- `QueuesFrom` is called once, when the module starts working jobs and the settings are loaded; a changed value applies after a restart, so mark such settings `requires_restart` in `settings.yaml`. Queues set in `RIVER_QUEUES` or in `Config.Queues` win over it, so an environment can still pin its capacity; with neither the module works `default=10`. An error, an empty map or a queue without workers stops the start. An instance that only inserts (`RIVER_WORK=false`, `APP_ROLE=api`) does not call it.
- `MaxAttemptsFrom` is asked on every insert through `Queue` and for every periodic job, so a changed value applies to the jobs inserted after the change. Its value replaces the attempts the job carries in `InsertOpts()`; attempts passed explicitly to `Insert`/`InsertTx` or returned by `PeriodicJob.Opts` win over it. A kind it reports `false` or a non positive number for keeps its own attempts. Inserts made with `Client()` directly are River as it is and do not see it.

**Shutdown.** The module stops River softly: no new jobs are fetched and running ones may finish. `RIVER_STOP_TIMEOUT` bounds that wait; when it runs out — or `SHUTDOWN_TIMEOUT` does — the running jobs get their context cancelled and `RIVER_CANCEL_TIMEOUT` to return, so none of them writes into a database pool that is already closed. With phased shutdown (`SHUTDOWN_PHASES`, see [Process roles](#process-roles)) the queue drains after the HTTP servers stop and before gRPC does.

| Variable | Default | |
|---|---|---|
| `RIVER_QUEUES` | `default=10` | queues and their capacity: an environment parameter, staging and production differ in it |
| `RIVER_WORK` | `true` | `false` makes an instance insert only, for splitting API and worker instances |
| `RIVER_JOB_TIMEOUT` | `1m` | |
| `RIVER_COMPLETED_RETENTION`, `RIVER_CANCELLED_RETENTION`, `RIVER_DISCARDED_RETENTION` | `24h`, `24h`, `168h` | |
| `RIVER_STOP_TIMEOUT` | none | on shutdown, how long running jobs may finish; none waits as long as `SHUTDOWN_TIMEOUT` allows. Taply uses `6s` |
| `RIVER_CANCEL_TIMEOUT` | none | then how long cancelled jobs get to return; none waits without a limit. Taply uses `1s` |

## The `enums` module

The catalog of the enums of the project — every allowed status, provider or type with its description — for clients and for validation, after taply's enumsgen. Requires `api`.

Enums are declared in the domain package (`internal/domain`, or the `domain` option) with taply's marker above a const block:

```go
type PaymentStatus string

// go-enum: payment.status "Payment status" entity="Payments" order=20
const (
	PaymentPending PaymentStatus = "pending" // waiting for the customer
	PaymentPaid    PaymentStatus = "paid"    // money received
)
```

`platformgo generate` builds `internal/enums/enums.gen.go` from the markers; entities are sorted by `order`, values keep their order in the code, the comment after a value is its description. An entity whose markers disagree on its description or order, a duplicate enum and a const block with several names on one line are generation errors. Unlike taply, a project without markers gets an empty catalog, so enabling the module never breaks the build.

The module serves the catalog at `ENUMS_PATH` (`/v1/enums`); with `i18n` enabled the descriptions come in the language of the request from the keys `enum.<entity>`, `enum.<entity>.<enum>` and `enum.<entity>.<enum>.<value>`. `enums.From(app).Valid("payment", "status", value)` checks input.

## The `i18n` module

Translations of the API, ported from taply's i18n package. Requires `postgres` and `api`.

**Entity fields.** A string field is marked translatable in the proto file; the option comes with the platform as `proto/platform/i18n/v1/i18n.proto`:

```proto
import "platform/i18n/v1/i18n.proto";

message Product {
  int64 id = 1;
  string name = 2 [(platform.i18n.v1.i18n_field) = {key: "product.name", instance_key: "id"}];
}
```

Translations live in taply's `i18n_translations` table (`entity`, `entity_id`, `field`, `locale`, `text`); for every response the module collects the annotated fields, loads their translations with one query per entity type and substitutes the language of `Accept-Language` — `kk-KZ` matches `kk`, an unsupported language gets `I18N_DEFAULT_LOCALE`. An entity without a translation keeps its base value. A message with a `field_translations` map belongs to an editor and is left alone. For its requests, as taply does: when the request carries the translation into the default locale for a field — empty too, which clears the field — the base field takes it, trimmed, before normalisation and validation, so an editor that edits only the translations and sends the base field as it loaded it does not lose the edit; then, after validation, the base value is copied into the default locale, so the column and the translation never disagree. `i18n.From(app)` reads and writes translations (`Set`, `SetBatch`, `SetBatchIfMissing` for syncs that must not overwrite edits, `GetAll`, `GetBatchAll`, in a transaction with `WithTx`); `i18nx.TranslationsToProto`, `ProtoToTranslations` and `ValidateFields` serve editor APIs. `i18n.SkipTranslation(app, fn)` turns substitution off for chosen calls, as taply does for some tablets; the substitution runs in `PhaseTransform`, after the project's authentication, so `fn` sees the caller it put into the context. Error messages are translated in `PhaseErrorsI18n`, outside authentication, so its refusals are translated too. `i18n.WithErrorPhase(api.PhaseAfterValidate)` translates only what the handler and the phases after validation return, as taply did, so a 400, 401 or 403 of the chain keeps its text whatever `Accept-Language` says; a generated project gives it with `i18n.Configure(modules, …)` in `main.go`, as for `rbac`.

**Error messages** are translated the way taply does, in order: a whole sentence template, `{resource} {id}: {message}`, `missing {field}` and `{field} is required`, an exact message. The platform translates its own messages (rate limit, authentication, idempotency, routing) and taply's generic ones into Russian, Kazakh and English; the project adds its own in `i18n/messages.yaml`, created with the module and embedded into the binary:

```yaml
error.resource.order: {en: order, ru: заказ, kk: тапсырыс}
error.msg.product is out of stock: {en: product is out of stock, ru: товар не в наличии, kk: өнім қоймада жоқ}
error.tmpl.image_too_large: {en: "image is too large: %s; maximum allowed is %s", ru: "изображение слишком большое: %s; максимально допустимо %s"}
```

A broken file stops the start; a template whose translation has a different number of `%s` than its English pattern is refused.

<a id="external-provider-messages"></a>**Messages of external providers.** By default the failures of external providers — an `ErrorInfo` of reason `EXTERNAL_TIMEOUT` or `EXTERNAL_UNAVAILABLE`, as `api.ExternalTimeout` and `api.ExternalUnavailable` build them — keep their text, which names the provider. `i18n.WithExternalErrors()` translates them like any other message, as taply does for the guest at a tablet: `api.ExternalTimeout("zebra", "external service timeout")` gets the entry of its exact text, and a message without an entry keeps its text. The code and the `ErrorInfo` stay, so the gateway still answers 504 and the client still sees the provider. An `Unavailable` is never translated, like any `Unavailable`, so `api.ExternalUnavailable` keeps its text either way. A generated project gives the option in `main.go`, `i18n.Configure(modules, i18n.WithExternalErrors())`; outside the module, `(*i18nx.Client).TranslateExternal(true)`. The key has `: ` in it, so YAML needs it quoted:

```yaml
"error.msg.zebra: external service timeout": {en: The cash register did not respond in time. Please try again, ru: Касса не ответила вовремя. Попробуйте ещё раз, kk: Касса уақытында жауап бермеді. Қайталап көріңіз}
```

| Variable | Default | |
|---|---|---|
| `I18N_DEFAULT_LOCALE` | `ru` | language of the base columns and the fallback |
| `I18N_LOCALES` | `ru,kk,en` | languages the API answers in |

## The `web` module

Pages rendered on the server by the same binary, on the same address as the API: a sign up form, a customer's card page, anything a browser opens. Requires `api` — pages are its plain routes, so they share its server, request ids, metrics, access log and body limit.

```yaml
modules:
  api: {}
  web: {}
```

The module pins [templ](https://templ.guide) as a tool: `platformgo generate` turns every `*.templ` file into Go code next to it and removes the code of deleted templates, and `platformgo verify` fails when that code is stale. The generated files are committed like the rest of generated code, so the image builds without the generator. htmx and styles are the project's own files, embedded and served by `kit/webx`.

```go
kit := web.From(app)
assets, _ := webx.NewAssets(static.FS, "/static/")

page := func(h http.HandlerFunc) http.Handler { return webx.PageHeaders(kit.CSRF.Middleware(h)) }
api.HandleHTTP(app, "GET /{$}", page(signup.Form))   // the root itself: "/" is the gateway's catch-all
api.HandleHTTP(app, "POST /{$}", page(signup.Submit))
api.HandleHTTP(app, "GET /c/{token}", webx.NoReferrer(page(card.Show)))
api.HandleHTTP(app, assets.Pattern(), assets.Handler())
```

Page routes must be more specific than `/`, which the REST gateway serves: `GET /{$}` for the root, `GET /c/{token}` and so on.

What `kit/webx` gives:

| | |
|---|---|
| `Render(w, r, status, page, fragment)` | the whole page, or only the fragment when htmx asks (`HX-Request`) — one route for both, forms keep working without JavaScript |
| `Redirect(w, r, url)` | `303` after a plain form post, `HX-Redirect` for htmx |
| `CSRF` | `http.CrossOriginProtection` plus a token in every form (`CSRFInput()`, or `X-CSRF-Token` for htmx) matching a `__Host-` cookie |
| `Cookies` | small state of a multi step form in a cookie sealed with AES-GCM, bound to its name, with expiry |
| `Assets` | embedded files under content hashed names (`app.1a2b3c4d.css`), cached for a year; `Path("app.css")` for templates |
| `PageHeaders`, `NoReferrer` | a CSP that allows nothing inline and nothing from other origins; `no-referrer` and `no-store` for pages whose address is a secret |
| `Locale(r, supported, fallback)` | the visitor's choice from a cookie, then `Accept-Language` |

| Variable | Default | |
|---|---|---|
| `WEB_SECRET_KEY` | — | required; base64 of at least 32 bytes (`openssl rand -base64 32`), seals cookies |
| `WEB_INSECURE_COOKIES` | `false` | cookies over plain http, for local development |

## The `rbac` module

Role based access to the gRPC methods of the API — REST calls included, since they pass through gRPC — with taply's casbin model and policy format. Requires `api`.

`rbac/policy.csv` is created when the module is enabled and belongs to the project; the generated `rbac/policy.gen.go` embeds it, so the image needs no configuration folder:

```
# p, role, method pattern (keyMatch2), action
# * is the public role: no authentication
p, *, /grpc.health.v1.Health/*, *
# every method of a service
p, admin, /shop.v1.OrdersService/*, *
p, support, /shop.v1.OrdersService/GetOrder, *
```

A comment takes a line of its own: a `#` after a rule is part of its last field.

The module decides what a role may call; who the caller is stays with the project. The project's authentication interceptor puts the roles into the context and skips authentication for public methods:

```go
api.AddUnaryInterceptor(app, func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	if rbac.IsPublic(app, info.FullMethod) {
		return h(ctx, req)
	}
	claims, err := tokens.Validate(ctx) // the project's own tokens
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "bad token")
	}
	return h(rbac.WithRoles(ctx, claims.Roles...), req)
})
```

The access check runs in `PhaseAuthorize`, after the project's authentication and before its guards and request validation: no roles in the context answers `Unauthenticated`, no matching role answers `PermissionDenied`. Only the token itself decides `Unauthenticated`: a valid token whose scope names no role the service knows is `rbac.WithRoles(ctx)` with no roles, an authenticated caller who may do nothing, and answers `PermissionDenied`. A caller cannot claim the public role `*` to reach a private method. A broken policy line stops the start with its line number, a rule that matches no registered method is logged at start, and refused calls are counted in `rbac_denied_total{method,reason}`.

### Any casbin model

Since v0.11.0 the module takes any casbin model and a policy from any casbin adapter. Without these options it is taply's, as above, byte for byte: the same model, checks, errors, logs and metrics.

| Option | What it does |
|---|---|
| `WithModel(text)` | a casbin model of the project's own: role inheritance (`g`), domains or tenants, matchers and effects of its own |
| `WithPolicy(text)` | the policy text, `rbac/policy.csv` in the generated wiring. In taply's model `ParsePolicy` checks it; in a model of the project's own it is casbin's CSV — `p`, `g` and whatever the model defines — and casbin checks it, a broken rule stopping the start with its line |
| `WithAdapter(persist.Adapter)` | the policy from a casbin adapter — Postgres, a file, a service — instead of the text, which is then ignored. In taply's model every load is checked as `ParsePolicy` checks a text |
| `WithReloadEvery(d)` | reload the adapter's policy on a period while the service runs |
| `WithFunction(name, fn)` | a function the matcher may call, as casbin's `AddFunction` |
| `WithRequest(func(ctx, method) ([][]any, error))` | how a call becomes the requests to enforce, see below |
| `WithPublic(func(method) []any)` | the request that, when allowed, makes a method public |

A generated project gives the options in its own `main.go`, between `platformModules` and `platform.Run`; the generated wiring stays as it is:

```go
modules := platformModules(cfg)
if err := rbac.Configure(modules,
	rbac.WithModel(accessModel),
	rbac.WithAdapter(policyStore), // a casbin adapter over the project's database
	rbac.WithReloadEvery(30*time.Second),
	rbac.WithRequest(tenantRequest),
); err != nil {
	log.Fatal(err)
}
if err := platform.Run(platform.Config{Service: serviceName}, modules, wireDomain); err != nil {
	log.Fatal(err)
}
```

**Requests.** By default a call is one request per role from `rbac.WithRoles`: `{role, method, "*"}`, allowed when any is. That fits a model whose request is `sub, obj, act`, role inheritance included. A model with another request — `r = sub, dom, obj, act` — needs `WithRequest`, or the start fails with `give WithRequest`. The function returns the requests in the order of the model's `request_definition`; the call is allowed when any of them is. It returns `rbac.ErrUnauthenticated` for a caller who has not authenticated (`Unauthenticated`, counted as `unauthenticated`), no requests for one who may do nothing (`PermissionDenied`, `forbidden`), and any other error to refuse: an error with a gRPC status reaches the client as it is, any other answers `Internal` and is logged; both are counted with the reason `error`. An error of casbin itself — a matcher on a missing field — answers `Internal` the same way.

```go
func tenantRequest(ctx context.Context, method string) ([][]any, error) {
	users, ok := rbac.RolesFrom(ctx)
	if !ok {
		return nil, rbac.ErrUnauthenticated
	}
	tenant := tenantFrom(ctx) // the project's own
	var reqs [][]any
	for _, u := range users {
		reqs = append(reqs, []any{u, tenant, method, "*"})
	}
	return reqs, nil
}
```

**Public methods.** A method is public when the public request is allowed: `{"*", method, "*"}` by default, as in taply's model, so a model of `sub, obj, act` keeps `p, *, /grpc.health.v1.Health/*, *` working when its matcher compares `r.sub == p.sub`. A model with another request has no public methods until `WithPublic` gives the request — for the domain model above, `[]any{"*", "*", method, "*"}` with a rule `p, *, *, /grpc.health.v1.Health/*, *`. Public methods skip the request function and authentication as before. The public request is also enforced once at start, so a matcher calling an unknown function stops the start instead of failing every call.

**`Rules` and `Unmatched`.** In a model of the project's own a rule is the `p` line read through the fields named `sub`, `obj` and `act` of `policy_definition` (empty when the model has none), with its line when the policy is a text and line 0 from an adapter. Only rules whose `obj` starts with `/` are method patterns: `Unmatched`, and the warning at start, look at those alone, so a rule over another kind of object is never reported. `Allowed(roles, method)` asks `{role, method, "*"}` as before; `Check(ctx, method)` asks what the interceptor asks, with the request function — use it for a check outside gRPC.

**Reload.** `rbac.From(app).Reload()` reads the adapter again and swaps the policy whole: a call being checked sees the old policy or the new one, never a mix (the enforcer is casbin's `SyncedEnforcer`). A load that fails — the database is down, or in taply's model a rule breaks the format — leaves the policy in force and returns the error. `WithReloadEvery` does the same on a period from the start of the module until the service stops, logs a failure as a warning and counts every reload in `rbac_policy_reloads_total{result="ok|error"}`, a metric that exists only with an adapter. A policy text is part of the binary: `Reload` does nothing.

**Casbin itself.** `rbac.From(app).Casbin()` is the `*casbin.SyncedEnforcer` for what the module does not wrap: implicit roles, domains, policy changes through the casbin API (saved through the adapter when it supports it, kept in memory with a text), a watcher calling `LoadPolicy` on changes from other instances. In taply's model the adapter it holds is the project's wrapped in the format check. `rbac.NewEnforcerWith(opts...)` builds the same enforcer outside the module, for a command or a test.

## The `s3` module

Object storage for the files of the project: MinIO locally (the release taply runs, with its console on `127.0.0.1:9001`), any S3 compatible storage in production. The variables and the storage API are taply's.

```go
st := s3.From(app)

// Public bucket: created on first use with an anonymous read policy, so nginx serves
// the file without signing. An existing bucket keeps the policy it has.
path, err := st.Put(ctx, "images", true, "restaurants/42/logo.png", "image/png", data)
link := st.PublicURL("images", path) // S3_PUBLIC_URL, or the endpoint

// Private bucket: read through the service or a signed link.
_, err = st.PutStream(ctx, "receipts", false, "2026/09/1.pdf", "application/pdf", file, size)
signed, err := st.SignedURL(ctx, "receipts", "2026/09/1.pdf", 15*time.Minute)
r, err := st.Get(ctx, "receipts", "2026/09/1.pdf") // s3.ErrNotFound when missing
err = st.Delete(ctx, "images", path)
```

The module connects during startup and checks the keys, so a wrong address stops the start instead of the first upload; `/health` keeps checking. `S3_ENDPOINT`, `S3_ACCESS_KEY` and `S3_SECRET_KEY` are required; `S3_USE_SSL`, `S3_REGION` and `S3_PUBLIC_URL` are optional.

## The `settings` module

Business settings of the project: the schema in `settings.yaml`, the values in the database, typed access generated into `internal/settings`.

```yaml
modules:
  postgres: {}       # required: the values live in the database
  settings:
    schema: settings.yaml
```

The module owns its table (`SETTINGS_TABLE`, `platform_settings` by default) and keeps it at the current version with `pgdb.MigrateSchema`, loads the overrides during startup, refreshes them every `SETTINGS_REFRESH_INTERVAL` and reports through `/health` when it can no longer read them. The levels of settings and the generated API are described in [settings.md](settings.md); `settings.Import` and `settings.ImportQuery` move settings from an older table, storing only the overrides ([settings.md](settings.md#moving-existing-settings)).

## Process roles

One binary, split by `APP_ROLE`, the way taply runs API and worker instances:

| `APP_ROLE` | What runs |
|---|---|
| `all` (default) | everything |
| `api` | the API and the admin panel; River only inserts jobs |
| `worker` | River works jobs and runs periodic jobs; the API and the admin panel are not served |

Every role runs the same `wireDomain`: services, pages and workers are registered everywhere, and each module decides what to start. `/health` and `/metrics` are served in every role.

**Tracing** follows taply: with `OTEL_EXPORTER_OTLP_ENDPOINT` set, spans go over OTLP gRPC to a collector — the one of the `monitoring` module or of an external agent; W3C `traceparent` and baggage carry the trace in and out. The gRPC server and the HTTP side are instrumented and the gateway passes the trace to gRPC, so a REST call is one trace; log lines written with a context carry `trace_id` and `span_id`. Without the variable nothing is exported, yet an incoming trace still continues. Sampling and the exporter follow the standard `OTEL_*` variables; `OTEL_SERVICE_NAME` names the service in the spans instead of `project.service`, so a service moving to the platform keeps the name its traces are searched by (taply: `be-maas-api`).

**Profiling** follows taply too: with `PYROSCOPE_ADDR` set, say `http://pyroscope:4040`, the process sends continuous profiles — CPU, memory, goroutines, mutexes, blocking — to Pyroscope under the same name as its traces. Empty, the default, starts nothing. A profiler that does not start is logged, and the service runs without it.

**Moving metrics** — a service moving to the platform has dashboards and alerts on its own metric names. `app.AliasMetric(name, alias)` in wireDomain serves a metric under the old name as well, with the same series and values, until they read the platform's name; then the call goes:

```go
app.AliasMetric("api_deprecated_calls_total", "maas_deprecated_rpc_calls_total")
```

The alias lives only at `/metrics`, and a real metric of the same name wins over it. `METRICS_OPENMETRICS=true` answers a scraper that asks for OpenMetrics — Prometheus does — in that format, which carries the exemplars of the `api` module; the default is the classic text format, as before.

**Alerts** are ordinary log lines. A use case that needs someone's attention logs with the `alert` attribute (`logx.AlertKey`) set to the kind of the problem, and the platform counts the line in `app_alerts_total{kind}` — whichever logger derived from `app.Logger()` wrote it and whatever `LOG_LEVEL` is. `app.DeclareAlerts(kinds...)` in `wireDomain` starts the known kinds at zero, so `increase()` sees the first alert after a restart. The alert rules on the counter belong to the project: which kinds page someone is its decision.

```go
app.DeclareAlerts("pool_empty", "code_send_failed")
log.ErrorContext(ctx, "the card pool is empty", logx.AlertKey, "pool_empty")
```

**Shutdown** stops the modules in reverse order of initialisation within `SHUTDOWN_TIMEOUT`, so the database outlives what uses it. `SHUTDOWN_PHASES=true` (`platform.Config.ShutdownPhases` in code) switches to taply's order: the HTTP servers — the REST gateway and the admin panel — stop taking requests first, the job queue drains next while gRPC clients are still served, gRPC goes last, and the modules without phases stop after them in reverse order. Each step can have a budget of its own (`API_HTTP_STOP_TIMEOUT`, `RIVER_STOP_TIMEOUT`, `RIVER_CANCEL_TIMEOUT`, `API_GRPC_STOP_TIMEOUT`) inside the shared `SHUTDOWN_TIMEOUT`; taply's are 5s, 6s + 1s and 1s. Set them when turning phases on: the steps share one deadline, so an unbounded HTTP step that takes the whole timeout leaves the queue no time for a soft stop, and its jobs are cancelled at once. A module of a project joins the order by implementing `platform.PhasedStopper`: its `StopPhases()` returns steps with `platform.PhaseHTTP`, `PhaseQueue`, `PhaseGRPC` or a number between them, and together they must do what its `Stop` does; one that returns no steps is stopped with `Stop`.

Other platform variables, read when the code does not set them: `OPS_ADDR` (`:9090`), `SHUTDOWN_TIMEOUT` (`20s`), `SHUTDOWN_PHASES` (`false`), `LOG_LEVEL` (`info`), `LOG_FORMAT` (`json` or `text`), `METRICS_OPENMETRICS` (`false`). A bad value stops the start, listed together with every other bad variable.

## Dependencies between modules

`river` requires `postgres`, `admin` requires `settings` for the settings pages, `settings` requires `postgres`. `platformgo plan` reports missing modules and offers to add them.

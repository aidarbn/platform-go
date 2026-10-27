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
| **rbac** | access to gRPC methods by role, taply's casbin model and policy format |
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

`make generate` runs buf with the plugins pinned in `go.mod` by `apply` (buf, protoc-gen-go, protoc-gen-go-grpc, protoc-gen-grpc-gateway, protoc-gen-connect-openapi): Go code into `internal/api/gen`, an OpenAPI 3.1 description with the protovalidate rules into `api/openapi/openapi.yaml`. The human part of that description — title, intro, tags, security scheme — lives in `api/openapi/base.yaml`, created once on `apply` and owned by the project from then on: only the project knows what to tell the reader of its API, and the proto files cannot say it. Tags and operation ids use the short service name. `verify` generates into a scratch directory and fails when the committed code is stale.

The handler is registered from `wire.go`:

```go
api.Register(app, api.Service{
	GRPC:    func(s *grpc.Server) { shopv1.RegisterOrdersServiceServer(s, orders.NewHandler(repo)) },
	Gateway: shopv1.RegisterOrdersServiceHandler,
})
api.AddUnaryInterceptor(app, auth.Interceptor(tokens))           // authentication and the like
api.HandleHTTP(app, "POST /webhooks/kaspi", kaspi.WebhookHandler) // routes that are not gRPC
```

What the module does for every call, REST included — the gateway calls the local gRPC server, so both go through one chain: metrics (taply's `grpc_server_handled_total`, `grpc_server_handling_seconds` and `grpc_req_panics_recovered_total`, which the go-grpc dashboard and alerts of the monitoring agent read), logging of failed calls, panic recovery, errors without internal details (a plain error becomes `Internal`, a status keeps its code), project interceptors, protovalidate validation with the violations as details. The REST side speaks snake_case JSON as in the proto files, returns every field, ignores unknown request fields and forwards request headers as gRPC metadata. Also: gRPC health, optional reflection, CORS, `/openapi.yaml` and `/docs`, graceful stop.

The HTTP side, following taply's gateway:

- **File uploads**: a `multipart/form-data` request fills the request message. A part named after a field of a message with `filename`, `content_type` and `content` becomes a file; a repeated field takes several parts in order (`images`, `images`) or by index (`images[0]`, `images[3]`, with empty entries for skipped ones); a JSON part fills a message field; other parts set scalar fields.
- **Request id and client address**: `X-Request-ID` is taken from the request or generated, returned in the response and passed to gRPC handlers — `api.RequestID(ctx)`. The client address — `api.ClientIP(ctx)` — is the connection address, or, when the connection comes from `API_TRUSTED_PROXIES`, the last `X-Forwarded-For` hop: the one the proxy itself saw, read from the last header line, since HAProxy adds a line of its own. Only that hop is taken, never one further left: the trusted proxies default to every private network, where a client may sit too. The gateway hands the address to the gRPC server under its own metadata key, `api.ClientIPMetadataKey` (`x-platform-client-ip`), and drops an incoming header of that name, so a client cannot choose it. The old `X-Client-IP` header is overwritten with the resolved address, so plain routes of `api.HandleHTTP` can still read it, and the gateway passes the address under the old `x-client-ip` metadata key as well, for handlers that read it themselves. The gRPC server believes the key only from loopback — where the gateway of the process connects from —, from its own listen address, or from networks set in `API_TRUSTED_PROXIES`, so a gRPC proxy listed there may set it; the default of every private network does not count here, so a gRPC port open to the private network does not let its hosts name themselves. A trusted peer that sends only the old `x-client-ip` key — a proxy or a client written for an older release — is still believed, with one warning in the log. Both keys of the incoming metadata are rewritten to the checked address before any interceptor of the project, so a handler that reads them itself never sees what an untrusted peer sent. A proxy on a trusted address that passes the gRPC metadata of its clients through, such as nginx with `grpc_pass`, must drop or set both keys itself. A direct gRPC client anywhere else is its peer address whatever metadata it sends. `x-forwarded-for` in gRPC metadata is not an address to trust: grpc-gateway appends the HTTP peer to whatever the client sent.
- **Limits and headers**: a body over `API_MAX_RECV_MB` answers 413; taply's security headers (`nosniff`, `DENY`, `no-store`, HSTS).
- **CORS** — off until `API_CORS_ORIGINS` lists origins: exact ones, `https://*.example.com` for any subdomain, `http://localhost:*` for any port, `*` for any. An allowed origin gets `Access-Control-Allow-Origin` on every response and `X-Request-ID` in `Access-Control-Expose-Headers`; a preflight answers 204 with the methods, the headers and `Access-Control-Max-Age`, while a foreign origin goes on to the API with no CORS headers. Every response but the literal `*` carries `Vary: Origin`, so a shared cache keeps the answers apart. A browser may send `Authorization`, `Content-Type`, `Accept`, `X-Request-ID` and `Idempotency-Key`; headers the API cannot work without are registered in code with `api.CORSHeaders(app, "X-Restaurant-Id")` in wireDomain, and a stand adds its own with `API_CORS_HEADERS`. The preflight answers this fixed list rather than echoing `Access-Control-Request-Headers`, so a header the browser refuses is one missing from it. By default the answer echoes the origin with `Access-Control-Allow-Credentials: true`, so the page may send cookies — with `*` too, which the module warns about on start. A public API, taply's mode, sets `API_CORS_ORIGINS=*` and `API_CORS_CREDENTIALS=false`: the answer is then a literal `*` without credentials. `API_CORS_CREDENTIALS=false` with listed origins echoes them without credentials.
- **Metrics and access log**: taply's `http_gateway_requests_total{method,path,status}`, `http_gateway_request_duration_seconds`, `http_gateway_response_size_bytes` and `http_gateway_requests_in_flight`, which the go-http dashboard and alerts read, labelled by the route template — `/v1/orders/{id}`, never the concrete path — with unmatched paths as `unknown`; an access log line per request with the route, status, duration, client address and request id — the concrete path is logged only when no route matched, because a path segment can be a secret (a link whose token is the credential) and the log is shipped off the box, plus the bodies of failed requests with binary and multipart bodies summarised instead of dumped. Paths that take secrets keep their bodies out: `api.MaskLogBodies(app, "/certificate")` in code or `API_LOG_BODIES_MASK_PATHS` per stand log `[REDACTED]` for any path containing one of the parts, as taply does for certificate uploads.
- **Call metrics per tenant** — `API_METRICS_TENANT_HEADER=tenant-name` labels `grpc_server_*` with `tenant_name` from that request header (gRPC metadata or a REST header), `unknown` without it, as taply's do. Off by default, and the metrics keep their labels. Every distinct value is a new series of every call metric, and the client sends the header before it is authenticated, so the value is bounded: up to 64 letters, digits, `.`, `-` and `_`, anything else is counted as `invalid`, and past 256 distinct tenants in a process a new one is counted as `other`. Use it where the tenants are a short, known list, such as taply's restaurants behind its own proxy.
- **Trace exemplars** — with `METRICS_OPENMETRICS=true` a sampled call leaves its trace id as an exemplar (`traceID`, taply's label) of the call counters and the latency histogram, so a Grafana panel links a slow bucket to its trace. Prometheus stores them with `--enable-feature=exemplar-storage`.
- **Routing errors** name the method and the path: `GET /v1/nothing: route not found`.
- **Error contract** — an API with its own error body replaces the gateway's writer: `api.HTTPErrorHandler(app, handler)` receives every REST error — handler statuses, decoding, routing, and a body over `API_MAX_RECV_MB` as a `*runtime.HTTPStatusError` with 413 — and writes the status and the body the contract wants. A middleware that reads the body itself, such as a request signature check, asks `api.BodyTooLarge(r)`. Plain routes answer 413 on their own.

And on every call, following taply's interceptors:

- **Rate limit** — a token bucket per client address, 50 calls a second with a burst of 100, and a separate 30/60 for public methods (the ones `rbac` marks public, or `api.PublicMethods`). An IPv6 client is limited by its /64, the block one subscriber gets, so it cannot take a new bucket with every address of the block; `API_RATE_IPV6_PREFIX` changes the length, `128` limits every address on its own. A refused call answers `ResourceExhausted`, 429 over REST, and is counted in `api_rate_limited_total`. It runs before project interceptors, so a flood never reaches token validation. A second bucket limits each principal, 50/100 by default, once the project names it: `api.RateLimitKey(app, func(ctx) string)` returns the user, API client or device the authentication put into the context. It runs after the project interceptors, counts refusals in `api_principal_rate_limited_total`, and leaves a call with an empty key to the limit by address; the limit by address applies to every call. Streams are not limited.
- **Deprecated methods** — `option deprecated = true` in the proto file is enough: calls are counted in `api_deprecated_calls_total{method}` and logged with the caller's address and user agent, so the method can go once nobody calls it. taply keeps this list in code. `api.DeprecatedReplacements(app, map[string]string{"/shop.v1.Orders/GetOld": "/shop.v1.Orders/Get"})` in wireDomain adds taply's `replacement` label to the metric and the log line, and counts a listed method as deprecated even without the proto option; without the call the metric has only `method`.
- **Idempotency** — a call with an `Idempotency-Key` header is remembered in the database of the `postgres` module, with taply's semantics: a repeat gets the saved answer without running the handler again, the same key with a different payload answers `AlreadyExists`, a repeat while the first call runs answers `Aborted`, a final failure is remembered and a failure that says try again (a `RetryableError`, or `Unavailable`, `DeadlineExceeded`, `ResourceExhausted`, `Aborted`) lets the next repeat run. `api.RequireIdempotency(app, methods...)` makes a key mandatory, as taply does for every create method; `api.IdempotencyUser` scopes keys to the caller. Unlike taply, taking over a retry is an atomic compare and set, and a call that died while holding its key can be repeated once its lock expires. Without the `postgres` module keys are ignored, and a required key stops the start.

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
| `API_DOCS` | `true` | `/openapi.yaml` and `/docs` |
| `API_REFLECTION` | `false` | gRPC reflection |
| `API_MAX_RECV_MB`, `API_MAX_SEND_MB` | 16, 32 | message size limits; the request limit also caps HTTP bodies |
| `API_RATE_RPS`, `API_RATE_BURST` | 50, 100 | per client; `0` turns the limit off |
| `API_PUBLIC_RATE_RPS`, `API_PUBLIC_RATE_BURST` | 30, 60 | per client, public methods |
| `API_PRINCIPAL_RATE_RPS`, `API_PRINCIPAL_RATE_BURST` | 50, 100 | per principal named by `api.RateLimitKey`; without it, or with `0`, there is no such limit |
| `API_RATE_IPV6_PREFIX` | 64 | leading bits of an IPv6 address that make one client of the rate limit; `128` limits every address on its own |
| `API_IDEMPOTENCY_RETENTION`, `API_IDEMPOTENCY_LOCK` | `24h`, `1m` | |
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

Translations live in taply's `i18n_translations` table (`entity`, `entity_id`, `field`, `locale`, `text`); for every response the module collects the annotated fields, loads their translations with one query per entity type and substitutes the language of `Accept-Language` — `kk-KZ` matches `kk`, an unsupported language gets `I18N_DEFAULT_LOCALE`. An entity without a translation keeps its base value. A message with a `field_translations` map belongs to an editor and is left alone; for its requests the base value is copied into the default locale, so the column and the translation never disagree. `i18n.From(app)` reads and writes translations (`Set`, `SetBatch`, `SetBatchIfMissing` for syncs that must not overwrite edits, `GetAll`, `GetBatchAll`, in a transaction with `WithTx`); `i18nx.TranslationsToProto`, `ProtoToTranslations` and `ValidateFields` serve editor APIs. `i18n.SkipTranslation(app, fn)` turns substitution off for chosen calls, as taply does for some tablets.

**Error messages** are translated the way taply does, in order: a whole sentence template, `{resource} {id}: {message}`, `missing {field}` and `{field} is required`, an exact message. The platform translates its own messages (rate limit, authentication, idempotency, routing) and taply's generic ones into Russian, Kazakh and English; the project adds its own in `i18n/messages.yaml`, created with the module and embedded into the binary:

```yaml
error.resource.order: {en: order, ru: заказ, kk: тапсырыс}
error.msg.product is out of stock: {en: product is out of stock, ru: товар не в наличии, kk: өнім қоймада жоқ}
error.tmpl.image_too_large: {en: "image is too large: %s; maximum allowed is %s", ru: "изображение слишком большое: %s; максимально допустимо %s"}
```

A broken file stops the start; a template whose translation has a different number of `%s` than its English pattern is refused.

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
p, *, /grpc.health.v1.Health/*, *                  # * — public, no authentication
p, admin, /shop.v1.OrdersService/*, *              # every method of a service
p, support, /shop.v1.OrdersService/GetOrder, *
```

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

The access check runs after every project interceptor and before request validation: no roles in the context answers `Unauthenticated`, no matching role answers `PermissionDenied`. A caller cannot claim the public role `*` to reach a private method. A broken policy line stops the start with its line number, a rule that matches no registered method is logged at start, and refused calls are counted in `rbac_denied_total{method,reason}`.

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

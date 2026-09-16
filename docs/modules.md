# Module catalogue

A module is declared as a section in `platformgo.yaml` and applied with `platformgo apply`. Module code lives in `kit/<module>` and is imported; a project only receives generated files (wiring, compose, generator configuration) and owned files it keeps.

| Module | What it gives the project |
|---|---|
| **core** (always) | settings from the environment, slog, `/metrics`, `/health`, graceful shutdown, tracing, profiling, Makefile, Dockerfile, linters, CI |
| **postgres** | pgx pool, goose migrations, application migrations, database in docker-compose, query generation (sqlc and jet) |
| **river** | queues, periodic jobs, workers, riverui, test helpers |
| **api** | gRPC server with an interceptor chain, REST gateway, multipart, OpenAPI 3.1, documentation page |
| **admin** | admin shell: login, roles, TOTP, audit log; pages for settings, jobs and state; a place for project pages |
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

**Project pages** are registered with `admin.AddPage` and drawn inside the panel: `admin.Render` puts the body the project renders — `html/template`, templ, anything that writes HTML — under the menu, the account and the `?ok=` / `?error=` banners. Forms carry `admin.CSRFField` with `admin.CSRFToken(r)`; `admin.Record(r, action, target, details)` writes to the audit log; `admin.UserFrom(ctx)` tells who is acting. A path ending in `/` takes the whole subtree, so a list and its detail pages are one page.

```go
admin.AddPage(app, admin.Page{Title: "Customers", Path: "/customers/", Roles: []string{"support"}, Handler: customers})

func (h *Customers) show(w http.ResponseWriter, r *http.Request) {
	admin.Render(w, r, "Customer", func(w io.Writer) error { return views.Customer(c).Render(r.Context(), w) })
}
```

`ADMIN_LANGUAGE` (`en` or `ru`) sets the language of the panel itself.

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

What the module does for every call, REST included — the gateway calls the local gRPC server, so both go through one chain: metrics (taply's `grpc_server_handled_total`, `grpc_server_handling_seconds` and `grpc_req_panics_recovered_total`, which the go-grpc dashboard and alerts of the monitoring agent read), logging of failed calls, panic recovery, errors without internal details (a plain error becomes `Internal`, a status keeps its code), project interceptors, protovalidate validation with the violations as details. The REST side speaks snake_case JSON as in the proto files, returns every field, ignores unknown request fields and forwards request headers as gRPC metadata. Also: gRPC health, optional reflection, CORS, `/openapi.yaml` and `/docs`, graceful stop.

The HTTP side, following taply's gateway:

- **File uploads**: a `multipart/form-data` request fills the request message. A part named after a field of a message with `filename`, `content_type` and `content` becomes a file; a repeated field takes several parts in order (`images`, `images`) or by index (`images[0]`, `images[3]`, with empty entries for skipped ones); a JSON part fills a message field; other parts set scalar fields.
- **Request id and client address**: `X-Request-ID` is taken from the request or generated, returned in the response and passed to gRPC handlers — `api.RequestID(ctx)`; the client address is the last `X-Forwarded-For` hop (the one the proxy saw) or the connection address — `api.ClientIP(ctx)`, which the client cannot forge through the gateway.
- **Limits and headers**: a body over `API_MAX_RECV_MB` answers 413; taply's security headers (`nosniff`, `DENY`, `no-store`, HSTS); CORS origins accept `https://*.example.com` and `http://localhost:*`.
- **Metrics and access log**: taply's `http_gateway_requests_total{method,path,status}`, `http_gateway_request_duration_seconds`, `http_gateway_response_size_bytes` and `http_gateway_requests_in_flight`, which the go-http dashboard and alerts read, labelled by the route template — `/v1/orders/{id}`, never the concrete path — with unmatched paths as `unknown`; an access log line per request with the route, status, duration, client address and request id — the concrete path is logged only when no route matched, because a path segment can be a secret (a link whose token is the credential) and the log is shipped off the box, plus the bodies of failed requests with binary and multipart bodies summarised instead of dumped.
- **Routing errors** name the method and the path: `GET /v1/nothing: route not found`.
- **Error contract** — an API with its own error body replaces the gateway's writer: `api.HTTPErrorHandler(app, handler)` receives every REST error — handler statuses, decoding, routing, and a body over `API_MAX_RECV_MB` as a `*runtime.HTTPStatusError` with 413 — and writes the status and the body the contract wants. A middleware that reads the body itself, such as a request signature check, asks `api.BodyTooLarge(r)`. Plain routes answer 413 on their own.

And on every call, following taply's interceptors:

- **Rate limit** — a token bucket per client address, 50 calls a second with a burst of 100, and a separate 30/60 for public methods (the ones `rbac` marks public, or `api.PublicMethods`). A refused call answers `ResourceExhausted`, 429 over REST, and is counted in `api_rate_limited_total`. It runs before project interceptors, so a flood never reaches token validation.
- **Deprecated methods** — `option deprecated = true` in the proto file is enough: calls are counted in `api_deprecated_calls_total{method}` and logged with the caller's address and user agent, so the method can go once nobody calls it. taply keeps this list in code.
- **Idempotency** — a call with an `Idempotency-Key` header is remembered in the database of the `postgres` module, with taply's semantics: a repeat gets the saved answer without running the handler again, the same key with a different payload answers `AlreadyExists`, a repeat while the first call runs answers `Aborted`, a final failure is remembered and a failure that says try again (a `RetryableError`, or `Unavailable`, `DeadlineExceeded`, `ResourceExhausted`, `Aborted`) lets the next repeat run. `api.RequireIdempotency(app, methods...)` makes a key mandatory, as taply does for every create method; `api.IdempotencyUser` scopes keys to the caller. Unlike taply, taking over a retry is an atomic compare and set, and a call that died while holding its key can be repeated once its lock expires. Without the `postgres` module keys are ignored, and a required key stops the start.

The description marks messages with `additionalProperties: false`: that is the contract for clients, while the gateway is more forgiving and ignores unknown fields.

| Variable | Default | |
|---|---|---|
| `API_HTTP_ADDR` | `:8080` | REST gateway |
| `API_GRPC_ADDR` | `127.0.0.1:9091` | gRPC |
| `API_CORS_ORIGINS` | none | allowed browser origins, `*` for any |
| `API_DOCS` | `true` | `/openapi.yaml` and `/docs` |
| `API_REFLECTION` | `false` | gRPC reflection |
| `API_MAX_RECV_MB`, `API_MAX_SEND_MB` | 16, 32 | message size limits; the request limit also caps HTTP bodies |
| `API_RATE_RPS`, `API_RATE_BURST` | 50, 100 | per client; `0` turns the limit off |
| `API_PUBLIC_RATE_RPS`, `API_PUBLIC_RATE_BURST` | 30, 60 | per client, public methods |
| `API_IDEMPOTENCY_RETENTION`, `API_IDEMPOTENCY_LOCK` | `24h`, `1m` | |
| `API_ACCESS_LOG`, `API_LOG_BODIES`, `API_SECURITY_HEADERS` | `true` | |
| `API_TRUSTED_PROXIES` | loopback and private networks | whose `X-Forwarded-For` names the client; a request from elsewhere keeps its connection address, so an allowlist by address cannot be bypassed with a forged header. `none` ignores the header |

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

The module creates its own table (`SETTINGS_TABLE`, `platform_settings` by default), loads the overrides during startup, refreshes them every `SETTINGS_REFRESH_INTERVAL` and reports through `/health` when it can no longer read them. The levels of settings and the generated API are described in [settings.md](settings.md).

## Process roles

One binary, split by `APP_ROLE`, the way taply runs API and worker instances:

| `APP_ROLE` | What runs |
|---|---|
| `all` (default) | everything |
| `api` | the API and the admin panel; River only inserts jobs |
| `worker` | River works jobs and runs periodic jobs; the API and the admin panel are not served |

Every role runs the same `wireDomain`: services, pages and workers are registered everywhere, and each module decides what to start. `/health` and `/metrics` are served in every role.

**Tracing** follows taply: with `OTEL_EXPORTER_OTLP_ENDPOINT` set, spans go over OTLP gRPC to a collector — the one of the `monitoring` module or of an external agent; W3C `traceparent` and baggage carry the trace in and out. The gRPC server and the HTTP side are instrumented and the gateway passes the trace to gRPC, so a REST call is one trace; log lines written with a context carry `trace_id` and `span_id`. Without the variable nothing is exported, yet an incoming trace still continues. Sampling and the exporter follow the standard `OTEL_*` variables.

Other platform variables, read when the code does not set them: `OPS_ADDR` (`:9090`), `SHUTDOWN_TIMEOUT` (`20s`), `LOG_LEVEL` (`info`), `LOG_FORMAT` (`json` or `text`). A bad value stops the start, listed together with every other bad variable.

## Dependencies between modules

`river` requires `postgres`, `admin` requires `settings` for the settings pages, `settings` requires `postgres`. `platformgo plan` reports missing modules and offers to add them.

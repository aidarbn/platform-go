// Package registry is the catalogue of modules platformgo knows how to wire in.
//
// For every module it holds exactly what the generator needs: package, settings type,
// dependencies and environment variables. That is why enabling a module needs no code
// changes: the generator assembles the wiring from this description.
package registry

import (
	"fmt"
	"slices"
	"strings"
)

// EnvVar describes an environment variable of a module for .env.example.
type EnvVar struct {
	Key      string
	Example  string
	Comment  string
	Required bool
}

// Tool is a code generator a module needs in the project. It is pinned in the go.mod of
// the project, so every developer and CI run generate with the same version.
type Tool struct {
	Module  string // module path, "github.com/sqlc-dev/sqlc"
	Package string // what go tool runs, "github.com/sqlc-dev/sqlc/cmd/sqlc"
	Version string
}

// Module describes a platform module.
type Module struct {
	Name     string   // section name in platformgo.yaml
	Requires []string // modules it cannot work without
	Import   string   // import path of the module package; empty for a module without runtime code
	Package  string   // package name in code
	Field    string   // field name in the project's Config struct
	Env      []EnvVar

	// ProjectPkg is a package generated inside the project that the module needs at
	// startup, given relative to the project module path. Empty for modules that need
	// nothing from the project.
	ProjectPkg   string
	ProjectAlias string   // name of that import in generated code
	ExtraArgs    []string // arguments passed to New after the settings

	// Compose is the local development service of the module in docker-compose.yml,
	// indented as an entry under services. Volumes lists the named volumes it uses.
	Compose string
	Volumes []string

	Tools []Tool

	Options []Option // what the module section of platformgo.yaml may contain
}

// Runtime reports whether the module runs inside the service. A module without runtime
// code only generates files, like the monitoring stack, and stays out of the wiring.
func (m Module) Runtime() bool { return m.Import != "" }

// ConfigType is the settings type of the module, for example postgres.Config.
func (m Module) ConfigType() string { return m.Package + ".Config" }

// LoadCall is the call that reads settings from the environment.
func (m Module) LoadCall() string { return m.Package + ".Load(l)" }

// NewCall builds the module from the project's Config struct.
func (m Module) NewCall() string {
	args := append([]string{"cfg." + m.Field}, m.ExtraArgs...)
	return fmt.Sprintf("%s.New(%s)", m.Package, strings.Join(args, ", "))
}

var all = []Module{
	{
		Name:    "postgres",
		Import:  "github.com/aidarbn/platform-go/kit/modules/postgres",
		Package: "postgres",
		Field:   "Postgres",
		Env: []EnvVar{
			{Key: "DATABASE_URL", Example: "postgres://app:app@localhost:5432/app?sslmode=disable", Comment: "database address", Required: true},
			{Key: "DATABASE_MAX_CONNS", Example: "10", Comment: "connection limit"},
			{Key: "DATABASE_MIN_CONNS", Example: "0", Comment: "connections kept open"},
			{Key: "DATABASE_MIGRATE", Example: "true", Comment: "apply migrations on start"},
		},
		ProjectPkg:   "db/migrations",
		ProjectAlias: "migrations",
		ExtraArgs:    []string{"postgres.WithMigrations(migrations.FS)"},
		Options:      []Option{sqlcOption, jetOption, composeOption},
		Compose: `  postgres:
    image: postgres:18-alpine
    environment:
      POSTGRES_USER: app
      POSTGRES_PASSWORD: app
      POSTGRES_DB: app
    ports:
      - "127.0.0.1:5432:5432"
    volumes:
      - postgres-data:/var/lib/postgresql
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U app -d app"]
      interval: 2s
      timeout: 5s
      retries: 30`,
		Volumes: []string{"postgres-data"},
		Tools: []Tool{
			{Module: "github.com/sqlc-dev/sqlc", Package: "github.com/sqlc-dev/sqlc/cmd/sqlc", Version: "v1.31.1"},
			{Module: "github.com/go-jet/jet/v2", Package: "github.com/go-jet/jet/v2/cmd/jet", Version: "v2.16.0"},
		},
	},
	{
		Name:         "api",
		Import:       "github.com/aidarbn/platform-go/kit/modules/api",
		Package:      "api",
		Field:        "API",
		ProjectPkg:   "api/openapi",
		ProjectAlias: "openapi",
		ExtraArgs:    []string{"api.WithOpenAPI(openapi.FS)"},
		Options:      apiOptions,
		Env: []EnvVar{
			{Key: "API_HTTP_ADDR", Example: ":8080", Comment: "REST gateway address"},
			{Key: "API_GRPC_ADDR", Example: "127.0.0.1:9091", Comment: "gRPC address"},
			{Key: "API_CORS_ORIGINS", Example: "http://localhost:3000", Comment: "origins allowed to call the REST API from a browser, comma separated"},
			{Key: "API_CORS_HEADERS", Example: "", Comment: "request headers a browser may send on top of the platform's, comma separated"},
			{Key: "API_CORS_EXPOSE", Example: "", Comment: "response headers a page may read on top of X-Request-ID, comma separated"},
			{Key: "API_CORS_CREDENTIALS", Example: "true", Comment: "allow cookies on CORS calls; false with * answers a literal *"},
			{Key: "API_CORS_MAX_AGE", Example: "86400", Comment: "seconds a browser may cache a preflight"},
			{Key: "API_DOCS", Example: "true", Comment: "serve /openapi.yaml and /docs"},
			{Key: "API_REFLECTION", Example: "false", Comment: "gRPC server reflection"},
			{Key: "API_MAX_RECV_MB", Example: "16", Comment: "largest request"},
			{Key: "API_MAX_SEND_MB", Example: "32", Comment: "largest response"},
			{Key: "API_RATE_RPS", Example: "50", Comment: "calls per second per client; 0 turns the rate limit off"},
			{Key: "API_RATE_BURST", Example: "100", Comment: "burst per client"},
			{Key: "API_PUBLIC_RATE_RPS", Example: "30", Comment: "calls per second per client for public methods"},
			{Key: "API_PUBLIC_RATE_BURST", Example: "60", Comment: "burst per client for public methods"},
			{Key: "API_PRINCIPAL_RATE_RPS", Example: "50", Comment: "calls per second per principal named by api.RateLimitKey; 0 turns it off"},
			{Key: "API_PRINCIPAL_RATE_BURST", Example: "100", Comment: "burst per principal"},
			{Key: "API_RATE_IPV6_PREFIX", Example: "64", Comment: "leading bits of an IPv6 address that make one client of the rate limit; 128 limits every address on its own"},
			{Key: "API_IDEMPOTENCY_RETENTION", Example: "24h", Comment: "how long an Idempotency-Key call is remembered"},
			{Key: "API_IDEMPOTENCY_LEASE", Example: "2m", Comment: "lease of a running Idempotency-Key call, extended while it runs; API_IDEMPOTENCY_LOCK is its old name"},
			{Key: "API_ACCESS_LOG", Example: "true", Comment: "log every HTTP request"},
			{Key: "API_LOG_BODIES", Example: "true", Comment: "bodies of failed requests in the access log"},
			{Key: "API_LOG_BODIES_MASK_PATHS", Example: "", Comment: "path parts whose request bodies never reach the access log, comma separated"},
			{Key: "API_METRICS_TENANT_HEADER", Example: "", Comment: "request header whose value labels the call metrics as tenant_name; empty leaves the label out"},
			{Key: "API_SECURITY_HEADERS", Example: "true", Comment: "security response headers"},
			{Key: "API_HTTP_STOP_TIMEOUT", Example: "", Comment: "on shutdown, how long running REST calls may finish; empty waits as long as SHUTDOWN_TIMEOUT allows"},
			{Key: "API_GRPC_STOP_TIMEOUT", Example: "", Comment: "then how long running gRPC calls may finish; empty waits as long as SHUTDOWN_TIMEOUT allows"},
			{Key: "API_TRUSTED_PROXIES", Example: "", Comment: "networks whose X-Forwarded-For is believed, and, when set, gRPC peers that may name the client; empty means loopback and private networks for HTTP only, none ignores the header"},
		},
		Tools: []Tool{
			{Module: "github.com/bufbuild/buf", Package: "github.com/bufbuild/buf/cmd/buf", Version: "v1.73.0"},
			{Module: "google.golang.org/protobuf", Package: "google.golang.org/protobuf/cmd/protoc-gen-go", Version: "v1.36.12"},
			{Module: "google.golang.org/grpc/cmd/protoc-gen-go-grpc", Package: "google.golang.org/grpc/cmd/protoc-gen-go-grpc", Version: "v1.6.2"},
			{Module: "github.com/grpc-ecosystem/grpc-gateway/v2", Package: "github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway", Version: "v2.30.0"},
			{Module: "github.com/sudorandom/protoc-gen-connect-openapi", Package: "github.com/sudorandom/protoc-gen-connect-openapi", Version: "v0.27.2"},
		},
	},
	{
		Name:     "enums",
		Requires: []string{"api"},
		Import:   "github.com/aidarbn/platform-go/kit/modules/enums",
		Package:  "enums",
		Field:    "Enums",
		Options: []Option{
			{Name: "domain", Description: "Go package scanned for go-enum markers, relative to the project root", Default: "internal/domain"},
		},
		ProjectPkg:   "internal/enums",
		ProjectAlias: "enumcatalog",
		ExtraArgs:    []string{"enums.WithCatalog(enumcatalog.Catalog)"},
		Env: []EnvVar{
			{Key: "ENUMS_PATH", Example: "/v1/enums", Comment: "where the enum catalog is served"},
		},
	},
	{
		Name:         "i18n",
		Requires:     []string{"postgres", "api"},
		Import:       "github.com/aidarbn/platform-go/kit/modules/i18n",
		Package:      "i18n",
		Field:        "I18n",
		ProjectPkg:   "i18n",
		ProjectAlias: "i18nmessages",
		ExtraArgs:    []string{"i18n.WithMessages(i18nmessages.Messages)"},
		Env: []EnvVar{
			{Key: "I18N_DEFAULT_LOCALE", Example: "ru", Comment: "language of the base columns and the fallback"},
			{Key: "I18N_LOCALES", Example: "ru,kk,en", Comment: "languages the API answers in"},
		},
	},
	{
		Name:     "admin",
		Requires: []string{"postgres"},
		Import:   "github.com/aidarbn/platform-go/kit/modules/admin",
		Package:  "admin",
		Field:    "Admin",
		Env: []EnvVar{
			{Key: "ADMIN_ADDR", Example: "127.0.0.1:8081", Comment: "address of the admin panel"},
			{Key: "ADMIN_SESSION_TTL", Example: "12h", Comment: "how long a session lives"},
			{Key: "ADMIN_INSECURE_COOKIES", Example: "false", Comment: "allow the cookie over plain http: local development only"},
			{Key: "ADMIN_BOOTSTRAP_EMAIL", Example: "admin@example.com", Comment: "first account, created while there are none"},
			{Key: "ADMIN_BOOTSTRAP_PASSWORD", Example: "", Comment: "password of the first account"},
			{Key: "ADMIN_LANGUAGE", Example: "en", Comment: "language of the panel: en or ru"},
		},
	},
	{
		Name:     "web",
		Requires: []string{"api"},
		Import:   "github.com/aidarbn/platform-go/kit/modules/web",
		Package:  "web",
		Field:    "Web",
		Env: []EnvVar{
			{Key: "WEB_SECRET_KEY", Example: "", Comment: "seals cookies of the pages, base64 of 32 random bytes: openssl rand -base64 32", Required: true},
			{Key: "WEB_INSECURE_COOKIES", Example: "false", Comment: "cookies over plain http, local development only"},
		},
		Tools: []Tool{
			{Module: "github.com/a-h/templ", Package: "github.com/a-h/templ/cmd/templ", Version: "v0.3.1020"},
		},
	},
	{
		// monitoring runs next to the service, not in it: it generates the observability
		// stack under monitoring/ and has no Go code.
		Name: "monitoring",
		Env: []EnvVar{
			{Key: "OTEL_EXPORTER_OTLP_ENDPOINT", Example: "", Comment: "traces to the collector of the monitoring stack, for example http://127.0.0.1:4317; empty turns tracing off"},
		},
	},
	{
		Name:    "s3",
		Import:  "github.com/aidarbn/platform-go/kit/modules/s3",
		Package: "s3",
		Field:   "S3",
		Env: []EnvVar{
			{Key: "S3_ENDPOINT", Example: "127.0.0.1:9000", Comment: "storage address, host:port", Required: true},
			{Key: "S3_ACCESS_KEY", Example: "minioadmin", Comment: "access key", Required: true},
			{Key: "S3_SECRET_KEY", Example: "minioadmin", Comment: "secret key", Required: true},
			{Key: "S3_USE_SSL", Example: "false", Comment: "https to the storage"},
			{Key: "S3_REGION", Example: "", Comment: "region, when the storage needs one"},
			{Key: "S3_PUBLIC_URL", Example: "", Comment: "where public buckets are served, for example https://example.com/storage"},
		},
		// Pigsty's community build of MinIO: MinIO publishes no images any more, neither to
		// Docker Hub nor, without an account, to quay.io.
		Compose: `  minio:
    image: pgsty/minio:RELEASE.2026-08-04T00-00-00Z
    command: server /data --console-address ":9001"
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: minioadmin
    ports:
      - "127.0.0.1:9000:9000"
      - "127.0.0.1:9001:9001"
    volumes:
      - minio-data:/data
    healthcheck:
      test: ["CMD", "mc", "ready", "local"]
      interval: 2s
      timeout: 5s
      retries: 30`,
		Volumes: []string{"minio-data"},
	},
	{
		Name:         "rbac",
		Requires:     []string{"api"},
		Import:       "github.com/aidarbn/platform-go/kit/modules/rbac",
		Package:      "rbac",
		Field:        "RBAC",
		ProjectPkg:   "rbac",
		ProjectAlias: "rbacpolicy",
		ExtraArgs:    []string{"rbac.WithPolicy(rbacpolicy.Policy)"},
	},
	{
		Name:     "river",
		Requires: []string{"postgres"},
		Import:   "github.com/aidarbn/platform-go/kit/modules/riverx",
		Package:  "riverx",
		Field:    "River",
		Env: []EnvVar{
			{Key: "RIVER_QUEUES", Example: "default=10", Comment: "queues and how many jobs each works at once"},
			{Key: "RIVER_WORK", Example: "true", Comment: "work jobs; false leaves this instance inserting only"},
			{Key: "RIVER_JOB_TIMEOUT", Example: "1m", Comment: "how long one job may run"},
			{Key: "RIVER_COMPLETED_RETENTION", Example: "24h", Comment: "how long completed jobs are kept"},
			{Key: "RIVER_CANCELLED_RETENTION", Example: "24h", Comment: "how long cancelled jobs are kept"},
			{Key: "RIVER_DISCARDED_RETENTION", Example: "168h", Comment: "how long discarded jobs are kept"},
			{Key: "RIVER_STOP_TIMEOUT", Example: "", Comment: "on shutdown, how long running jobs may finish; empty waits as long as SHUTDOWN_TIMEOUT allows"},
			{Key: "RIVER_CANCEL_TIMEOUT", Example: "", Comment: "then how long cancelled jobs get to return; empty waits without a limit"},
		},
	},
	{
		Name:     "settings",
		Requires: []string{"postgres"},
		Import:   "github.com/aidarbn/platform-go/kit/modules/settings",
		Package:  "settings",
		Field:    "Settings",
		Options: []Option{
			{Name: "schema", Description: "business settings schema file, relative to the project root", Default: "settings.yaml"},
		},
		ProjectPkg:   "internal/settings",
		ProjectAlias: "appsettings",
		ExtraArgs:    []string{"appsettings.Schema"},
		Env: []EnvVar{
			{Key: "SETTINGS_TABLE", Example: "platform_settings", Comment: "table holding the overrides"},
			{Key: "SETTINGS_REFRESH_INTERVAL", Example: "15s", Comment: "how often values are re-read"},
		},
	},
}

// All returns every known module in alphabetical order.
func All() []Module {
	out := slices.Clone(all)
	slices.SortFunc(out, func(a, b Module) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Get returns a module by name.
func Get(name string) (Module, bool) {
	for _, m := range all {
		if m.Name == name {
			return m, true
		}
	}
	return Module{}, false
}

// Names returns the names of known modules.
func Names() []string {
	out := make([]string, 0, len(all))
	for _, m := range All() {
		out = append(out, m.Name)
	}
	return out
}

// sqlcOption is the sqlc configuration of the postgres module. The defaults are the
// layout every project had before the option existed.
var sqlcOption = Option{
	Name:        "sqlc",
	Description: "static queries: where sqlc.yaml, the queries and the generated code live, and Go types on top of the platform's",
	Kind:        KindObject,
	Fields: []Option{
		{Name: "dir", Description: "folder of the generated sqlc.yaml, relative to the project root", Default: "db"},
		{Name: "queries", Description: "folder of the *.sql queries, relative to dir", Default: "queries"},
		{Name: "out", Description: "folder of the generated Go code, relative to the project root", Default: "internal/db/sqlcgen"},
		{Name: "package", Description: "Go package name of the generated code", Default: "sqlcgen"},
		{Name: "emit_interface", Description: "generate the Querier interface", Kind: KindBool, Default: true},
		{
			Name:        "overrides",
			Description: "Go types of database types or columns; they come before the platform's, and sqlc takes the first match",
			Kind:        KindList,
			Default:     []any{},
			Items: &Option{Kind: KindObject, Fields: []Option{
				{Name: "db_type", Description: "database type, as sqlc names it: int4, pg_catalog.numeric; or set column"},
				{Name: "column", Description: "column, table.column or schema.table.column; or set db_type"},
				{Name: "nullable", Description: "the override is for nullable columns", Kind: KindBool, Default: false},
				{
					Name:        "go_type",
					Description: "Go type: a name such as int64, or an object",
					Required:    true,
					OneOf: []Option{
						{Kind: KindString},
						{Kind: KindObject, Fields: []Option{
							{Name: "import", Description: "import path of the type"},
							{Name: "package", Description: "package name when it differs from the last element of import"},
							{Name: "type", Description: "type name", Required: true},
							{Name: "pointer", Description: "a pointer to the type", Kind: KindBool, Default: false},
							{Name: "slice", Description: "a slice of the type", Kind: KindBool, Default: false},
						}},
					},
				},
			}},
		},
		{
			Name:        "renames",
			Description: "Go names of columns and tables, sqlc rename: {url: URL}",
			Kind:        KindMap,
			Default:     map[string]any{},
			Values:      &Option{},
		},
	},
}

// jetOption is where the dynamic query builder of the postgres module goes. The default
// is the folder every project had before the option existed.
var jetOption = Option{
	Name:        "jet",
	Description: "dynamic queries: where jet writes the query builder",
	Kind:        KindObject,
	Fields: []Option{
		{Name: "out", Description: "folder of the generated Go code, relative to the project root", Default: "internal/db/jetgen"},
	},
}

// composeOption is the local database of the postgres module: its service in
// docker-compose.yml and the DATABASE_URL example in .env.example. The defaults are the
// service every project had before the option existed.
var composeOption = Option{
	Name:        "compose",
	Description: "the local database: its service in docker-compose.yml and DATABASE_URL in .env.example",
	Kind:        KindObject,
	Fields: []Option{
		{Name: "enabled", Description: "generate the service; false leaves it out for a database service of the project's own in docker-compose.override.yml", Kind: KindBool, Default: true},
		{Name: "image", Description: "Docker image of the database", Default: "postgres:18-alpine"},
		{Name: "db", Description: "name of the database", Default: "app"},
		{Name: "user", Description: "user of the database", Default: "app"},
		{Name: "password", Description: "password of the user", Default: "app"},
		{Name: "port", Description: "port on 127.0.0.1 the database listens on", Kind: KindInt, Default: 5432},
		{Name: "data_dir", Description: "folder of the data inside the container, where the postgres-data volume goes: /var/lib/postgresql/data for images before 18", Default: "/var/lib/postgresql"},
		{
			Name:        "command",
			Description: "arguments of the postgres server: [-c, max_connections=300]",
			Kind:        KindList,
			Default:     []any{},
			Items:       &Option{},
		},
		{Name: "init", Description: "a folder or a .sql or .sh file, relative to the project root, mounted into /docker-entrypoint-initdb.d: run once on an empty database"},
	},
}

// apiOptions are where the proto files of the api module live, buf settings on top of the
// platform's and the generator of the OpenAPI description. The defaults are what every
// project had before the options existed.
var apiOptions = []Option{
	{Name: "proto_dir", Description: "folder of the proto files, the buf module, relative to the project root", Default: "proto"},
	{
		Name:        "buf",
		Description: "buf.yaml on top of the platform's",
		Kind:        KindObject,
		Fields: []Option{
			{
				Name:        "deps",
				Description: "buf modules the proto files import, added to the platform's: buf.build/grpc-ecosystem/grpc-gateway",
				Kind:        KindList,
				Default:     []any{},
				Items:       &Option{},
			},
			{
				Name:        "breaking_except",
				Description: "breaking rules left out of FILE: FILE_NO_DELETE, RPC_NO_DELETE, MESSAGE_NO_DELETE, FIELD_NO_DELETE",
				Kind:        KindList,
				Default:     []any{},
				Items:       &Option{},
			},
		},
	},
	{
		Name:        "openapi",
		Description: "the OpenAPI description of the API",
		Kind:        KindObject,
		Fields: []Option{
			{
				Name:        "generator",
				Description: "platform: the platform's own generator describes the API as the gateway serves it, split by proto file with a bundle; connect: protoc-gen-connect-openapi writes one api/openapi/openapi.yaml, as before v0.10.0; none: the project keeps api/openapi/openapi.yaml itself",
				Enum:        []string{"platform", "connect", "none"},
				Default:     "platform",
			},
			{
				Name:        "documents",
				Description: "descriptions for one audience each, generator platform only: openapi.<name>.bundle.yaml with the operations selected and the components they use, served at /openapi/<name>.yaml",
				Kind:        KindList,
				Default:     []any{},
				Items: &Option{Kind: KindObject, Fields: []Option{
					{Name: "name", Description: "the name in the file and the URL: lower case letters, digits, - and _; all is the whole description", Required: true},
					{Name: "title", Description: "info.title of the document; the base's when empty"},
					{Name: "description", Description: "info.description of the document; the base's when empty"},
					documentSelector("include", "operations that go in: an operation matches a listed group, tag or service; everything when empty"),
					documentSelector("exclude", "operations that stay out of the included ones, matched the same way"),
				}},
			},
		},
	},
}

// documentSelector picks operations for a document of the OpenAPI description.
func documentSelector(name, description string) Option {
	list := func(name, description string) Option {
		return Option{Name: name, Description: description, Kind: KindList, Default: []any{}, Items: &Option{}}
	}
	return Option{Name: name, Description: description, Kind: KindObject, Fields: []Option{
		list("tag_groups", "groups of x-tagGroups, as the description names them, base.yaml's included"),
		list("tags", "sections, tags of operations"),
		list("services", "gRPC services by full name, package.Service"),
	}}
}

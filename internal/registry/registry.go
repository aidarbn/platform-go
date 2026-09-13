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
	Import   string   // import path of the module package
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
}

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
		Env: []EnvVar{
			{Key: "API_HTTP_ADDR", Example: ":8080", Comment: "REST gateway address"},
			{Key: "API_GRPC_ADDR", Example: "127.0.0.1:9091", Comment: "gRPC address"},
			{Key: "API_CORS_ORIGINS", Example: "http://localhost:3000", Comment: "origins allowed to call the REST API from a browser, comma separated"},
			{Key: "API_DOCS", Example: "true", Comment: "serve /openapi.yaml and /docs"},
			{Key: "API_REFLECTION", Example: "false", Comment: "gRPC server reflection"},
			{Key: "API_MAX_RECV_MB", Example: "16", Comment: "largest request"},
			{Key: "API_MAX_SEND_MB", Example: "32", Comment: "largest response"},
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
		},
	},
	{
		Name:         "settings",
		Requires:     []string{"postgres"},
		Import:       "github.com/aidarbn/platform-go/kit/modules/settings",
		Package:      "settings",
		Field:        "Settings",
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

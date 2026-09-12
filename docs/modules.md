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

It runs together with the application, inside the same binary.

```yaml
modules:
  admin:
    addr: :8081      # own port; "" serves it on the shared port under admin.<domain>
    auth: session    # session | basic | none (none listens on 127.0.0.1 only)
    totp: true
```

Out of the box: business settings pages generated from `settings.yaml`, River queues and jobs, module and migration state, audit log, users and roles. Templates and static files are embedded into the binary and create no files in the project.

Project pages are added from project code:

```go
admin.AddPage(app, admin.Page{
	Title:   "Orders",
	Path:    "/orders",
	Roles:   []string{"support", "admin"},
	Handler: adminui.NewOrders(orders),
})
```

Removing the admin UI means deleting the section and running `apply`; settings are then edited with `platformgo settings set`.

## Process roles

One binary, different sets of modules:

| Start | What runs |
|---|---|
| default | every declared module |
| `--role=worker` | queues and what they need; no API, no admin UI |
| `--role=api` | API and admin UI; no workers |

## Dependencies between modules

`river` requires `postgres`, `admin` requires `settings` for the settings pages, `settings` requires `postgres`. `platformgo plan` reports missing modules and offers to add them.

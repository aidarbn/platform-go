# platformgo.yaml

`platformgo.yaml` in the project root is the single source of truth about how the project is put together: which modules are enabled and how they are configured technically, extra generation, linter rules, CI. The project changes like this: edit the file, run `platformgo plan`, then `platformgo apply`. There are no `add` or `remove` commands.

Business settings do not live here — see [settings.md](settings.md). The list of modules is in [modules.md](modules.md).

## Files

| File | Written by | Content | In git |
|---|---|---|---|
| `platformgo.yaml` | a human | what the project needs | yes |
| `platformgo.lock` | `platformgo` | what has actually been applied: module and library versions, hashes of managed files | yes |

Same split as `go.mod` and `go.sum`: intent in one file, the recorded result in the other.

## Example

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/aidarbn/platform-go/v0.2.0/schema/platformgo.schema.json
schema: 1
platform: v0.2.0

project:
  module: github.com/aidarbn/shop-api
  service: shop-api
  go: "1.27"

modules:
  postgres:
    migrations: db/migrations
    queries: sqlc

  river:
    ui: true

  settings:
    schema: settings.yaml
    storage: postgres

  admin:
    addr: :8081
    auth: session
    totp: true

  api:
    grpc_addr: 127.0.0.1:9090
    rest_prefix: /api/v1
    proto: proto
    openapi:
      base: api/base.openapi.yaml
      out: api/shop.openapi.yaml

  s3: {}

generate:
  extra:
    - go run ./tools/mygen

lint:
  depguard:
    - deny: github.com/riverqueue/river
      except: [internal/adapters/dbqueue/**]

ci:
  e2e: false
  security_scan: true
```

## Rules

- **No secrets in the file.** Only structure and settings; values come from environment variables. `.env.example` is generated from the file.
- **A JSON schema** is published for every platform version; the link on the first line turns on completion and validation in editors.
- **`schema`** is the file format version. `platformgo upgrade` migrates the file to a newer schema, see [upgrades.md](upgrades.md).
- **A module is enabled** by adding its section under `modules` and disabled by removing it. Owned files stay behind when a module is removed, and the platform says so.
- **Technical settings only.** Paths, addresses, generation tools, optional services. Schedules, limits, timeouts, flags, retry counts, queue and bucket names are business settings and project code, see [settings.md](settings.md).
- **The platform installs dependencies**: required modules (river needs postgres, and `plan` says so), Go modules and Go tools at pinned versions, local infrastructure services in `docker-compose.yml`. System programs such as Docker are checked by `platformgo doctor`.

## Commands

| Command | What it does |
|---|---|
| `platformgo new <name>` | creates a project with `platformgo.yaml` and the core |
| `platformgo plan` | shows how the project differs from `platformgo.yaml`; changes nothing |
| `platformgo apply` | brings the project in line: dependencies, managed files, scaffolds, generation, `go build`, `go test`; updates `platformgo.lock` |
| `platformgo generate` | generation only; `--check` fails in CI when something is stale |
| `platformgo verify` | for CI: the project matches the file, managed files are untouched, generation is fresh |
| `platformgo upgrade` | moves the project to a new platform version |
| `platformgo doctor` | checks tools and system dependencies; `--fix` offers to install them |
| `platformgo setup` | shell completion and the `pgo` alias |

## Example plan

```
$ platformgo plan
+ module s3
    go get github.com/minio/minio-go/v7@v7.0.69
    docker-compose.yml: minio service
    config.gen.go: S3Config; .env.example: S3_*
~ module river
    ui enabled — docker-compose.yml: riverui service
- module keycloak
    remove from modules.gen.go, docker-compose.yml, linter rules
    owned files stay: internal/adapters/out/keycloak/…
```

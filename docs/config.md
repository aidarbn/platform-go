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
  postgres: {}

  river:
    ui: true

  settings:
    schema: settings.yaml

  admin: {}

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
| `platformgo new <module-path>` | creates a project with `platformgo.yaml`, the core files and the lock |
| `platformgo plan` | shows how the project differs from `platformgo.yaml`; changes nothing |
| `platformgo apply` | writes the generated files, creates the files an enabled module needs (such as `settings.yaml`), deletes the generated files of removed modules, updates `platformgo.lock` and runs `go mod tidy` |
| `platformgo generate` | the same without `go mod tidy`; `--check` fails in CI when anything is stale |
| `platformgo verify` | for CI: the project matches the file, generation is fresh, nothing is left over |
| `platformgo lint` | every check of the project, as in taply; the proto checks run only with the `api` module, so the Makefile stays the same when modules change |
| `platformgo doctor` | checks tools and system dependencies |

Planned: `upgrade` (moving to a new platform version), `doctor --fix`, `setup`.

A generated file of a removed module is deleted only while it still carries the generated mark on its first line. A file taken over by hand stays, and `plan` says so. Files that belong to the project — `settings.yaml`, `main.go`, `wire.go`, the Makefile — are never deleted.

## Example plan

```
$ platformgo plan
service  shop-api
modules  admin, postgres
  - module settings
~ cmd/app/config.gen.go
~ cmd/app/modules.gen.go
~ .env.example
- internal/settings/settings.gen.go
~ platformgo.lock
```

# platformgo.yaml

`platformgo.yaml` in the project root is the single source of truth about how the project is put together: which modules are enabled and how they are configured technically, extra generation, linter rules, CI. The project changes like this: edit the file, run `platformgo plan`, then `platformgo apply`. There are no `add` or `remove` commands.

Business settings do not live here — see [settings.md](settings.md). The list of modules is in [modules.md](modules.md).

## Files

| File | Written by | Content | In git |
|---|---|---|---|
| `platformgo.yaml` | a human | what the project needs | yes |
| `platformgo.lock` | `platformgo` | what has been applied: enabled modules, generated files, tools added to `go.mod` | yes |

Same split as `go.mod` and `go.sum`: intent in one file, the recorded result in the other.

## Example

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/aidarbn/platform-go/v0.2.1/schema/platformgo.schema.json
schema: 1
platform: v0.2.1

project:
  module: github.com/aidarbn/shop-api
  service: shop-api
  go: "1.27"

modules:
  postgres: {}
  settings:
    schema: settings.yaml   # the default
  admin: {}
  api: {}
  river: {}
  s3: {}
```

## Rules

- **Only what the platform knows is accepted.** An unknown field, module or module option is an error that names the known ones: a typo fails `plan` instead of being ignored.
- **A JSON schema** is published for every platform version (`schema/platformgo.schema.json`, also printed by `platformgo schema`); the first line of a new project points editors at it, which turns on completion and validation.
- **No secrets in the file.** Only structure and technical options; values come from environment variables, and `.env.example` is generated from the enabled modules.
- **A module is enabled** by adding its section under `modules` and removed by deleting it. `apply` removes what generation produced for it and the tools it added to `go.mod`; files that belong to the project stay.
- **Technical options only.** Paths and switches of the tooling. Schedules, limits, timeouts, flags and retry counts are business settings, see [settings.md](settings.md); addresses, secrets and capacity are environment variables.
- **Dependencies between modules are checked**: `settings`, `admin` and `river` require `postgres`, `rbac` requires `api`, `i18n` requires `postgres` and `api`, `enums` requires `api`, and `plan` says so.

| Module | Options |
|---|---|
| `postgres`, `admin`, `api`, `i18n`, `rbac`, `river`, `s3` | none: everything else is environment variables or project files, see [modules.md](modules.md) |
| `settings` | `schema` — the business settings schema file, `settings.yaml` by default |
| `enums` | `domain` — the Go package scanned for go-enum markers, `internal/domain` by default |

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

`platformgo upgrade [version]` moves the project to a platform version. Planned: `doctor --fix`, `setup`.

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

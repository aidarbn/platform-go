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

## The environment of the project's own code

Modules document their variables in the generated `.env.example`. Variables of the project's own code — keys, provider addresses — go into `.env.project.example`, which belongs to the project: `platformgo generate` appends it to `.env.example` under `# project`, so one file describes the whole environment and `make run` reads it.

## Rules

- **Only what the platform knows is accepted.** An unknown field, module or module option is an error that names the known ones, and an option of the wrong type is an error with its line: a typo fails `plan` instead of being ignored. See [Module options](#module-options).
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

## Module options

Every option of a module has a type, declared once in the module registry. Today `settings` and `enums` have one string option each (the table above); the other types are there for the options that follow, and the examples below show their shape. The same declaration checks `platformgo.yaml` and builds the JSON schema, so the editor and `plan` never disagree.

| Type | In YAML | Example |
|---|---|---|
| string | a scalar the YAML reads as a string; quote numbers and versions | `schema: settings.yaml`, `go_type: "1.5"` |
| string with allowed values | one of the listed strings | `generator: connect` |
| bool | `true` or `false`, unquoted | `emit_interface: false` |
| integer | an unquoted whole number | `workers: 4` |
| list | a sequence of values of one type | `deps: [buf.build/grpc-ecosystem/grpc-gateway]` |
| object | a mapping with the declared keys only; some are required | `openapi: {generator: none}` |
| map | a mapping with any keys, values of one type | `renames: {url: URL}` |
| one of | a value of one of several shapes, told apart by the shape | `go_type: int64` or `go_type: {type: int64, pointer: true}` |

An option left out takes its default, which the schema shows. An empty value (`schema:` with nothing after it) is an error, not the default: delete the key instead. YAML anchors and aliases work; merge keys (`<<`) are not supported in module sections.

Errors say where and what:

```
platformgo.yaml: line 10: module enums: option domain must be a string, got a list
line 11: module enums: unknown option "domian", known options: domain
line 8: module settings: option schema must be a string, got a number 1.5: quote it, "1.5"
```

Code that generates files reads the options of a module into a typed struct with `spec.File.DecodeOptions`, strictly: a struct that misses an option the registry declares fails in tests.

## Schema version

`schema: 1` is the format of the file. Adding an option, a type or a module does not change it: a file that was valid stays valid and means the same, and an older `platformgo` rejects the new key by name. The number goes up only when a key is renamed, moved, removed or changes meaning. Such a release ships a migration that `platformgo upgrade` applies to the file (see [upgrades.md](upgrades.md)), and `platformgo` refuses a file with a schema newer than it understands.

## Commands

| Command | What it does |
|---|---|
| `platformgo new <module-path>` | creates a project with `platformgo.yaml`, the core files and the lock |
| `platformgo plan` | shows how the project differs from `platformgo.yaml`; changes nothing |
| `platformgo apply` | writes the generated files, creates the files an enabled module needs (such as `settings.yaml`), deletes the generated files of removed modules, updates `platformgo.lock` and runs `go mod tidy` |
| `platformgo generate` | the same without `go mod tidy`; `--check` fails in CI when anything is stale |
| `platformgo verify` | for CI: the project matches the file, generation is fresh, nothing is left over |
| `platformgo lint` | every check of the project, as in taply; the proto checks run only with the `api` module and the migrations check only with `postgres`, so the Makefile stays the same when modules change |
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

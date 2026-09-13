# platform-go

`platformgo` is a command line tool that creates, generates and maintains Go projects.

**Status:** early development.

## Commands

| Command | What it does | State |
|---|---|---|
| `platformgo new <module-path>` | creates a project: wiring, compose, Dockerfile, Makefile, CI | ready |
| `platformgo generate [--check]` | regenerates wiring, `.env.example`, `docker-compose.yml`, typed settings; `--check` fails when stale | ready |
| `platformgo plan` | shows which generated files would change | ready |
| `platformgo doctor` | checks the tools a project needs | ready |
| `platformgo apply` | brings dependencies and managed files in line with `platformgo.yaml` | planned |
| `platformgo verify` | CI check that managed files are untouched | planned |
| `platformgo upgrade` | moves a project to a new platform version | planned |
| `platformgo setup` | shell completion and the short `pgo` alias | planned |

A project is described declaratively in `platformgo.yaml` — see [docs/config.md](docs/config.md).

## Modules

| Module | State |
|---|---|
| `postgres` — pool, health, pool metrics, transactions, local database in compose | ready |
| `settings` — business settings from `settings.yaml`, stored in the database, typed access | ready |
| `admin` — admin panel: sign in with one time codes, roles, accounts, audit log, settings pages, project pages | ready |
| migrations and query generation, `river`, `api` and the rest of [docs/modules.md](docs/modules.md) | planned |

## Layout

- **Modules** are declared in `platformgo.yaml` and applied with `platformgo apply` at any time. Their files come in three kinds: `managed` belongs to the platform and is rewritten on upgrade, `owned` is created once and then belongs to the project, `generated` is rebuilt by `generate`.
- **Settings** of the project and its modules live in `platformgo.yaml`; versions and hashes of managed files live in `platformgo.lock`.
- **Libraries** of the platform live in `github.com/aidarbn/platform-go/kit/...`; modules in a project stay a thin layer on top of them.

## Documentation

- [docs/config.md](docs/config.md) — the `platformgo.yaml` format
- [docs/modules.md](docs/modules.md) — module catalogue
- [docs/settings.md](docs/settings.md) — levels of settings
- [docs/upgrades.md](docs/upgrades.md) — how projects stay upgradable

## Installation

Available with the first release:

```bash
go install github.com/aidarbn/platform-go/cmd/platformgo@latest
```

## License

Copyright 2026 Aidar Babanov.

The platform code — the `platformgo` tool and the `kit` libraries — is distributed under the [Apache 2.0](LICENSE) license.

Code that `platformgo` creates inside your project (templates, examples, generated files) belongs to your project: use it, change it and distribute it without any conditions, including without keeping license notices.

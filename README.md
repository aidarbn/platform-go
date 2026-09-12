# platform-go

`platformgo` is a command line tool that creates, generates and maintains Go projects.

**Status:** early development.

## Commands

| Command | What it does |
|---|---|
| `platformgo new <module-path>` | creates a project: core, build, tests, linters, CI |
| `platformgo plan` | shows how the project differs from `platformgo.yaml` |
| `platformgo apply` | brings the project in line with `platformgo.yaml` |
| `platformgo generate` | runs every generator of the project |
| `platformgo verify` | check for CI |
| `platformgo doctor` | checks tools and their versions |
| `platformgo upgrade` | moves the project to a new platform version |
| `platformgo setup` | shell completion and the short `pgo` alias |

A project is described declaratively in `platformgo.yaml` — see [docs/config.md](docs/config.md).

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

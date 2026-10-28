# Changelog

Changes by release. What a project or its clients see differently, with how to keep the old behaviour, is in [docs/upgrades.md](docs/upgrades.md#behaviour-changes); manual steps of an upgrade are there too. Releases before v0.11.0 are described only there.

## v0.11.2

Backward compatible: no API of `kit` changes incompatibly. One default changes, listed under Changed: the start check of `s3` and `postgres` is retried for 15 seconds instead of being made once; `S3_STARTUP_TIMEOUT=0` and `DATABASE_STARTUP_TIMEOUT=0` give the old behaviour back. Upgrade with `platformgo upgrade v0.11.2`; nothing to do in a project, but see Fixed for a `.env` that still sets `RIVER_QUEUES=default=10`.

### Added

- `s3`: `S3_OPTIONAL` (`Config.Optional`) — start without the storage: one check, a warning in the log, `/health` reports s3 as degraded with 200 while it is unreachable, file operations return the storage's errors. Off by default. See [the s3 module](docs/modules.md#the-s3-module).
- `s3`: `S3_STARTUP_TIMEOUT` (`Config.StartupTimeout`) and `postgres`: `DATABASE_STARTUP_TIMEOUT` (`Config.StartupTimeout`) — how long the start retries a storage or a database that does not answer.
- `platform`: `platform.Degraded` and `platform.IsDegraded` — a health check failure of a part the instance can work without; `/health` answers 200 with `"status": "degraded"` unless another check fails.

### Changed

- `s3`, `postgres`: the start check is retried for 15 seconds by default — each attempt as long as before (5s for the storage, `DATABASE_CONNECT_TIMEOUT` for the database), pauses from 250ms to 2s, a warning for each failed attempt — instead of one attempt and exit. A replica rolled out while MinIO or Postgres restarts now waits for it rather than exiting and being restarted, as one did on the dev stand of taply. A refusal of the service itself (wrong keys, a wrong password, no such database, a malformed URL) is not retried and stops the start at once, as before.
  Why retries by default and not the old behaviour byte for byte: the old check turned a few seconds of a restarting dependency into a crash and a restart of the service, which is what the fix is for; with retries a misconfiguration still fails at once, and a dependency that stays down still stops the start, within the same order of time (15–20s instead of 5s) — well inside the start budget of compose and Kubernetes probes, since `/health` is not served until the modules are up anyway. The storage is not made optional by default: a service that needs its files should not run without them unnoticed. The old behaviour: `S3_STARTUP_TIMEOUT=0`, `DATABASE_STARTUP_TIMEOUT=0`; a `Config` built by hand rather than by `Load` keeps it as well. See [behaviour changes](docs/upgrades.md#v0112).

### Fixed

- Generator: `.env.example` has `RIVER_QUEUES=` empty, with a comment on the order, instead of `RIVER_QUEUES=default=10`. A value there won over `riverx.QueuesFrom`, and `make run` reads `.env.example` when there is no `.env`, so a project sizing its queues from its settings ran `default=10`. The upgrade regenerates `.env.example`; a `.env` or a deploy environment copied from an older example still sets `RIVER_QUEUES=default=10` and still wins — empty or remove it there. The order of the sources is in [queue sizes](docs/modules.md#queue-sizes).

## v0.11.1

Backward compatible: one new option, off by default; without it the platform behaves as v0.11.0 byte for byte. One fix raises a dependency. Upgrade with `platformgo upgrade v0.11.1`; nothing to do in a project.

### Added

- `i18n`: `i18n.WithExternalErrors()` and `(*i18nx.Client).TranslateExternal` — translate the failures of external providers (`EXTERNAL_TIMEOUT`, `EXTERNAL_UNAVAILABLE` under a code other than `Unavailable`) when the dictionary has an entry for their text, as taply translates `zebra: external service timeout` for the guest at a tablet; the code and the `ErrorInfo` stay. See [messages of external providers](docs/modules.md#external-provider-messages).

### Fixed

- `golang.org/x/net` v0.62.0, which fixes GO-2026-6860 (`html.Parse`); with v0.61.0 the upgrade left a project's `govulncheck` red. Projects get this minimum version with the upgrade.

## v0.11.0

Backward compatible: no API of `kit` changes incompatibly, and every new behaviour is an option, off by default. Two defaults change, listed under Changed: the capital of one routing error text no client sees today, and the Go toolchain. The fix of idempotency changes what a late repeat gets, listed under Fixed. Upgrade with `platformgo upgrade v0.11.0`; nothing to do in a project.

### Added

- `rbac`: any casbin model and policy store. `WithModel` (role inheritance, domains, matchers of the project's own), `WithAdapter` (any casbin adapter, Postgres for one) with `Enforcer.Reload` and `WithReloadEvery` to take a new policy without a restart, safe while calls are checked; `WithFunction` for functions of the matcher; `WithRequest` and `ErrUnauthenticated` for how a call becomes the requests to enforce; `WithPublic` for public methods of another request; `Configure` to give the options from the `main.go` of a generated project; `Enforcer.Check`, `Enforcer.Casbin` (the `*casbin.SyncedEnforcer`) and `NewEnforcerWith`; the metric `rbac_policy_reloads_total{result}` with an adapter. See [the rbac module](docs/modules.md#any-casbin-model).
- `postgres`: the local database of `docker-compose.yml` and the `DATABASE_URL` of `.env.example` are an option, `compose` — image, database, user, password, port, data folder, server arguments, an init script, or no service at all. See [the local database](docs/modules.md#the-local-database).
- `postgres`: the folder of the jet query builder is an option, `jet.out`; `internal/db/jetgen` by default.
- `api`: `api.IdempotencyMessages` — texts of the errors about a missing, too long or unprintable `Idempotency-Key` of the project's own (taply's: `Idempotency-Key is required in header`, `Idempotency-Key is too long`).
- `api`: `API_CORS_PREFLIGHT_NO_CONTENT` (`Config.CORSPreflightNoContent`) — an `OPTIONS` request the CORS policy does not answer gets 204 without CORS headers, as in taply, instead of the gateway's 404 or 405.
- `api`: `api.MaxRecvSize(app, bytes)` — the request size limit from code, over `API_MAX_RECV_MB`.
- `i18n`: `i18n.WithErrorPhase(api.Phase)` and `i18n.Configure` — translate error messages only from a chosen phase, `api.PhaseAfterValidate` as in taply, so refusals of authentication, guards and validation keep their text.

### Changed

- `api`: the routing error that is neither 400, 404 nor 405 says `Unexpected routing error`, with a capital, as grpc-gateway and taply do; grpc-gateway does not produce such a status today.
- The platform builds with Go toolchain go1.27.3, which fixes vulnerabilities of the standard library in go1.27.1, and `golang.org/x/tools` v0.52.0 with the `golang.org/x` modules it needs; projects get these minimum versions with the upgrade.

### Fixed

- `api`: an idempotency outcome past its retention is no longer replayed while its record waits for the hourly cleanup; the repeat runs the call again, a racing repeat answers `Aborted` while it runs.
- docs: the example `rbac/policy.csv` had comments after its rules, which broke the start.

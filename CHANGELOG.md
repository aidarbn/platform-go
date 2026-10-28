# Changelog

Changes by release. What a project or its clients see differently, with how to keep the old behaviour, is in [docs/upgrades.md](docs/upgrades.md#behaviour-changes); manual steps of an upgrade are there too. Releases before v0.11.0 are described only there.

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

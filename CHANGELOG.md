# Changelog

Changes by release. What a project or its clients see differently, with how to keep the old behaviour, is in [docs/upgrades.md](docs/upgrades.md#behaviour-changes); manual steps of an upgrade are there too. Releases before v0.11.0 are described only there.

## v0.11.0

Backward compatible: without the new options everything behaves, fails and is counted as in v0.10.1, and no API of `kit` changes incompatibly. Upgrade with `platformgo upgrade v0.11.0`; nothing to do in a project.

### Added

- `rbac`: any casbin model and policy store. `WithModel` (role inheritance, domains, matchers of the project's own), `WithAdapter` (any casbin adapter, Postgres for one) with `Enforcer.Reload` and `WithReloadEvery` to take a new policy without a restart, safe while calls are checked; `WithFunction` for functions of the matcher; `WithRequest` and `ErrUnauthenticated` for how a call becomes the requests to enforce; `WithPublic` for public methods of another request; `Configure` to give the options from the `main.go` of a generated project; `Enforcer.Check`, `Enforcer.Casbin` (the `*casbin.SyncedEnforcer`) and `NewEnforcerWith`; the metric `rbac_policy_reloads_total{result}` with an adapter. See [the rbac module](docs/modules.md#any-casbin-model).

### Fixed

- `api`: an idempotency outcome past its retention is no longer replayed while its record waits for the hourly cleanup; the repeat runs the call again, a racing repeat answers `Aborted` while it runs.
- docs: the example `rbac/policy.csv` had comments after its rules, which broke the start.

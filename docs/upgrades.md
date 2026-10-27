# Upgradability

> **State:** in place — module code in `kit`, generated files rebuilt by `apply`, the lock that cleans up after removed modules and records the version that applied the project, codemods run by `apply` for every version newer than that, the API compatibility check of `kit` (`scripts/apicheck.sh`, CI job `api`) and the upgrade matrix over the last three releases (`scripts/upgrade-matrix.sh`, CI job `upgrade-matrix`). Config schema migrations are not needed yet: the schema is still 1.

The guarantee rests on three layers: an architecture that leaves almost nothing to upgrade, mechanisms that upgrade the rest, and platform tests that prove an upgrade works before it is released.

## 1. Architecture

| Rule | How it works |
|---|---|
| Module code lives in `kit`, not in the project | most upgrades are a `go get`, with no file merging |
| As few managed files as possible, and they are only rewritten | only what tools must see in the repository is copied: CI, Dockerfile, `buf.gen.yaml`, `platform.mk` |
| Managed files must not be edited by hand | `platformgo verify` in the project CI compares hashes from `platformgo.lock` and fails on a mismatch, so rewriting is always safe |
| Extension points instead of edits | the project `Makefile` includes `platform.mk` and adds its own targets; CI is a shared workflow with inputs; linters and `buf.gen.yaml` are assembled from `platformgo.yaml`, which has `extra` sections |
| Owned code is never touched without a codemod | a scaffold created once belongs to the project; the platform changes it only through automated rewriting |
| Tables a module owns are versioned | `admin`, `settings`, the idempotency table of `api` and the translations of `i18n` bring their tables up with `pgdb.MigrateSchema`: numbered steps per module, the version in `platform_schema_versions`, applied on start under an advisory lock. A release adds a step, never edits one, and a step stays readable by the release before it, so a rollback works on the newer schema |

## 2. What `platformgo upgrade` does

```
$ platformgo upgrade --dry-run
platform    v0.4.2 → v0.6.0 (steps: v0.5.0, v0.6.0)
config      platformgo.yaml: schema 2 → 3
libraries   platform-go/kit v0.4.2 → v0.6.0
managed     rewrite: platform.mk, ci.yml, buf.gen.yaml, .golangci.yml
codemods    river.NewInserter → river.Inserter: 3 places
generate    modules.gen.go, config.gen.go, OpenAPI
checks      go build, go test, platformgo verify
```

| Mechanism | What it is |
|---|---|
| Step by step migrations | every platform version ships a manifest of steps; skipping versions replays them in order |
| Config migrations | `platformgo.yaml` carries a schema number; renames and moves are done by code |
| Codemods | changes to the `kit` API come with rewrites of project code through the Go syntax tree (`golang.org/x/tools/go/analysis` with suggested fixes); simple renames use `//go:fix inline` |
| Pinned tool versions | a platform version pins buf, templ and the other generators, so generated code is identical everywhere |
| One upgrade, one commit | upgrade runs on a branch and lands as a single commit; rollback is `git revert` |

## Automatic upgrades

A project created by the platform has `.github/workflows/platform-upgrade.yml`. Every Monday, and by hand from the Actions tab, it calls the upgrade workflow of the platform (`.github/workflows/upgrade.yml`):

1. `platformgo upgrade latest`; a project already on the latest release ends here;
2. build, vet and `platformgo verify` — the checks that need no database;
3. the result goes to a branch and a pull request against the base branch, a draft when the checks failed; a later release replaces the branch;
4. the CI run of the pull request waits for approval, because the pull request was opened with the workflow token; the workflow approves it, and the checks show up on the pull request. When approval is not allowed, it starts the project CI on the branch through `workflow_dispatch` instead, and the checks land on the commit. With a `token` secret that may start workflows the pull request starts CI by itself.

The calling file belongs to the project, so the runner, the base branch and the naming are the project's:

```yaml
jobs:
  upgrade:
    uses: aidarbn/platform-go/.github/workflows/upgrade.yml@main
    permissions: { contents: write, pull-requests: write, actions: write }
    with:
      runs-on: '["self-hosted", "linux", "x64"]'
      setup-go: false # the Go and the module cache of the runner: actions/setup-go caches slowly there
      base: main
      branch-prefix: chore/GEN-00000-platform-
      title: "chore GEN-00000: platform-go {from} → {to}"
```

A project created before this workflow adds the file by hand, together with `workflow_dispatch:` among the triggers of its CI.

## 3. Platform tests

| Check | What it catches |
|---|---|
| Upgrade matrix | a project with every module the release knew is created by each of the last three releases and moved to the checkout: `apply` by the new version, `verify`, `go build`, `go vet`, `go test` must be clean. One failing release fails CI |
| `kit` API compatibility | `apidiff` against the previous tag. Every run reports the changes; a release tag fails when an incompatible change ships without a new minor version (before v1) or major version (after v1) |
| Reference projects | demo services in the repository, one per typical module set, always on the latest version |
| Codemods | before and after tests: the result matches, keeps comments and a second run changes nothing |

### Writing a codemod

An incompatible change of `kit` that project code can follow mechanically ships with a codemod in `internal/codemod/all.go`, under the version it is released in:

```go
codemod.RenameSymbol("v0.3.0", "github.com/aidarbn/platform-go/kit/modules/riverx", "NewQueue", "Queue"),
```

`apply` runs every codemod newer than the `platform` recorded in `platformgo.lock` and records its own version afterwards, so skipping releases replays the steps in order; `plan` lists the files a codemod would rewrite. Generated files, `vendor` and `testdata` are left alone. Anything beyond a rename is a `Codemod` with its own `Fix`, which edits source ranges through `File.Replace` so comments and layout stay as they were.

## Version policy

| Rule | What it means |
|---|---|
| SemVer | a minor version never breaks projects: changes are covered by migrations and codemods; a major version comes with an upgrade guide |
| Deprecation before removal | a `kit` API is first marked `// Deprecated:` and lives for at least one minor version |
| Support window | upgrades are guaranteed from the last N minor versions; older ones go step by step |
| Release notes | a human `CHANGELOG` and the machine readable manifest of steps are built from one source |

## Behaviour changes

Changes that keep the API but change what a project or its clients see, by the version that brings them: what was, what is, who is affected and how to get the old behaviour back.

### v0.9.0 (unreleased)

- **CORS: `Vary: Origin` on every answer.** Before, the REST API added `Vary: Origin` only to answers for an allowed origin; now every answer carries it while `API_CORS_ORIGINS` is set, unless the origins are `*` with `API_CORS_CREDENTIALS=false` and every caller gets the same literal `*`. A shared cache can no longer hand an answer without CORS headers to a page on an allowed origin. Affects only caches in front of the API, which now keep one copy per origin; nothing to do in a project. The new `API_CORS_HEADERS`, `API_CORS_EXPOSE`, `API_CORS_CREDENTIALS`, `API_CORS_MAX_AGE` and `api.CORSHeaders` are opt-in: without them the allowed headers, credentials and the preflight max age are as before.
- **API: the client address over gRPC comes only from trusted peers.** Before, `api.ClientIP` believed `x-client-ip` metadata from any gRPC peer, so a client reaching `API_GRPC_ADDR` directly named any address and got a fresh rate limit bucket with every call. Now the address keys — the new `x-platform-client-ip` (`api.ClientIPMetadataKey`) and the old `x-client-ip` — count only from loopback, the server's own listen address and networks set explicitly in `API_TRUSTED_PROXIES`; the default of every private network does not count over gRPC. Affects a project with its own gRPC proxy on another host that names the client: list the proxy in `API_TRUSTED_PROXIES`. The old key still works from those peers, with one warning in the log; switch the proxy to `x-platform-client-ip`. The REST gateway of the process is not affected. There is no switch back: it was a way around the rate limit.
- **API: address metadata seen by handlers is the checked address.** Both keys of the incoming gRPC metadata are rewritten to the address the platform settled on before any interceptor of the project. A handler that read `x-client-ip` itself now gets the peer address of a direct untrusted client instead of what it sent; through the gateway it gets the same address as before.
- **API: `X-Forwarded-For` from the last header line, addresses normalised.** Before, the last hop was taken from the first `X-Forwarded-For` line; behind a proxy that adds a line of its own, as HAProxy does, the client's forged line named the client. Now it is the last hop of the last line. A hop that is not an address falls back to the connection address instead of being used as it is, and addresses are normalised: no port, brackets or zone, IPv4 mapped into IPv6 as plain IPv4. Affects allowlists and logs that compared the raw string. Plain routes of `api.HandleHTTP` still read the resolved address from `X-Client-IP`.
- **API: IPv6 clients are rate limited by their /64.** Before, every IPv6 address had its own bucket, so one subscriber could take a new bucket with every address of its block. `API_RATE_IPV6_PREFIX` (`RateLimit.IPv6Prefix` in code, where zero means 64) sets the length; `128` brings back a bucket per address. Affects many clients behind one /64 sharing their limits, rare outside a single subscriber.
- **API: a rate limit burst under one is taken as one.** Before, `API_RATE_BURST=0` or `API_PUBLIC_RATE_BURST=0` with a non zero rate refused every call; now a call passes at the rate. To turn a limit off, set its rate to `0`, as before.
- **API: a rate limit per principal, opt-in.** `api.RateLimitKey` names the user, client or device of a call for a second limit, `API_PRINCIPAL_RATE_RPS`/`API_PRINCIPAL_RATE_BURST` (50/100). Nothing changes until a project calls it.
- **Postgres: numeric as `decimal.Decimal` on every connection.** Every connection `pgdb.Open` and the `postgres` module make registers the codec of `pgx-shopspring-decimal`: `decimal.Decimal` parameters and scan targets — the type the generated sqlc config uses for numeric — travel in the binary format, arrays too, instead of the text fallback through `sql.Scanner`. The values are the same; every other target (`string`, `float64`, `int64`, `pgtype.Numeric`, `[]byte`, `sql.Null*`, slices, `any` and `rows.Values`, which still give `pgtype.Numeric`) and `database/sql` read and write as before. The module brings `github.com/jackc/pgx-shopspring-decimal` into `go.sum`, which `platformgo apply` tidies. Nothing to do in a project. The hooks (`pgdb.Config.Hooks`, `postgres.Apply` with `WithAfterConnect`, `WithPoolConfig`, `WithDefaults`) are opt-in.
- **Shutdown phases, budgets and River settings, opt-in.** `SHUTDOWN_PHASES`, `API_HTTP_STOP_TIMEOUT`, `API_GRPC_STOP_TIMEOUT`, `RIVER_STOP_TIMEOUT`, `RIVER_CANCEL_TIMEOUT`, `riverx.QueuesFrom` and `riverx.MaxAttemptsFrom` change nothing until a project sets or calls them: the modules stop in reverse order within `SHUTDOWN_TIMEOUT` and the queues come from `RIVER_QUEUES` or `default=10`, as before. `platformgo apply` adds the new variables to `.env.example`: `SHUTDOWN_PHASES=false` and the budgets with empty values, which mean unset. Two small differences without them: when running jobs do not finish within the shutdown timeout, River logs the warning `river jobs did not finish in time, cancelling them` before it cancels them, as it did silently before; and the `api` module stops its rate limiters and the idempotency cleanup after the running gRPC calls finish rather than before, so those calls are still limited and recorded. Nothing to do in a project.

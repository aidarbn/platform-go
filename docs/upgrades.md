# Upgradability

> **State:** in place — module code in `kit`, generated files rebuilt by `apply`, the lock that cleans up after removed modules and records the version that applied the project, codemods for Go and `.proto` files run by `apply` for every version newer than that, manual steps that `verify` keeps open until they are done, `upgrade --dry-run` and the checks after an upgrade, the API compatibility check of `kit` with the rule that every incompatible change has a codemod or a manual step (`scripts/apicheck.sh`, CI job `api`) and the upgrade matrix over the last three releases (`scripts/upgrade-matrix.sh`, CI job `upgrade-matrix`). Config schema migrations are not needed yet: the schema is still 1.

The guarantee rests on three layers: an architecture that leaves almost nothing to upgrade, mechanisms that upgrade the rest, and platform tests that prove an upgrade works before it is released.

## 1. Architecture

| Rule | How it works |
|---|---|
| Module code lives in `kit`, not in the project | most upgrades are a `go get`, with no file merging |
| As few managed files as possible, and they are only rewritten | only what tools must see in the repository is copied: CI, Dockerfile, `buf.gen.yaml`, `platform.mk` |
| Managed files must not be edited by hand | `platformgo verify` in the project CI compares hashes from `platformgo.lock` and fails on a mismatch, so rewriting is always safe |
| Extension points instead of edits | the project `Makefile` includes `platform.mk` and adds its own targets; CI is a shared workflow with inputs; linters and `buf.gen.yaml` are assembled from `platformgo.yaml`, which has `extra` sections |
| Owned code is never touched without a codemod | a scaffold created once belongs to the project; the platform changes it only through automated rewriting |
| Tables a module owns are versioned | `admin`, `settings`, the idempotency table of `api`, the translations of `i18n` and the processed ids of `webhookx` bring their tables up with `pgdb.MigrateSchema`: numbered steps per module, the version in `platform_schema_versions`, applied on start under an advisory lock. A release adds a step, never edits one, and a step stays readable by the release before it, so a rollback works on the newer schema |

## 2. What `platformgo upgrade` does

1. warns when `go.mod` is not tidy before the upgrade: `apply` runs `go mod tidy`, and somebody else's leftovers would land in the upgrade commit, so they go into a commit of their own first;
2. `go get github.com/aidarbn/platform-go/cmd/platformgo@<version>` — of the platform tool rather than the module, so `go.sum` gets every module the new tool needs, new ones included — and the version recorded in `platformgo.yaml`;
3. `go tool platformgo apply` by the new version: generated files, pinned tools, codemods, the lock;
4. checks: `go build ./...`, `platformgo verify`, `go test ./...`. A failed check leaves the upgrade in place to be fixed or reverted and prints a warning; the command still succeeds, as it did before the checks existed. `--strict` makes a failed check fail the command, `--no-check` skips the checks, for CI that runs its own.

`--dry-run` does all of it in a temporary copy of the project — `.git` and `node_modules` left out, relative replace directives to folders outside the project made absolute — and prints what would happen, `platformgo plan` of the new version included; the project itself stays as it is. On a project with code on the `kit/adminx` API of v0.5:

```
$ platformgo upgrade --dry-run v0.8.0
platform    v0.5.11 → v0.8.0
config      platformgo.yaml: platform v0.8.0
plan of v0.8.0:
    service  sendkz
    modules  admin, api, postgres, river, settings, web
    ~ internal/adminseed/seed.go: codemod adminx.no-totp
    ~ platformgo.lock
    manual internal/adminseed/seed.go:13: CreateUser returns no secret since v0.6.0: secret is gone, remove its uses (adminx.no-totp)
    manual internal/adminseed/seed.go:20: one time codes are gone in v0.6.0: drop the handling of adminx.ErrCodeRequired (adminx.no-totp)
checks:
  fail  go build: exit status 1
        internal/adminseed/seed.go:18:6: undefined: secret
  fail  platformgo verify: exit status 1
        error: manual steps of a platform upgrade are open: …
  fail  go test: exit status 1
warning: upgrade: v0.5.11 → v0.8.0 would fail checks: go build, platformgo verify, go test (--strict makes this an error)
```

`upgrade` runs from the binary at hand when it is at least as new as the version the project pins: the steps of the project are done by the new version anyway, and an older project would not know `--dry-run`.

| Mechanism | What it is |
|---|---|
| Step by step codemods | every platform version registers its codemods; skipping versions replays them in order |
| Config migrations | `platformgo.yaml` carries a schema number; renames and moves are done by code |
| Codemods | changes to the `kit` API come with rewrites of project code: Go through the syntax tree (`go/ast`), `.proto` through a scanner of imports and options. What cannot be rewritten becomes a manual step with a file and a line |
| Manual steps | printed by `plan` and `apply`; the ones that leave code which cannot compile, such as a reference to a removed name, keep `platformgo verify` failing until they are done |
| Pinned tool versions | a platform version pins buf, templ and the other generators, so generated code is identical everywhere |
| One upgrade, one commit | upgrade runs on a branch and lands as a single commit; rollback is `git revert` |

## Automatic upgrades

A project created by the platform has `.github/workflows/platform-upgrade.yml`. Every Monday, and by hand from the Actions tab, it calls the upgrade workflow of the platform (`.github/workflows/upgrade.yml`):

1. `platformgo upgrade --no-check latest`, run by the latest platformgo release (`go run github.com/aidarbn/platform-go/cmd/platformgo@latest`) rather than by the version the project pins, so a fix of the upgrade itself reaches every project with the next run; the project is still applied by the version it moves to. A project already on the latest release ends here;
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
| Upgrade matrix | a project with every module the release knew is created by each of the last three releases and moved to the checkout by `platformgo upgrade` of the checkout, as a real upgrade: the checkout stands in for a version of its own through `replace`, with no `go mod tidy` beforehand, so a module the new version needs and the upgrade leaves out of `go.sum` fails it. Then `verify`, a tidy `go.mod`, `go build`, `go vet`, `go test` must be clean. One failing release fails CI |
| `kit` API compatibility | `apidiff` against the previous tag. Every run reports the changes; a release tag fails when an incompatible change ships without a new minor version (before v1) or major version (after v1) |
| Reference projects | demo services in the repository, one per typical module set, always on the latest version |
| Codemods | before and after tests: the result matches, keeps comments, compiles with the new `kit` and a second run changes nothing |
| Every incompatible change is answered | `scripts/apicheck.sh` lists the incompatible changes that have neither a codemod covering them nor a manual step below; a release before v1 with such a change fails |

### Writing a codemod

An incompatible change of `kit` that project code can follow mechanically ships with a codemod in `internal/codemod/all.go`, under the version it is released in:

```go
codemod.RenameSymbol("v0.3.0", "github.com/aidarbn/platform-go/kit/modules/riverx", "NewQueue", "Queue"),
```

`apply` runs every codemod newer than the `platform` recorded in `platformgo.lock` and records its own version afterwards, so skipping releases replays the steps in order; `plan` lists the files a codemod would rewrite. Generated files, `vendor`, `testdata` and `node_modules` are left alone. Anything beyond a rename is a `Codemod` of its own (`AdminxNoTOTP` in `internal/codemod/adminx.go` is the example):

- `Covers` — the incompatible changes it answers for, named as apidiff names them relative to the module: `kit/adminx.(*Auth).Login`;
- `Fix` — rewrites a Go file through `File.Replace` and `File.ReplaceRange`, so comments and layout stay as they were, and leaves `File.Report` notes for what it cannot rewrite. `File.PackageImports` tells whether the package uses a `kit` package even when the file does not import it — a method call on a field of a `kit` type;
- `Check` — reports only, and runs on every `plan` and `verify`, not just while the codemod is pending. It must match only code that cannot compile with the new version, such as a reference to a removed name through the package, because a match keeps `verify` failing;
- `Proto` — rewrites a `.proto` file: `ProtoFile.Imports` and `ProtoFile.Options` find import statements and custom options, statement and field ones, skipping comments and strings; `Replace` edits byte ranges and `Report` leaves notes.

## Version policy

Before v1 the platform is still finding its shape, so the policy is what it can keep. v0.6.0 removed one time codes from `kit/adminx` without a deprecation and without a codemod; a codemod for it came later and catches projects up from v0.5 on any upgrade.

| Rule | What it means |
|---|---|
| Patch | never breaks |
| Minor before v1 | may break `kit`, but every incompatible change has a codemod that covers it or a manual step under its version below; `scripts/apicheck.sh` fails the release otherwise |
| Major after v1 | the only version that breaks; it comes with an upgrade guide |
| Deprecation before removal | where it costs little, a `kit` API is first marked `// Deprecated:` and lives for at least one minor version |
| Behaviour changes | changes that keep the API but change behaviour are listed below with a checklist |
| Support window | upgrades are guaranteed from the last three minor versions (the upgrade matrix); older ones go step by step |

## Behaviour changes

Changes that keep the API but change what a project or its clients see, by the version that brings them: what was, what is, who is affected and how to get the old behaviour back.

### v0.10.0

- **The platform's own OpenAPI generator, the default.** Without `openapi.generator` in the `api` section of `platformgo.yaml`, the description is written by `protoc-gen-platform-openapi`, a plugin of the platform, instead of protoc-gen-connect-openapi; `apply` pins it in `go.mod` in place of the connect plugin, whose module and its dependencies leave the project's graph with `go mod tidy`, and `buf.yaml` loses the gnostic dependency, so `buf.lock` is resolved again on `generate`. Who is affected: every project with the `api` module and proto files, and the clients of its description.
  - *The layout.* `api/openapi/openapi.yaml` becomes the root with references, next to a yaml per proto file, `common.yaml` and `openapi.bundle.yaml` with everything. The service serves the bundle at `/openapi.yaml`, so `/docs` and clients keep their URL; a tool that read `api/openapi/openapi.yaml` from the repository as one file reads the bundle instead.
  - *The description.* It follows the gateway: every field of a response is required, a message, wrapper or proto2 field is nullable, request schemas of messages a response also uses are named `<message>Input`, 64-bit integers of a request take a number or a string, tags are per service, every operation has the shared `Error` answer, uploads are `multipart/form-data`, protovalidate rules are JSON Schema keywords. Clients generated from it change accordingly.
  - *Annotations.* gnostic annotations are not read; the codemod `openapi.gnostic` moves summaries, tags, descriptions, titles and examples to the platform's options (`platform/openapi/v1/options.proto`, which `apply` writes under the proto folder; `buf.gen.yaml` keeps the `platform` folder out of generation) and to proto comments, and reports the rest — see the manual steps of v0.10.0. `platformgo openapi convert` does the same for the `openapiv2_*` options of grpc-gateway.
  - *`none`.* A project with `openapi.generator: none` loses the pinned connect plugin it did not use; `buf.yaml` keeps gnostic, which its own annotations may need, and the codemod leaves them alone.
  - To keep the old description, set `openapi: {generator: connect}`: `buf.yaml`, `buf.gen.yaml`, the tools and the description are then the same byte for byte as before (checked on sendkz), and the codemod leaves the annotations alone.
  - *A project without proto files of its own* gets `proto/platform/openapi/v1/options.proto` and the new `buf.gen.yaml` all the same, and no description: there is nothing to describe. modelwatch is such a project.
  - *How to upgrade.* `platformgo upgrade --dry-run v0.10.0` shows the plan, the files the codemod rewrites and what it leaves for a person; `platformgo upgrade v0.10.0` does it, and `platformgo lint` after it checks what the upgrade checks do not, the tidy `go.mod` included. The weekly `upgrade` workflow opens the same upgrade as a pull request, with nothing to change in the project. Commit the new files under `api/openapi` and `proto/platform`. Checked on copies of modelwatch and sendkz (from v0.5.11) and wallet-damdala (from v0.9.1, eight `(gnostic.openapi.v3.operation)` annotations moved by the codemod with no manual step left): build, vet, verify, lint and tests green, the description valid for the platform's validator and Redocly, and sendkz serving the bundle at `/openapi.yaml`.
- **`apply` tidies `go.mod` again after the generators.** It ran `go mod tidy` only before buf, sqlc and templ, so a module the regenerated code no longer imports stayed in `go.mod`: after the codemod `openapi.gnostic`, `github.com/google/gnostic` did, and `platformgo lint` failed on `tidy`. Now the second `go mod tidy` drops it; `apply` takes a little longer, and `--no-tidy` skips both. Nothing to do in a project.
- **Documents for audiences, an option.** `api.openapi.documents` in `platformgo.yaml` cuts documents for one audience each — the tablet, the public, the admin panel — out of the description, by group of `x-tagGroups`, tag or gRPC service, to include and to exclude: `api/openapi/openapi.<name>.bundle.yaml` with only the operations selected and the components they use. The module serves each at `/openapi/<name>.yaml` with a page at `/docs/<name>`, the whole one stays at `/openapi.yaml` and `/docs`. `API_DOCS_DOCUMENTS` picks the ones served, `all` for the whole one: `API_DOCS_DOCUMENTS=public` serves the public document alone. Without it every document is served, as the whole description already is, so a stand that must not show the admin endpoints — production — sets it explicitly; `API_DOCS=false` still serves none. `api.DocsAccess` guards each document, the whole one included, by its name: a password, a token or a role in front of all but the public one. Without documents nothing changes: `buf.gen.yaml` and the description are the same byte for byte (checked on sendkz and modelwatch). See [Documents for audiences](modules.md#documents-for-audiences).

### v0.9.1

- **`platformgo upgrade` works again for versions that bring new dependencies.** Up to v0.9.0 it ran `go get` of the module root, which is no package, so `go.sum` got no entries for the modules the new version needs; moving to v0.9.0, which brings `github.com/grafana/pyroscope-go` and `github.com/jackc/pgx-shopspring-decimal`, stopped at `go tool platformgo apply` with `missing go.sum entry`. Now it runs `go get github.com/aidarbn/platform-go/cmd/platformgo@<version>`, which records what the new tool needs; `apply` tidies the rest as before. The dry run does the same in its copy.
- **The `upgrade` workflow runs the latest platformgo, not the one the project pins.** It used `go tool platformgo upgrade`, so a project on v0.9.0 or older kept the broken step whatever was fixed on the platform; now it runs `go run github.com/aidarbn/platform-go/cmd/platformgo@latest upgrade --no-check <version>`. The inputs are the same, and a project on v0.5 to v0.9.0 that calls the workflow at `@main`, as the generated `platform-upgrade.yml` does, is upgraded by its next run with nothing to change; one that pins the workflow to a tag moves it to `@main` or `@v0.9.1`. The latest platformgo may need a newer Go than the project: with `setup-go: false` the runner must allow `GOTOOLCHAIN=auto`. To upgrade by hand from v0.9.0 or older, get the fixed tool first: `go get github.com/aidarbn/platform-go/cmd/platformgo@v0.9.1 && go tool platformgo upgrade v0.9.1`; a `platformgo` v0.9.1 on the path does it too, since it drives the upgrade itself.

### v0.9.0

- **API: a failure of an external provider is a 502.** The REST gateway answered 503 for every `Unavailable`; now it answers 502 for an `Unavailable` whose `google.rpc.ErrorInfo` has the reason `EXTERNAL_UNAVAILABLE`, whatever its domain, since nginx takes a 503 for a dead replica and stops sending to it. Another code with that reason keeps its usual answer. Affects a project that already returns such a status itself, as taply does, and has no `api.HTTPErrorHandler`: its clients get 502 instead of 503 for those failures. A project with its own `api.HTTPErrorHandler` gets the error as before and maps it itself with `api.IsExternalUnavailable`. To keep 503, drop the reason or write the errors with `api.HTTPErrorHandler`. `api.ExternalUnavailable` and `api.ExternalTimeout` build the statuses. The i18n module no longer translates the message of a status with the reason `EXTERNAL_UNAVAILABLE` or `EXTERNAL_TIMEOUT`: it names the provider.
- **API: idempotency as taply has it after its fixes.** Announce to projects that use `Idempotency-Key`.
  - *Keys count only for a known caller.* Before, a key counted for every call, and without `api.IdempotencyUser`, or for a caller it returned `""` for, all anonymous callers shared one space of keys: one could get another's answer by sending the same key and body. Now such a key is ignored, as in taply; the start logs it when no `api.IdempotencyUser` is set (a warning with `api.RequireIdempotency`). There is no switch back: it was a leak. A project that wants idempotency names the caller with `api.IdempotencyUser`; sendkz does.
  - *Keys count only on methods that change something.* Before, a key on a GET wrote a record too. Now, by default, only methods whose HTTP binding is not a GET and whose `idempotency_level` is not `NO_SIDE_EFFECTS`; `api.IdempotencyMethods` lists them explicitly, and the list wins. To keep a key on a read method, list it.
  - *Only final outcomes are remembered.* Before, `Internal`, `Unknown`, `Canceled` and a handler panic were remembered for the retention period, so a client that hung up got `Canceled` for a day. Now only a success and client errors are; everything else lets the next repeat run. `RetryableError` still decides either way: `IsRetryable() == false` keeps the old behaviour for a chosen error.
  - *A failure is replayed with its details.* The full `google.rpc.Status` is kept in the `response` column, as binary proto; records written before are replayed from their code and message as before, and a rollback reads the new ones the same way.
  - *Leases instead of a fixed lock.* `API_IDEMPOTENCY_LOCK` (1 minute, not extended: a longer handler lost its key and a repeat ran it a second time) becomes `API_IDEMPOTENCY_LEASE` (2 minutes, extended every third of it while the handler runs), with fencing: a call whose key was taken over no longer overwrites the outcome. `API_IDEMPOTENCY_LOCK` and `Config.IdempotencyLock` still work as the lease, with a warning at start; `platformgo apply` writes the new name into `.env.example`. A store of the project's own (`api.WithIdempotencyStore`) keeps working; it gets leases extended and fencing by implementing `api.IdempotencyLeaseExtender` and checking `IdempotencyRecord.Lease` in `Finish`.
  - *The outcome is written within five seconds*, on a context cut loose from the call, retried twice on a store failure, and expired records are removed at start as well as hourly.
  - *A key must be printable ASCII.* Before, a key with other bytes reached the database and answered `Internal`; now it answers `InvalidArgument`.
- **i18n: the base field of an editor request comes from its default translation.** Before, the base value of an annotated field was always copied over the translation into `I18N_DEFAULT_LOCALE`, so an editor that changed only the translation and sent the base field as it had loaded it lost the change. Now, as in taply, a request that carries the default locale for a field — empty too, which clears the field — takes the base field from it, trimmed; without that locale the base field is used and copied into the translation as before. The copy now runs after validation, so the translation gets the value the checks passed; protovalidate rules on `field_translations` itself, such as a required default locale, now see the request as the client sent it, without the copied base value. Affects only projects with the `i18n` module and editor messages with `field_translations`; modelwatch and sendkz do not use it. `api.Normalize` and `i18nx.(*Client).BaseFromTranslationInterceptor` are new.
- **API: interceptors by phase, recovery first and around the handler.** The chain used to follow the order of registration: the platform's interceptors, then whatever modules and `wireDomain` added, then `rbac`, validation and idempotency. Now every interceptor has a phase (`api.Phase`) and the chain is sorted by it; `api.AddUnaryInterceptor`, `api.AddStreamInterceptor` and `api.Authorize` keep their signatures and land where they used to run, in `PhaseAuthenticate` and `PhaseAuthorize`, so a project with its own authentication sees the same order. What changes: panic recovery is now outermost and again right around the handler. A panic of the handler is answered and counted as before, and with an `Idempotency-Key` it no longer holds the key: idempotency sees the `Internal` the client got and, as a passing failure, lets the next repeat run the handler again, where before the key stayed in progress and a repeat answered `Aborted` until the lock expired. Since the handler's recovery sits inside the project's interceptors, they now get the handler's panic as an `Internal` error instead of a panic unwinding through them: an interceptor that cleaned up in a `defer` with `recover()` must look at the error. A panic in a project interceptor, unary or stream, is caught by the outer recovery — still `Internal` for the client, logged with its stack and counted in `grpc_req_panics_recovered_total` — but the call no longer shows up in `grpc_server_handled_total` or the `call failed` log line, which now run inside the recovery. The new `api.AddUnaryInterceptorAt`, `api.AddStreamInterceptorAt`, `api.Chain` and `api.StreamChain` are opt-in. Nothing to do in a project.
- **i18n: response fields are translated after the project's authentication.** The module put both of its interceptors before the project's; now the translation of error messages stays there (`PhaseErrorsI18n`), so the refusals of the authentication are still translated, while the translation of response fields and the copy of editor requests into the default locale run in `PhaseTransform`, after authentication and access checks. `i18n.SkipTranslation` now sees the caller the authentication put into the context, which is what taply's switch for tablets needs. Affects a project with the `i18n` module and an interceptor that reads or changes the response or the request around the translation: it now sees the response before the translation of fields. modelwatch and sendkz do not use the module. `i18nx.(*Client).ResponseInterceptor` still does both in one; the new `ErrorInterceptor` and `FieldsInterceptor` are its halves.
- **CORS: `Vary: Origin` on every answer.** Before, the REST API added `Vary: Origin` only to answers for an allowed origin; now every answer carries it while `API_CORS_ORIGINS` is set, unless the origins are `*` with `API_CORS_CREDENTIALS=false` and every caller gets the same literal `*`. A shared cache can no longer hand an answer without CORS headers to a page on an allowed origin. Affects only caches in front of the API, which now keep one copy per origin; nothing to do in a project. The new `API_CORS_HEADERS`, `API_CORS_EXPOSE`, `API_CORS_CREDENTIALS`, `API_CORS_MAX_AGE` and `api.CORSHeaders` are opt-in: without them the allowed headers, credentials and the preflight max age are as before.
- **API: the client address over gRPC comes only from trusted peers.** Before, `api.ClientIP` believed `x-client-ip` metadata from any gRPC peer, so a client reaching `API_GRPC_ADDR` directly named any address and got a fresh rate limit bucket with every call. Now the address keys — the new `x-platform-client-ip` (`api.ClientIPMetadataKey`) and the old `x-client-ip` — count only from loopback, the server's own listen address and networks set explicitly in `API_TRUSTED_PROXIES`; the default of every private network does not count over gRPC. Affects a project with its own gRPC proxy on another host that names the client: list the proxy in `API_TRUSTED_PROXIES`. The old key still works from those peers, with one warning in the log; switch the proxy to `x-platform-client-ip`. The REST gateway of the process is not affected. There is no switch back: it was a way around the rate limit.
- **API: address metadata seen by handlers is the checked address.** Both keys of the incoming gRPC metadata are rewritten to the address the platform settled on before any interceptor of the project. A handler that read `x-client-ip` itself now gets the peer address of a direct untrusted client instead of what it sent; through the gateway it gets the same address as before.
- **API: `X-Forwarded-For` from the last header line, addresses normalised.** Before, the last hop was taken from the first `X-Forwarded-For` line; behind a proxy that adds a line of its own, as HAProxy does, the client's forged line named the client. Now it is the last hop of the last line. A hop that is not an address falls back to the connection address instead of being used as it is, and addresses are normalised: no port, brackets or zone, IPv4 mapped into IPv6 as plain IPv4. Affects allowlists and logs that compared the raw string. Plain routes of `api.HandleHTTP` still read the resolved address from `X-Client-IP`.
- **API: IPv6 clients are rate limited by their /64.** Before, every IPv6 address had its own bucket, so one subscriber could take a new bucket with every address of its block. `API_RATE_IPV6_PREFIX` (`RateLimit.IPv6Prefix` in code, where zero means 64) sets the length; `128` brings back a bucket per address. Affects many clients behind one /64 sharing their limits, rare outside a single subscriber.
- **API: a rate limit burst under one is taken as one.** Before, `API_RATE_BURST=0` or `API_PUBLIC_RATE_BURST=0` with a non zero rate refused every call; now a call passes at the rate. To turn a limit off, set its rate to `0`, as before.
- **API: a rate limit per principal, opt-in.** `api.RateLimitKey` names the user, client or device of a call for a second limit, `API_PRINCIPAL_RATE_RPS`/`API_PRINCIPAL_RATE_BURST` (50/100). Nothing changes until a project calls it.
- **Postgres: numeric as `decimal.Decimal` on every connection.** Every connection `pgdb.Open` and the `postgres` module make registers the codec of `pgx-shopspring-decimal`: `decimal.Decimal` parameters and scan targets — the type the generated sqlc config uses for numeric — travel in the binary format, arrays too, instead of the text fallback through `sql.Scanner`. The values are the same; every other target (`string`, `float64`, `int64`, `pgtype.Numeric`, `[]byte`, `sql.Null*`, slices, `any` and `rows.Values`, which still give `pgtype.Numeric`) and `database/sql` read and write as before. The module brings `github.com/jackc/pgx-shopspring-decimal` into `go.sum`, which `platformgo apply` tidies. Nothing to do in a project. The hooks (`pgdb.Config.Hooks`, `postgres.Apply` with `WithAfterConnect`, `WithPoolConfig`, `WithDefaults`) are opt-in.
- **Shutdown phases, budgets and River settings, opt-in.** `SHUTDOWN_PHASES`, `API_HTTP_STOP_TIMEOUT`, `API_GRPC_STOP_TIMEOUT`, `RIVER_STOP_TIMEOUT`, `RIVER_CANCEL_TIMEOUT`, `riverx.QueuesFrom` and `riverx.MaxAttemptsFrom` change nothing until a project sets or calls them: the modules stop in reverse order within `SHUTDOWN_TIMEOUT` and the queues come from `RIVER_QUEUES` or `default=10`, as before. `platformgo apply` adds the new variables to `.env.example`: `SHUTDOWN_PHASES=false` and the budgets with empty values, which mean unset. Two small differences without them: when running jobs do not finish within the shutdown timeout, River logs the warning `river jobs did not finish in time, cancelling them` before it cancels them, as it did silently before; and the `api` module stops its rate limiters and the idempotency cleanup after the running gRPC calls finish rather than before, so those calls are still limited and recorded. Nothing to do in a project.
- **Tracing: `OTEL_SERVICE_NAME` is honoured.** Before, the spans always carried `project.service` as `service.name`, whatever `OTEL_SERVICE_NAME` said; now the variable wins when set. Affects only a stand that already sets `OTEL_SERVICE_NAME`, whose traces move to that name; remove the variable to keep the old one.
- **Observability for services moving to the platform, opt-in.** `app.AliasMetric`, `METRICS_OPENMETRICS` with trace exemplars, `API_METRICS_TENANT_HEADER`, `api.DeprecatedReplacements`, `API_LOG_BODIES_MASK_PATHS` with `api.MaskLogBodies` and `PYROSCOPE_ADDR`. Without them `/metrics` serves the same metrics with the same labels in the same format, and nothing is profiled. `api_deprecated_calls_total` is now registered when the API starts rather than when the module initialises; a worker process, which serves no API, never had a series of it.
- **Webhooks, opt-in.** `kit/webhookx` sends and receives signed webhooks in the Standard Webhooks format, see [webhooks.md](webhooks.md). Nothing changes until a project calls `webhookx.NewSender` or `webhookx.NewReceiver`: a sender queues River jobs of the kind `webhook_delivery`, and a receiver brings up its table `platform_webhook_inbound` with `pgdb.MigrateSchema` when the first webhook arrives. Nothing to do in a project.
- **`platformgo upgrade` checks the project after the upgrade.** Before, it stopped after `apply`; now it also runs `go build ./...`, `platformgo verify` and `go test ./...`, so it takes longer and needs what the tests of the project need. A failed check is a warning and the exit code is the same as before; `--strict` turns it into a failure, `--no-check` skips the checks. The `upgrade` workflow passes `--no-check` and runs its own checks, as before.
- **`platformgo upgrade` runs from the binary at hand when it is at least as new as the version the project pins.** Before, a `platformgo` on the path always handed `upgrade` to `go tool platformgo` of the project. The result is the same — `go get` and `apply` by the new version — and an older pinned version would not know `--dry-run`. Other commands are still handed to the pinned version.
- **sqlc layout and types in `platformgo.yaml`, opt-in.** The `sqlc` option of the `postgres` module moves `sqlc.yaml`, the queries and the generated code and adds Go types before the platform's. Without it `db/sqlc.yaml` is the same file byte for byte, and `generate` and `verify` read the same folders.
- **buf and OpenAPI settings in `platformgo.yaml`, opt-in.** `api.proto_dir`, `api.buf.deps`, `api.buf.breaking_except` and `api.openapi.generator` adjust `buf.yaml` and `buf.gen.yaml`. Without them both files are the same byte for byte, and `generate`, `verify` and `lint proto` read `proto/` as before. One difference without them: `buf.lock` is no longer resolved again on every `generate` when a dependency in `buf.yaml` carries a label, which only a hand edit could put there.
- **`apply` no longer overwrites a generated file the project wrote itself.** Before, `apply` and `generate` rewrote every generated path that differed, so a service with its own `docker-compose.yml`, `.env.example`, `buf.yaml` or `sqlc.yaml` silently lost it. Now a file on disk without the platformgo mark on its first line and not recorded in `platformgo.lock` is a conflict: `apply` and `generate` fail and change nothing, `verify` and `doctor` name it, and `plan` marks it with `!`. `apply --adopt` creates only what does not exist and writes a report, `apply --force <path>` takes the generated version. Affects only files outside the lock without the mark; a project made by the platform has every generated file in its lock (since v0.1.0) and with the mark, so nothing changes for it — checked on modelwatch, sendkz and the upgrade matrix. To get the old behaviour for one file, `--force` it.
- **`platformgo verify` fails while a manual step of an upgrade is open.** Codemods may now leave steps for a person; the ones whose code cannot compile with the new version, such as a reference to a removed name of `kit/adminx`, keep `verify` failing until they are done. A project that builds is not affected.

## Manual steps

What a codemod cannot do, by the version that brings it. Names in backticks are the ones `scripts/apicheck.sh` matches with incompatible changes.

### v0.10.0

The platform's OpenAPI generator is the default; the codemod `openapi.gnostic` moves gnostic annotations to the platform's options and reports what it cannot move:

- `(gnostic.openapi.v3.document)` — info, servers, security and tags go into `api/openapi/base.yaml`, then the option and, with no annotation left, the gnostic import go.
- An annotation set field by field, `(gnostic.openapi.v3.operation).summary = "…"`, a second tag of an operation, formats, patterns, bounds, responses, parameters — a protovalidate rule, a comment or a platform option instead.
- While a gnostic annotation is left, buf needs the dependency: add `buf.build/gnostic/gnostic` to `api.buf.deps`, or set `openapi.generator: connect` for the time being.
- A project that stays on `connect` now and moves later converts its annotations by hand: the codemod runs once, with the upgrade.

### v0.6.0

The admin panel signs in with a password only; the codemod `adminx.no-totp` rewrites the calls and reports the rest.

- `kit/adminx.(*Auth).CreateUser` loses the `twoFactor` argument and the secret result; the codemod rewrites calls and assignments, uses of the secret are removed by hand.
- `kit/adminx.(*Auth).Login` loses the `code` argument; the codemod drops it, the code field of a sign in form is removed by hand.
- `kit/adminx.ErrCodeRequired`, `kit/adminx.ErrCodeReused`, `kit/adminx.ErrInvalidCode` — their handling is removed.
- `kit/adminx.NewTOTPSecret`, `kit/adminx.TOTPCode`, `kit/adminx.TOTPURI`, `kit/adminx.VerifyTOTP` — calls are removed.
- `kit/adminx.User.TOTPSecret`, `kit/adminx.User.TOTPCounter`, `kit/adminx.User.TwoFactor` — uses are removed. The columns stay in old databases and are no longer read.

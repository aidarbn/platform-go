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

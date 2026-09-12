# Upgradability

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
| Upgrade matrix | projects created by the last N minor versions with different module sets are upgraded to the new one; `go build`, `go test`, `verify` and `generate` must be clean. One failing combination blocks the release |
| `kit` API compatibility | `gorelease` or `apidiff` against the previous tag; a breaking change without a major bump fails CI |
| Reference projects | demo services in the repository, one per typical module set, always on the latest version |
| Codemods | before and after tests: the result matches and compiles |

## Version policy

| Rule | What it means |
|---|---|
| SemVer | a minor version never breaks projects: changes are covered by migrations and codemods; a major version comes with an upgrade guide |
| Deprecation before removal | a `kit` API is first marked `// Deprecated:` and lives for at least one minor version |
| Support window | upgrades are guaranteed from the last N minor versions; older ones go step by step |
| Release notes | a human `CHANGELOG` and the machine readable manifest of steps are built from one source |

#!/usr/bin/env bash
# Upgrade matrix: a project created by a released platform version moves to this
# checkout and must stay correct — apply by the new version, verify, a tidy go.mod,
# build, vet, tests.
#
#   scripts/upgrade-matrix.sh v0.2.0 [v0.2.1 ...]
#
# The checkout is not released yet, so the project points at it with replace under a
# version of its own and is upgraded by platformgo upgrade of the checkout, as a real
# upgrade is: no go mod tidy beforehand, go.sum as the old version left it. A new
# dependency of the platform that upgrade leaves out of go.sum fails here, as it did in
# v0.9.0 (missing go.sum entry).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
module=github.com/aidarbn/platform-go
[ "$#" -gt 0 ] || { echo "usage: $0 <released version>..." >&2; exit 2; }
# The version the checkout is given in the projects: newer than any release, so the
# codemods of every release run, and clearly not a release.
next=v0.999.0-matrix

for from in "$@"; do
  echo "== $from → $(git -C "$root" describe --tags --always --dirty)"
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT
  old="$work/platformgo-$from"
  GOBIN="$work" go install "$module/cmd/platformgo@$from"
  mv "$work/platformgo" "$old"
  new="$work/platformgo-checkout"
  (cd "$root" && go build -o "$new" ./cmd/platformgo)

  # Every module the old version knew, read from its own help.
  modules="$({ "$old" new -h 2>&1 || true; } | sed -n 's/.*comma separated modules: //p' | tr -d ' ')"
  echo "modules: $modules"

  (
    cd "$work"
    PLATFORMGO_DELEGATED=1 "$old" new example.com/matrix --dir matrix --with "$modules"
    cd matrix
    go mod tidy
    go build ./...

    # Every version of the platform is the checkout; go get of $next takes it from there.
    go mod edit -replace "$module=$root"
    "$new" upgrade --no-check "$next"
    grep -q "^platform: $next\$" platformgo.yaml
    go tool platformgo verify
    # apply tidies after the generators too, so the upgrade leaves go.mod tidy.
    go mod tidy -diff
    go build ./...
    go vet ./...
    go test ./...
  )
  rm -rf "$work"
  trap - EXIT
  echo "== $from ok"
done

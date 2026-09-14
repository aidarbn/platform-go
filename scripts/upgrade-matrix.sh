#!/usr/bin/env bash
# Upgrade matrix: a project created by a released platform version moves to this
# checkout and must stay correct — apply by the new version, verify, build, vet, tests.
#
#   scripts/upgrade-matrix.sh v0.2.0 [v0.2.1 ...]
#
# The checkout is not released yet, so the project points at it with replace and runs
# what platformgo upgrade runs after go get: go tool platformgo apply.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
module=github.com/aidarbn/platform-go
[ "$#" -gt 0 ] || { echo "usage: $0 <released version>..." >&2; exit 2; }

for from in "$@"; do
  echo "== $from → $(git -C "$root" describe --tags --always --dirty)"
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT
  old="$work/platformgo-$from"
  GOBIN="$work" go install "$module/cmd/platformgo@$from"
  mv "$work/platformgo" "$old"

  # Every module the old version knew, read from its own help.
  modules="$({ "$old" new -h 2>&1 || true; } | sed -n 's/.*comma separated modules: //p' | tr -d ' ')"
  echo "modules: $modules"

  (
    cd "$work"
    PLATFORMGO_DELEGATED=1 "$old" new example.com/matrix --dir matrix --with "$modules"
    cd matrix
    go mod tidy
    go build ./...

    go mod edit -replace "$module=$root"
    go mod tidy # what go get does in platformgo upgrade
    go tool platformgo apply
    go tool platformgo verify
    go build ./...
    go vet ./...
    go test ./...
  )
  rm -rf "$work"
  trap - EXIT
  echo "== $from ok"
done

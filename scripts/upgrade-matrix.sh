#!/usr/bin/env bash
# Upgrade matrix: a project created by a released platform version moves to this
# checkout and must stay correct — apply by the new version, verify, a tidy go.mod,
# build, vet, tests. A project with the api module gets a proto file of its own, and
# lint --proto-against the version it was created by must pass and still catch a
# deleted field or file.
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

    # A proto file of the project's own, annotated with gnostic when the old version
    # had it, committed as the base branch: after the upgrade lint --proto-against
    # must see no breaking change in it even though the gnostic files are gone (v0.10.0
    # read them as deleted), and must still see one in the project's own files.
    api=""
    case ",$modules," in *,api,*) api=1 ;; esac
    if [ -n "$api" ]; then
      gnostic=""
      grep -q 'buf.build/gnostic/gnostic' buf.yaml && gnostic=1
      mkdir -p proto/matrix/v1
      {
        printf 'syntax = "proto3";\n\npackage matrix.v1;\n\n'
        [ -n "$gnostic" ] && printf 'import "gnostic/openapi/v3/annotations.proto";\n'
        printf 'import "google/api/annotations.proto";\n\n'
        printf 'service PingService {\n  rpc Ping(PingRequest) returns (PingResponse) {\n'
        printf '    option (google.api.http) = {\n      post: "/v1/ping"\n      body: "*"\n    };\n'
        [ -n "$gnostic" ] && printf '    option (gnostic.openapi.v3.operation) = {summary: "Ping"};\n'
        printf '  }\n}\n\nmessage PingRequest {\n  string text = 1;\n  string note = 2;\n}\n\n'
        printf 'message PingResponse {\n  string text = 1;\n}\n'
      } > proto/matrix/v1/ping.proto
      go tool buf dep update
      go tool platformgo generate
      go build ./...
      git init -q -b base
      git add -A
      git -c user.name=matrix -c user.email=matrix@example.com commit -qm base
    fi

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

    if [ -n "$api" ]; then
      git switch -qc upgraded
      git add -A
      git -c user.name=matrix -c user.email=matrix@example.com commit -qm upgraded
      if [ -n "$gnostic" ] && go tool buf breaking --against .git#branch=base >/dev/null 2>&1; then
        echo "the scenario checks nothing: buf breaking with the imports passes without gnostic too" >&2; exit 1
      fi
      only="--skip format,tidy,build,generate,files,go,migrations,security"
      go tool platformgo lint --proto-against base $only
      sed -i.bak '/string note = 2;/d' proto/matrix/v1/ping.proto && rm proto/matrix/v1/ping.proto.bak
      if go tool platformgo lint --proto-against base $only >/dev/null 2>&1; then
        echo "lint --proto-against misses a deleted field" >&2; exit 1
      fi
      git checkout -q proto
      mv proto/matrix/v1/ping.proto ping.proto.moved
      mkdir -p proto/matrix/v2 && printf 'syntax = "proto3";\n\npackage matrix.v2;\n\nmessage Kept {}\n' > proto/matrix/v2/kept.proto
      if go tool platformgo lint --proto-against base $only >/dev/null 2>&1; then
        echo "lint --proto-against misses a deleted proto file" >&2; exit 1
      fi
      rm -r proto/matrix/v2 && mv ping.proto.moved proto/matrix/v1/ping.proto
    fi
  )
  rm -rf "$work"
  trap - EXIT
  echo "== $from ok"
done

#!/usr/bin/env bash
# API compatibility of kit: the exported API of this checkout against a released version.
#
#   scripts/apicheck.sh                    report against the latest tag
#   scripts/apicheck.sh v0.2.2             report against v0.2.2
#   scripts/apicheck.sh "" v0.3.0          check the release v0.3.0 against the tag before it
#
# A report never fails. A release check fails when an incompatible change ships in a
# release that does not allow it: before v1 an incompatible change needs a new minor
# version, after v1 a new major one. An incompatible change also needs a codemod when
# project code can be rewritten automatically (internal/codemod).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
module=github.com/aidarbn/platform-go
apidiff=golang.org/x/exp/cmd/apidiff@v0.0.0-20260908205506-85c1c2202aba

release="${2:-}"
base="${1:-}"
if [ -z "$base" ]; then
  base="$(git -C "$root" describe --tags --abbrev=0 --match 'v*' ${release:+--exclude "$release"} HEAD)"
fi

work="$(mktemp -d)"
cleanup() { git -C "$root" worktree remove --force "$work/base" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT

git -C "$root" worktree add --quiet --detach "$work/base" "$base"
(cd "$work/base" && go run "$apidiff" -m -w "$work/base.api" "$module" 2>/dev/null)
(cd "$root" && go run "$apidiff" -m "$work/base.api" "$module" 2>/dev/null) | sed "s#\./#$module/#" > "$work/report"
incompatible="$(sed -n '/^Incompatible changes:/,/^$/p' "$work/report" | grep -c '^- ' || true)"

summary() {
  echo "### API of $module against $base"
  echo
  if [ -s "$work/report" ]; then echo '```'; cat "$work/report"; echo '```'; else echo "No changes."; fi
}
summary
[ -n "${GITHUB_STEP_SUMMARY:-}" ] && summary >> "$GITHUB_STEP_SUMMARY"

[ -n "$release" ] || exit 0
[ "$incompatible" -eq 0 ] && { echo "$release: compatible with $base"; exit 0; }

IFS=. read -r bmaj bmin _ <<< "${base#v}"
IFS=. read -r rmaj rmin _ <<< "${release#v}"
if [ "$rmaj" -gt "$bmaj" ] || { [ "$bmaj" -eq 0 ] && [ "$rmin" -gt "$bmin" ]; }; then
  echo "$release: $incompatible incompatible changes since $base, allowed by the version"
  exit 0
fi
echo "$release: $incompatible incompatible changes since $base need a new $([ "$bmaj" -eq 0 ] && echo minor || echo major) version" >&2
exit 1

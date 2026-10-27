#!/usr/bin/env bash
# Statement coverage of kit against a real database and MinIO, with a gate.
#
#   scripts/cover.sh                 run the kit tests, print the report, fail below the gate
#   scripts/cover.sh profile.out     report on an existing profile without running tests
#
# Coverage counts every kit package hit by any kit test (-coverpkg=./kit/...).
# Generated code is left out: *.pb.go, the test service and the i18n option packages.
# The gate is COVER_MIN (default 90) for kit as a whole and COVER_PKG_MIN (default 85)
# for each package; packages in COVER_PKG_EXEMPT (space separated, none by default)
# only report. kit/platform.Run, the glue around signals, has no unit test and the
# package clears the floor without it.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
module=github.com/aidarbn/platform-go
total_min="${COVER_MIN:-90}"
pkg_min="${COVER_PKG_MIN:-85}"
pkg_exempt="${COVER_PKG_EXEMPT:-}"

profile="${1:-}"
if [ -z "$profile" ]; then
  if [ -z "${DATABASE_TEST_URL:-}" ] || [ -z "${S3_TEST_ENDPOINT:-}" ]; then
    echo "cover: set DATABASE_TEST_URL and S3_TEST_ENDPOINT, without them the database and S3 tests skip" >&2
    exit 2
  fi
  profile="$(mktemp)"
  log="$(mktemp)"
  trap 'rm -f "$profile" "$log"' EXIT
  if ! (cd "$root" && go test -count=1 -covermode=atomic -coverpkg=./kit/... -coverprofile="$profile" ./kit/... >"$log" 2>&1); then
    grep -v -e '^ok ' -e 'no test files' -e '^[[:space:]]*github.com/.*coverage: 0.0% of statements$' "$log" >&2 || true
    echo "cover: the kit tests failed" >&2
    exit 1
  fi
fi

# One line per block; a block listed by several test binaries counts once, covered
# when any of them hit it.
awk -v module="$module/" -v total_min="$total_min" -v pkg_min="$pkg_min" -v exempt=" $pkg_exempt " '
  NR == 1 && /^mode:/ { next }
  {
    split($1, loc, ":"); file = loc[1]
    if (index(file, module) == 1) file = substr(file, length(module) + 1)
    if (file ~ /\.pb\.go$/ || file ~ /^kit\/internal\/testapi\// ||
        file ~ /^kit\/i18nx\/internal\/testpb\// || file ~ /^kit\/i18nx\/i18npb\//) next
    key = file ":" loc[2]
    if (!(key in stmts)) { stmts[key] = $2; pkgof[key] = file; sub(/\/[^\/]*$/, "", pkgof[key]) }
    if ($3 > 0) hit[key] = 1
  }
  END {
    for (k in stmts) {
      p = pkgof[k]; total[p] += stmts[k]; all += stmts[k]
      if (k in hit) { covered[p] += stmts[k]; allc += stmts[k] }
    }
    fail = 0
    n = 0; for (p in total) names[++n] = p
    for (i = 2; i <= n; i++) { v = names[i]; for (j = i - 1; j >= 1 && names[j] > v; j--) names[j+1] = names[j]; names[j+1] = v }
    for (i = 1; i <= n; i++) {
      p = names[i]; pct = 100 * covered[p] / total[p]; mark = ""
      if (pct < pkg_min) { if (index(exempt, " " p " ")) mark = "  (exempt)"; else { mark = "  < " pkg_min "%"; fail = 1 } }
      printf "%-28s %6.1f%%  %5d/%-5d%s\n", p, pct, covered[p], total[p], mark
    }
    pct = all ? 100 * allc / all : 0
    printf "%-28s %6.1f%%  %5d/%-5d  gate %s%%\n", "kit total", pct, allc, all, total_min
    if (pct < total_min) fail = 1
    exit fail
  }
' "$profile"

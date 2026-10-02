#!/usr/bin/env bash
# Releases the platform: scripts/release.sh v0.6.0 (or make release VERSION=v0.6.0).
#
# A released binary knows its version from the module, so the only thing a release
# changes in the code is version.Fallback, the version of a build from a checkout.
# The script bumps it, checks the tree, commits, tags and pushes, so a tag can not
# go out with the previous fallback; CI fails a tag that does not match it anyway.
set -euo pipefail

version=${1:-}
if [[ ! $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	echo "usage: scripts/release.sh vX.Y.Z" >&2
	exit 1
fi
cd "$(git rev-parse --show-toplevel)"

[[ $(git rev-parse --abbrev-ref HEAD) == main ]] || { echo "release from main" >&2; exit 1; }
[[ -z $(git status --porcelain) ]] || { echo "the working tree is not clean" >&2; exit 1; }
git fetch --quiet --tags origin
[[ $(git rev-parse HEAD) == $(git rev-parse origin/main) ]] || { echo "main is not origin/main: pull or push first" >&2; exit 1; }
if git rev-parse --quiet --verify "refs/tags/$version" >/dev/null; then
	echo "$version is already tagged" >&2
	exit 1
fi
latest=$(git tag --list 'v*' --sort=-v:refname | head -1)
if [[ -n $latest && $(printf '%s\n%s\n' "$latest" "$version" | sort -V | tail -1) != "$version" ]]; then
	echo "$version is not newer than $latest" >&2
	exit 1
fi

file=internal/version/version.go
sed -i.bak -E "s/^const Fallback = \"v[0-9]+\.[0-9]+\.[0-9]+\"/const Fallback = \"$version\"/" "$file" && rm "$file.bak"
grep -q "const Fallback = \"$version\"" "$file" || { echo "could not bump $file" >&2; exit 1; }

test -z "$(gofmt -l .)"
go vet ./...
go test -race ./...

git commit --quiet -am "release: $version"
git tag -a "$version" -m "$version"
git push --quiet origin main "$version"
echo "released $version"

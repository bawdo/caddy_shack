#!/usr/bin/env bash
#
# Tags a release. Never pushes: the last line printed is the push command, for
# you to run once you are happy. Pushing the tag is what starts the release.
#
#   scripts/release.sh X.Y.Z
#
# Refuses unless the tree is clean, you are on the release branch at the same
# commit as the remote, CHANGELOG.md is well formed and its newest version is
# X.Y.Z, and the tag does not already exist.
#
# RELEASE_BRANCH (default main) and RELEASE_REMOTE (default origin) exist so
# the tests can run against a fixture. RELEASE_SIGN=0 skips signing the tag and
# is for those tests only.

set -euo pipefail

BRANCH="${RELEASE_BRANCH:-main}"
REMOTE="${RELEASE_REMOTE:-origin}"
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

die() {
    echo "release: $*" >&2
    exit 1
}

version="${1:-}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "version must be X.Y.Z"
tag="v$version"

[ -z "$(git status --porcelain)" ] || die "working tree is not clean"

current="$(git rev-parse --abbrev-ref HEAD)"
[ "$current" = "$BRANCH" ] || die "current branch is $current, not $BRANCH"

git fetch --quiet "$REMOTE" "$BRANCH" || die "could not fetch $REMOTE $BRANCH"
[ "$(git rev-parse HEAD)" = "$(git rev-parse "$REMOTE/$BRANCH")" ] \
    || die "$BRANCH is not up to date with $REMOTE/$BRANCH"

"$HERE/changelog.sh" check || die "CHANGELOG.md check failed"

newest="$("$HERE/changelog.sh" versions | head -1)"
[ "$newest" = "$version" ] || die "newest changelog version is $newest, not $version"

if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
    die "tag $tag already exists"
fi

sign=(-s)
[ "${RELEASE_SIGN:-1}" = "0" ] && sign=(-a)
git tag "${sign[@]}" "$tag" -m "$tag"

echo "tagged $tag"
echo "git push $REMOTE $tag"

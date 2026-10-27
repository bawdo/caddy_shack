#!/usr/bin/env bash
#
# Prints the notes for the GitHub release of a version: its CHANGELOG.md
# section, how to verify a download, and a compare link from the previous
# release tag when there is one.
#
#   scripts/release-notes.sh X.Y.Z
#
# Needs GITHUB_REPOSITORY (owner/repo). Run from the repository root.

set -euo pipefail

die() {
    echo "release-notes: $*" >&2
    exit 1
}

version="${1:-}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "usage: $0 X.Y.Z"
repo="${GITHUB_REPOSITORY:?release-notes: GITHUB_REPOSITORY is not set}"
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

# The highest tag below this version, whether or not this version is tagged
# yet. Sorted on the numbers, so v1.x orders after v0.x.
previous=$( { git tag -l 'v[0-9]*' | sed 's/^v//'; echo "$version"; } \
    | sort -u -t. -k1,1n -k2,2n -k3,3n \
    | grep -B1 -x "$version" | sed -n '1p' | grep -v -x "$version" \
    | sed 's/^/v/' || true)

"$here/changelog.sh" notes "$version" "$repo"
echo
echo "Verify a downloaded archive with:"
echo
echo '```'
echo "gh attestation verify caddy_shack_${version}_<os>_<arch>.tar.gz --repo bawdo/caddy_shack \\"
echo "  --signer-workflow bawdo/caddy_shack/.github/workflows/release.yml \\"
echo "  --source-ref refs/tags/v${version}"
echo "sha256sum -c checksums.txt --ignore-missing"
echo '```'
if [ -n "$previous" ]; then
    echo
    echo "**Full Changelog**: https://github.com/bawdo/caddy_shack/compare/$previous...v$version"
fi

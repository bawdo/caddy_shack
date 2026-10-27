#!/usr/bin/env bash
#
# Publishes the GitHub release for a tag with the archives in dist/.
#
#   scripts/publish-release.sh vX.Y.Z NOTES_FILE
#
# A published release is never touched. A draft is left over from a failed
# run and is replaced. The release is created as a draft with its assets and
# only then published, so it never exists without them. Needs
# GITHUB_REPOSITORY (owner/repo, read by gh) and an authenticated gh.

set -euo pipefail

die() {
    echo "publish-release: $*" >&2
    exit 1
}

tag="${1:-}"
notes="${2:-}"
if ! [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || [ -z "$notes" ]; then
    die "usage: $0 vX.Y.Z NOTES_FILE"
fi

draft=$(gh release view "$tag" --json isDraft --jq .isDraft 2>/dev/null || echo missing)
case "$draft" in
    false) die "$tag is already published" ;;
    true)
        echo "publish-release: replacing the draft release for $tag" >&2
        gh release delete "$tag" --yes
        ;;
esac

gh release create "$tag" --draft --verify-tag --title "$tag" --notes-file "$notes" \
    dist/*.tar.gz dist/checksums.txt
gh release edit "$tag" --draft=false --latest
gh release view "$tag" --json url --jq .url

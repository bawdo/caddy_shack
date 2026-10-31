#!/usr/bin/env bash
#
# Publishes the GitHub release for a tag with the archives in dist/.
#
#   scripts/publish-release.sh vX.Y.Z NOTES_FILE BUNDLE_FILE
#
# BUNDLE_FILE is the Sigstore bundle of the build provenance attestation. It
# is compacted onto one line, so it is a valid JSON lines file (what
# gh attestation verify --bundle reads), and attached as
# dist/caddy_shack_X.Y.Z_provenance.intoto.jsonl after the checksums. It is
# checked before any gh call, so a bad bundle leaves a stale draft alone.
#
# A published release is never touched. A draft is left over from a failed
# run and is replaced. The release is created as a draft with its assets and
# only then published, so it never exists without them. Needs
# GH_REPO or GITHUB_REPOSITORY (owner/repo, for gh) and an authenticated gh.

set -euo pipefail

die() {
    echo "publish-release: $*" >&2
    exit 1
}

tag="${1:-}"
notes="${2:-}"
bundle_in="${3:-}"
if ! [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || [ -z "$notes" ] || [ -z "$bundle_in" ]; then
    die "usage: $0 vX.Y.Z NOTES_FILE BUNDLE_FILE"
fi

bundle="dist/caddy_shack_${tag#v}_provenance.intoto.jsonl"
if ! jq -c . "$bundle_in" > "$bundle.tmp" 2>/dev/null \
    || [ "$(wc -l < "$bundle.tmp" | tr -d ' ')" != 1 ]; then
    rm -f "$bundle.tmp"
    die "BUNDLE_FILE is not a single JSON document"
fi
mv "$bundle.tmp" "$bundle"

draft=$(gh release view "$tag" --json isDraft --jq .isDraft 2>/dev/null || echo missing)
case "$draft" in
    false) die "$tag is already published" ;;
    true)
        echo "publish-release: replacing the draft release for $tag" >&2
        gh release delete "$tag" --yes
        ;;
esac

gh release create "$tag" --draft --verify-tag --title "$tag" --notes-file "$notes" \
    dist/*.tar.gz dist/checksums.txt "$bundle"
gh release edit "$tag" --draft=false --latest
gh release view "$tag" --json url --jq .url

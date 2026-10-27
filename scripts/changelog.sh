#!/usr/bin/env bash
#
# Reads CHANGELOG.md so releases are built from it rather than written by hand.
#
#   scripts/changelog.sh versions      list released versions (X.Y.Z, no "v"),
#                                      newest first
#   scripts/changelog.sh notes X.Y.Z [owner/repo]
#                                      print the section body (release notes);
#                                      with a repo, relative links point at the
#                                      file as it was at tag vX.Y.Z
#   scripts/changelog.sh check         validate the file and that every v* tag
#                                      has a section and is not ahead of it
#
# Set CHANGELOG to read a different file. Never touches the network.

set -euo pipefail

CHANGELOG="${CHANGELOG:-CHANGELOG.md}"
SEMVER='[0-9]+\.[0-9]+\.[0-9]+'
UNRELEASED_RE='^## \[Unreleased\]$'
RELEASE_RE="^## \[($SEMVER)\] - [0-9]{4}-[0-9]{2}-[0-9]{2}\$"

die() {
    echo "changelog: $*" >&2
    exit 1
}

[ -f "$CHANGELOG" ] || die "$CHANGELOG not found"

versions() {
    sed -nE "s/^## \[($SEMVER)\] - [0-9]{4}-[0-9]{2}-[0-9]{2}\$/\1/p" "$CHANGELOG"
}

notes() {
    local version="$1"
    # Print the body between "## [version]" and the next "## " heading,
    # dropping link reference lines and leading/trailing blank lines.
    awk -v ver="$version" '
        /^## / {
            if (found) exit
            if (index($0, "## [" ver "] ") == 1) { found = 1; next }
        }
        found && !/^\[[^]]+\]: / { lines[++n] = $0 }
        END {
            first = 1; while (first <= n && lines[first] ~ /^[[:space:]]*$/) first++
            last = n;  while (last >= first && lines[last] ~ /^[[:space:]]*$/) last--
            for (i = first; i <= last; i++) print lines[i]
            exit (found ? 0 : 2)
        }
    ' "$CHANGELOG" || die "no section for $version in $CHANGELOG"
}

# Release pages do not resolve repository-relative links, so point them at
# the file as it was at the tag.
absolute_links() {
    REPO="$1" REF="$2" perl -pe \
        's{\]\((?!https?:|mailto:|#)([^)\s]+)\)}{](https://github.com/$ENV{REPO}/blob/$ENV{REF}/$1)}g'
}

# Compare two X.Y.Z strings. Prints -1, 0 or 1.
semver_cmp() {
    local -a a b
    IFS=. read -ra a <<< "$1"
    IFS=. read -ra b <<< "$2"
    local i
    for i in 0 1 2; do
        if ((10#${a[$i]} < 10#${b[$i]})); then echo -1; return; fi
        if ((10#${a[$i]} > 10#${b[$i]})); then echo 1; return; fi
    done
    echo 0
}

# Release tags as X.Y.Z, one per line. Empty outside a git repository.
tag_versions() {
    git tag -l 'v[0-9]*' 2>/dev/null | sed -nE "s/^v($SEMVER)\$/\1/p" || true
}

highest_tag() {
    tag_versions | sort -t. -k1,1n -k2,2n -k3,3n | tail -1
}

check() {
    local errors=0 line v body dupes newest tag

    while IFS= read -r line; do
        if [[ "$line" =~ $UNRELEASED_RE ]] || [[ "$line" =~ $RELEASE_RE ]]; then
            continue
        elif [[ "$line" =~ ^##\ \[($SEMVER)\]$ ]]; then
            echo "changelog: missing release date: $line" >&2
        else
            echo "changelog: bad heading: $line" >&2
        fi
        errors=$((errors + 1))
    done < <(grep -E '^## ' "$CHANGELOG" || true)

    dupes=$(versions | sort | uniq -d | paste -sd ' ' -)
    if [ -n "$dupes" ]; then
        echo "changelog: duplicate versions: $dupes" >&2
        errors=$((errors + 1))
    fi

    for v in $(versions | sort -u); do
        if ! grep -qE "^\[$v\]: https://" "$CHANGELOG"; then
            echo "changelog: missing link reference for [$v]" >&2
            errors=$((errors + 1))
        fi
        body=$(notes "$v")
        if [ -z "$body" ]; then
            echo "changelog: empty section for $v" >&2
            errors=$((errors + 1))
        fi
    done

    # Every release tag must be described. A version with no tag yet is fine:
    # that is the state between merging the changelog entry and tagging.
    for v in $(tag_versions); do
        if ! versions | grep -qxF "$v"; then
            echo "changelog: tag v$v has no section in $CHANGELOG" >&2
            errors=$((errors + 1))
        fi
    done

    newest=$(versions | head -1)
    tag=$(highest_tag)
    if [ -n "$newest" ] && [ -n "$tag" ] && [ "$(semver_cmp "$newest" "$tag")" -lt 0 ]; then
        echo "changelog: newest changelog version $newest is behind tag v$tag" >&2
        errors=$((errors + 1))
    fi

    [ "$errors" -eq 0 ] || die "$errors problem(s) found"
    echo "$CHANGELOG is consistent ($(versions | wc -l | tr -d ' ') versions)"
}

case "${1:-}" in
    versions) versions ;;
    notes)
        [ -n "${2:-}" ] || die "usage: $0 notes X.Y.Z [owner/repo]"
        if [ -n "${3:-}" ]; then
            notes "$2" | absolute_links "$3" "v$2"
        else
            notes "$2"
        fi
        ;;
    check) check ;;
    *) die "usage: $0 {versions|notes X.Y.Z [owner/repo]|check}" ;;
esac

#!/usr/bin/env bash
#
# Tests for scripts/release.sh. Each case builds a bare "origin" repo and a
# clone under a temp directory, runs the script and asserts on stdout, stderr
# and exit code. Prints "ok <name>" per case and exits non-zero on the first
# failure.

set -euo pipefail

SCRIPTS="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/release_test.XXXXXX")"
trap 'rm -rf "$ROOT"' EXIT

# Fixture commits and tags must not depend on a global signing setting.
G=(git -c commit.gpgsign=false -c tag.gpgSign=false -c user.name=Test -c user.email=test@example.invalid)

OUT=""
ERR=""
CODE=0

fail() {
    echo "FAIL $1: $2" >&2
    echo "  exit: $CODE" >&2
    echo "  stdout: $OUT" >&2
    echo "  stderr: $ERR" >&2
    exit 1
}

# fixture NAME: builds $ROOT/NAME/origin (bare) and $ROOT/NAME/clone, with a
# CHANGELOG.md whose newest version is 0.1.0, pushed to origin on main.
fixture() {
    local dir="$ROOT/$1"
    mkdir -p "$dir/clone/scripts"
    git init -q --bare -b main "$dir/origin"
    git init -q -b main "$dir/clone"
    cp "$SCRIPTS/changelog.sh" "$SCRIPTS/release.sh" "$dir/clone/scripts/"
    cat > "$dir/clone/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

## [0.1.0] - 2026-10-27

### Added

- First release.

[Unreleased]: https://github.com/example/example/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/example/example/releases/tag/v0.1.0
EOF
    (
        cd "$dir/clone"
        git remote add origin "$dir/origin"
        git add -A
        "${G[@]}" commit -q -m "Initial"
        git push -q origin main
    )
    echo "$dir"
}

# run DIR ARGS...: runs release.sh from DIR/clone, capturing OUT, ERR, CODE.
run() {
    local dir="$1"
    shift
    CODE=0
    OUT=$(cd "$dir/clone" && RELEASE_SIGN=0 GIT_CEILING_DIRECTORIES="$dir" \
        bash scripts/release.sh "$@" 2>"$dir/.stderr") || CODE=$?
    ERR=$(cat "$dir/.stderr")
}

# expect_refusal NAME MESSAGE: asserts exit 1 and the exact stderr line.
expect_refusal() {
    [ "$CODE" -eq 1 ] || fail "$1" "expected exit 1"
    [ "$ERR" = "$2" ] || fail "$1" "expected stderr: $2"
    echo "ok $1"
}

name=refuses_bad_version
dir=$(fixture "$name")
run "$dir" 1.2
expect_refusal "$name" "release: version must be X.Y.Z"

name=refuses_dirty_tree
dir=$(fixture "$name")
touch "$dir/clone/extra"
run "$dir" 0.1.0
expect_refusal "$name" "release: working tree is not clean"

name=refuses_wrong_branch
dir=$(fixture "$name")
git -C "$dir/clone" checkout -q -b topic
run "$dir" 0.1.0
expect_refusal "$name" "release: current branch is topic, not main"

name=refuses_behind_origin
dir=$(fixture "$name")
git clone -q "$dir/origin" "$dir/other"
"${G[@]}" -C "$dir/other" commit -q --allow-empty -m "Extra"
git -C "$dir/other" push -q origin main
run "$dir" 0.1.0
expect_refusal "$name" "release: main is not up to date with origin/main"

name=refuses_version_mismatch
dir=$(fixture "$name")
run "$dir" 0.2.0
expect_refusal "$name" "release: newest changelog version is 0.1.0, not 0.2.0"

name=refuses_existing_tag
dir=$(fixture "$name")
"${G[@]}" -C "$dir/clone" tag v0.1.0
run "$dir" 0.1.0
expect_refusal "$name" "release: tag v0.1.0 already exists"

name=tags_and_prints_push
dir=$(fixture "$name")
run "$dir" 0.1.0
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
[ "$(git -C "$dir/clone" tag -l v0.1.0)" = "v0.1.0" ] || fail "$name" "tag v0.1.0 missing"
[ "$(git -C "$dir/clone" cat-file -t v0.1.0)" = "tag" ] || fail "$name" "tag is not annotated"
[ "$(tail -n 1 <<<"$OUT")" = "git push origin v0.1.0" ] || fail "$name" "last line is not the push command"
echo "ok $name"

name=never_pushes
[ -z "$(git -C "$dir/origin" tag -l)" ] || fail "$name" "origin has tags"
echo "ok $name"

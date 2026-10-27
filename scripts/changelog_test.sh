#!/usr/bin/env bash
#
# Tests for scripts/changelog.sh. Each case builds a fixture under a temp
# directory, runs the script and asserts on stdout, stderr and exit code.
# Prints "ok <name>" per case and exits non-zero on the first failure.

set -euo pipefail

SCRIPT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/changelog.sh"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/changelog_test.XXXXXX")"
trap 'rm -rf "$ROOT"' EXIT

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

# fixture NAME: creates a fresh directory, writes stdin to CHANGELOG.md in it
# and prints the directory path.
fixture() {
    local dir="$ROOT/$1"
    mkdir -p "$dir"
    cat > "$dir/CHANGELOG.md"
    echo "$dir"
}

# run DIR ARGS...: runs the script from DIR, capturing OUT, ERR and CODE.
run() {
    local dir="$1"
    shift
    local errfile="$dir/.stderr"
    CODE=0
    OUT=$(cd "$dir" && GIT_CEILING_DIRECTORIES="$dir" bash "$SCRIPT" "$@" 2>"$errfile") || CODE=$?
    ERR=$(cat "$errfile")
}

expect_code() {
    [ "$CODE" -eq "$2" ] || fail "$1" "expected exit $2"
}

expect_out() {
    [ "$OUT" = "$2" ] || fail "$1" "stdout did not equal expected: $2"
}

expect_err() {
    case "$ERR" in
        *"$2"*) ;;
        *) fail "$1" "stderr did not contain: $2" ;;
    esac
}

git_repo() {
    git -C "$1" init -q
    git -C "$1" -c user.name=t -c user.email=t@example.com commit --allow-empty -q -m x
}

git_tag() {
    git -C "$1" tag "$2"
}

BASIC='# Changelog

## [Unreleased]

## [0.2.0] - 2026-11-01

### Added

- Second thing.

## [0.1.0] - 2026-10-27

### Added

- First thing, see [guide](docs/x.md).
- External [a](https://e.com), [b](#anchor) and [c](mailto:x@y).

[Unreleased]: https://github.com/o/r/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/o/r/releases/tag/v0.2.0
[0.1.0]: https://github.com/o/r/releases/tag/v0.1.0'

test_versions_lists_newest_first() {
    local d
    d=$(printf '%s\n' "$BASIC" | fixture versions)
    run "$d" versions
    expect_code "${FUNCNAME[0]}" 0
    expect_out "${FUNCNAME[0]}" $'0.2.0\n0.1.0'
}

test_notes_prints_section_body() {
    local d
    d=$(printf '%s\n' "$BASIC" | fixture notes)
    run "$d" notes 0.2.0
    expect_code "${FUNCNAME[0]}" 0
    expect_out "${FUNCNAME[0]}" $'### Added\n\n- Second thing.'
}

test_notes_rewrites_relative_links() {
    local d
    d=$(printf '%s\n' "$BASIC" | fixture links)
    run "$d" notes 0.1.0 bawdo/caddy_shack
    expect_code "${FUNCNAME[0]}" 0
    case "$OUT" in
        *"(https://github.com/bawdo/caddy_shack/blob/v0.1.0/docs/x.md)"*) ;;
        *) fail "${FUNCNAME[0]}" "relative link not rewritten" ;;
    esac
    case "$OUT" in
        *"[a](https://e.com)"*"[b](#anchor)"*"[c](mailto:x@y)"*) ;;
        *) fail "${FUNCNAME[0]}" "absolute, anchor or mailto link changed" ;;
    esac
}

test_notes_unknown_version_fails() {
    local d
    d=$(printf '%s\n' "$BASIC" | fixture unknown)
    run "$d" notes 9.9.9
    expect_code "${FUNCNAME[0]}" 1
    expect_err "${FUNCNAME[0]}" "no section for 9.9.9"
}

test_check_passes_on_empty_unreleased() {
    local d
    d=$(printf '%s\n' '# Changelog

## [Unreleased]

## [0.1.0] - 2026-10-27

### Added

- Thing.

[Unreleased]: https://github.com/o/r/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/o/r/releases/tag/v0.1.0' | fixture passes)
    run "$d" check
    expect_code "${FUNCNAME[0]}" 0
    expect_out "${FUNCNAME[0]}" "CHANGELOG.md is consistent (1 versions)"
}

test_check_rejects_v_prefix() {
    local d
    d=$(printf '%s\n' '## [Unreleased]

## [v0.1.0] - 2026-10-27

- Thing.

[v0.1.0]: https://example.com' | fixture vprefix)
    run "$d" check
    expect_code "${FUNCNAME[0]}" 1
    expect_err "${FUNCNAME[0]}" "bad heading: ## [v0.1.0] - 2026-10-27"
}

test_check_rejects_missing_date() {
    local d
    d=$(printf '%s\n' '## [Unreleased]

## [0.1.0]

- Thing.

[0.1.0]: https://example.com' | fixture nodate)
    run "$d" check
    expect_code "${FUNCNAME[0]}" 1
    expect_err "${FUNCNAME[0]}" "missing release date"
}

test_check_rejects_duplicate_version() {
    local d
    d=$(printf '%s\n' '## [Unreleased]

## [0.1.0] - 2026-10-27

- Thing.

## [0.1.0] - 2026-10-28

- Other.

[0.1.0]: https://example.com' | fixture dupes)
    run "$d" check
    expect_code "${FUNCNAME[0]}" 1
    expect_err "${FUNCNAME[0]}" "duplicate versions: 0.1.0"
}

test_check_rejects_missing_link() {
    local d
    d=$(printf '%s\n' '## [Unreleased]

## [0.1.0] - 2026-10-27

- Thing.' | fixture nolink)
    run "$d" check
    expect_code "${FUNCNAME[0]}" 1
    expect_err "${FUNCNAME[0]}" "missing link reference for [0.1.0]"
}

test_check_rejects_empty_body() {
    local d
    d=$(printf '%s\n' '## [Unreleased]

## [0.1.0] - 2026-10-27

[0.1.0]: https://example.com' | fixture empty)
    run "$d" check
    expect_code "${FUNCNAME[0]}" 1
    expect_err "${FUNCNAME[0]}" "empty section for 0.1.0"
}

ONE_VERSION='## [Unreleased]

## [0.1.0] - 2026-10-27

- Thing.

[0.1.0]: https://example.com'

test_check_rejects_version_behind_tag() {
    local d
    d=$(printf '%s\n' "$ONE_VERSION" | fixture behind)
    git_repo "$d"
    git_tag "$d" v0.2.0
    run "$d" check
    expect_code "${FUNCNAME[0]}" 1
    expect_err "${FUNCNAME[0]}" "newest changelog version 0.1.0 is behind tag v0.2.0"
}

test_check_rejects_tag_without_section() {
    local d
    d=$(printf '%s\n' "$ONE_VERSION" | fixture notag)
    git_repo "$d"
    git_tag "$d" v0.1.0
    git_tag "$d" v0.0.1
    run "$d" check
    expect_code "${FUNCNAME[0]}" 1
    expect_err "${FUNCNAME[0]}" "tag v0.0.1 has no section"
}

test_check_outside_git_repo_passes() {
    local d
    d=$(printf '%s\n' "$ONE_VERSION" | fixture nogit)
    run "$d" check
    expect_code "${FUNCNAME[0]}" 0
}

for t in $(declare -F | awk '{print $3}' | grep '^test_'); do
    "$t"
    echo "ok ${t#test_}"
done

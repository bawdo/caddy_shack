#!/usr/bin/env bash
#
# Tests for scripts/docs-check.sh. Each case builds a throwaway git repo,
# runs the script and asserts on stdout, stderr and exit code. Prints
# "ok <name>" per case and exits non-zero on the first failure.

set -euo pipefail

SCRIPTS="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/docs-check_test.XXXXXX")"
trap 'rm -rf "$ROOT"' EXIT

G=(git -c commit.gpgsign=false -c user.name=Test -c user.email=test@example.invalid)
REQUIRED=(LICENSE CHANGELOG.md CONTRIBUTING.md SECURITY.md CODE_OF_CONDUCT.md
    .github/PULL_REQUEST_TEMPLATE.md .github/CODEOWNERS)
EMDASH="$(printf '\xe2\x80\x94')"

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

# fixture NAME: a committed repo holding every required file plus a README
# with no problems.
fixture() {
    local dir="$ROOT/$1" f
    mkdir -p "$dir/scripts" "$dir/.github"
    git init -q -b main "$dir"
    cp "$SCRIPTS/docs-check.sh" "$dir/scripts/"
    for f in "${REQUIRED[@]}"; do echo "$f" > "$dir/$f"; done
    echo "plain readme" > "$dir/README.md"
}

# run NAME: commits the fixture and runs the script inside it.
run() {
    local dir="$ROOT/$1" errf="$ROOT/$1.err"
    (cd "$dir" && git add -A && "${G[@]}" commit -q -m fixture)
    CODE=0
    OUT="$(cd "$dir" && bash scripts/docs-check.sh 2>"$errf")" || CODE=$?
    ERR="$(cat "$errf")"
}

test_fails_on_em_dash() {
    fixture em
    printf 'a %s b\n' "$EMDASH" > "$ROOT/em/README.md"
    run em
    [[ $CODE -eq 1 ]] || fail fails_on_em_dash "expected exit 1"
    [[ "$ERR" == *"README.md:1: em or en dash"* ]] || fail fails_on_em_dash "message missing"
    echo "ok fails_on_em_dash"
}

test_fails_on_broken_link() {
    fixture link
    echo "[x](docs/missing.md)" > "$ROOT/link/README.md"
    run link
    [[ $CODE -eq 1 ]] || fail fails_on_broken_link "expected exit 1"
    [[ "$ERR" == *"README.md:1: broken link docs/missing.md"* ]] || fail fails_on_broken_link "message missing"
    echo "ok fails_on_broken_link"
}

test_fails_on_missing_required() {
    fixture req
    rm "$ROOT/req/SECURITY.md"
    run req
    [[ $CODE -eq 1 ]] || fail fails_on_missing_required "expected exit 1"
    [[ "$ERR" == *"SECURITY.md: required file missing"* ]] || fail fails_on_missing_required "message missing"
    echo "ok fails_on_missing_required"
}

test_passes_on_clean_tree() {
    fixture clean
    mkdir -p "$ROOT/clean/docs/superpowers"
    echo "target" > "$ROOT/clean/docs/real.md"
    printf '[ok](docs/real.md#frag) [web](https://example.com) [anchor](#top)\n' > "$ROOT/clean/README.md"
    printf 'historical %s record\n' "$EMDASH" > "$ROOT/clean/docs/superpowers/old.md"
    run clean
    [[ $CODE -eq 0 ]] || fail passes_on_clean_tree "expected exit 0"
    [[ "$OUT" == "docs are consistent" ]] || fail passes_on_clean_tree "stdout mismatch"
    echo "ok passes_on_clean_tree"
}

test_fails_on_em_dash
test_fails_on_broken_link
test_fails_on_missing_required
test_passes_on_clean_tree

#!/usr/bin/env bash
#
# Tests for the scripts release.yml runs: verify-tag.sh, publish-release.sh,
# release-notes.sh and build-release.sh. Every gh call goes to a fake gh put
# first on PATH, which logs its arguments and answers from canned files, so
# nothing here touches the network. Prints "ok <name>" per case and exits
# non-zero on the first failure. The build cases cross-compile four binaries
# each, so they run last and take a minute.

set -euo pipefail

SCRIPTS="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd -- "$SCRIPTS/.." && pwd)"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/release-workflow_test.XXXXXX")"
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

# The fake gh. FAKE_GH_DIR holds the canned answers and the call log:
#   ref.json      body of GET repos/OWNER/REPO/git/ref/tags/TAG
#   tag.json      body of GET repos/OWNER/REPO/git/tags/SHA
#   is_draft      what "release view --json isDraft" prints; when absent the
#                 release does not exist and the view exits 1. Create, edit
#                 and delete keep it in step, as GitHub would.
#   gh.log        one line per call, the arguments joined by spaces
mkdir -p "$ROOT/bin"
cat > "$ROOT/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
echo "$*" >> "$FAKE_GH_DIR/gh.log"
jq_expr=""
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
    [ "${args[$i]}" = "--jq" ] && jq_expr="${args[$((i + 1))]}"
done
answer() {
    if [ -n "$jq_expr" ]; then jq -r "$jq_expr" "$1"; else cat "$1"; fi
}
case "$1 $2" in
    "api "*/git/ref/tags/*) answer "$FAKE_GH_DIR/ref.json" ;;
    "api "*/git/tags/*) answer "$FAKE_GH_DIR/tag.json" ;;
    "release view")
        if [ ! -f "$FAKE_GH_DIR/is_draft" ]; then
            echo "release not found" >&2
            exit 1
        fi
        case "$*" in
            *isDraft*) cat "$FAKE_GH_DIR/is_draft" ;;
            *url*) echo "https://github.com/bawdo/caddy_shack/releases/tag/$3" ;;
        esac
        ;;
    "release create") echo true > "$FAKE_GH_DIR/is_draft" ;;
    "release edit")
        case "$*" in *--draft=false*) echo false > "$FAKE_GH_DIR/is_draft" ;; esac
        ;;
    "release delete") rm -f "$FAKE_GH_DIR/is_draft" ;;
    *)
        echo "fake gh: unexpected call: $*" >&2
        exit 2
        ;;
esac
EOF
chmod +x "$ROOT/bin/gh"

# fake NAME: makes a fresh directory for one case's canned answers, with a
# dist/ holding stand-in release assets, and prints its path.
fake() {
    local dir="$ROOT/$1"
    mkdir -p "$dir/dist"
    : > "$dir/gh.log"
    touch "$dir/dist/caddy_shack_0.1.0_linux_amd64.tar.gz" "$dir/dist/checksums.txt"
    echo "$dir"
}

# run_gh DIR SCRIPT ARGS...: runs SCRIPT from DIR with the fake gh first on
# PATH, capturing OUT, ERR and CODE.
run_gh() {
    local dir="$1" script="$2"
    shift 2
    CODE=0
    OUT=$(cd "$dir" && PATH="$ROOT/bin:$PATH" FAKE_GH_DIR="$dir" GITHUB_REPOSITORY=bawdo/caddy_shack \
        GIT_CEILING_DIRECTORIES="$ROOT" \
        bash "$SCRIPTS/$script" "$@" 2>"$dir/.stderr") || CODE=$?
    ERR=$(cat "$dir/.stderr")
}

# expect_refusal NAME MESSAGE: asserts exit 1 and the exact stderr line.
expect_refusal() {
    [ "$CODE" -eq 1 ] || fail "$1" "expected exit 1"
    [ "$ERR" = "$2" ] || fail "$1" "expected stderr: $2"
}

# log_order NAME DIR PATTERN...: asserts each PATTERN matches a line of the
# fake gh log, later patterns on later lines.
log_order() {
    local name="$1" dir="$2" line=0 found pattern
    shift 2
    for pattern in "$@"; do
        found=$(awk -v start="$line" -v pat="$pattern" 'NR > start && index($0, pat) { print NR; exit }' "$dir/gh.log")
        [ -n "$found" ] || fail "$name" "gh log has no \"$pattern\" after line $line: $(cat "$dir/gh.log")"
        line=$found
    done
}

VALID_TAG='{"verification":{"verified":true,"reason":"valid"}}'

# tagged NAME: a fake directory that is also a git repository with an
# annotated (unsigned) tag v0.1.0, standing in for the checked-out tag.
# Prints its path.
tagged() {
    local dir
    dir=$(fake "$1")
    git init -q -b main "$dir"
    "${G[@]}" -C "$dir" commit -q --allow-empty -m "Initial"
    "${G[@]}" -C "$dir" tag -a v0.1.0 -m "v0.1.0"
    echo "$dir"
}

# remote_ref DIR TYPE SHA: what GitHub answers for the ref of v0.1.0.
remote_ref() {
    printf '{"object":{"type":"%s","sha":"%s"}}\n' "$2" "$3" > "$1/ref.json"
}

# The accepting case is also the one where the remote tag object matches the
# local one.
name=verify_tag_accepts_valid_signed_tag
dir=$(tagged "$name")
sha=$(git -C "$dir" rev-parse refs/tags/v0.1.0)
remote_ref "$dir" tag "$sha"
echo "$VALID_TAG" > "$dir/tag.json"
run_gh "$dir" verify-tag.sh v0.1.0
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
log_order "$name" "$dir" "api repos/bawdo/caddy_shack/git/ref/tags/v0.1.0" "api repos/bawdo/caddy_shack/git/tags/$sha"
echo "ok $name"

name=verify_tag_rejects_lightweight_tag
dir=$(tagged "$name")
remote_ref "$dir" commit "$(git -C "$dir" rev-parse HEAD)"
echo "$VALID_TAG" > "$dir/tag.json"
run_gh "$dir" verify-tag.sh v0.1.0
expect_refusal "$name" "verify-tag: v0.1.0 is not an annotated tag"
echo "ok $name"

name=verify_tag_rejects_tag_moved_since_checkout
dir=$(tagged "$name")
sha=$(git -C "$dir" rev-parse refs/tags/v0.1.0)
remote_ref "$dir" tag 0123456789abcdef0123456789abcdef01234567
echo "$VALID_TAG" > "$dir/tag.json"
run_gh "$dir" verify-tag.sh v0.1.0
expect_refusal "$name" "verify-tag: v0.1.0 on GitHub is 0123456789abcdef0123456789abcdef01234567, not the checked-out tag object $sha"
! grep -q "git/tags/" "$dir/gh.log" || fail "$name" "read the verification of the wrong tag object"
echo "ok $name"

name=verify_tag_rejects_missing_local_tag
dir=$(fake "$name")
git init -q -b main "$dir"
remote_ref "$dir" tag 0123456789abcdef0123456789abcdef01234567
echo "$VALID_TAG" > "$dir/tag.json"
run_gh "$dir" verify-tag.sh v0.1.0
expect_refusal "$name" "verify-tag: v0.1.0 is not a local tag"
echo "ok $name"

name=verify_tag_rejects_unverified
dir=$(tagged "$name")
remote_ref "$dir" tag "$(git -C "$dir" rev-parse refs/tags/v0.1.0)"
echo '{"verification":{"verified":false,"reason":"unsigned"}}' > "$dir/tag.json"
run_gh "$dir" verify-tag.sh v0.1.0
expect_refusal "$name" "verify-tag: v0.1.0 signature not verified by GitHub (unsigned)"
echo "ok $name"

name=publish_stops_on_published_release
dir=$(fake "$name")
echo false > "$dir/is_draft"
run_gh "$dir" publish-release.sh v0.1.0 "$dir/notes.md"
expect_refusal "$name" "publish-release: v0.1.0 is already published"
! grep -q "release create" "$dir/gh.log" || fail "$name" "created a release"
echo "ok $name"

name=publish_replaces_draft
dir=$(fake "$name")
echo true > "$dir/is_draft"
run_gh "$dir" publish-release.sh v0.1.0 "$dir/notes.md"
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
log_order "$name" "$dir" "release delete v0.1.0 --yes" "release create v0.1.0 --draft --verify-tag" \
    "release edit v0.1.0 --draft=false --latest"
[ "$OUT" = "https://github.com/bawdo/caddy_shack/releases/tag/v0.1.0" ] || fail "$name" "did not print the release URL"
echo "ok $name"

name=publish_creates_when_missing
dir=$(fake "$name")
run_gh "$dir" publish-release.sh v0.1.0 "$dir/notes.md"
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
! grep -q "release delete" "$dir/gh.log" || fail "$name" "deleted a release"
log_order "$name" "$dir" "release create v0.1.0 --draft --verify-tag --title v0.1.0 --notes-file $dir/notes.md" \
    "release edit v0.1.0 --draft=false --latest"
grep -q "release create .* dist/caddy_shack_0.1.0_linux_amd64.tar.gz dist/checksums.txt" "$dir/gh.log" \
    || fail "$name" "assets not attached"
echo "ok $name"

# notes_repo NAME: a git repository holding CHANGELOG.md with 0.1.0 and 0.2.0
# and the two scripts release-notes.sh needs. Prints its path.
notes_repo() {
    local dir="$ROOT/$1"
    mkdir -p "$dir/scripts"
    cp "$SCRIPTS/changelog.sh" "$SCRIPTS/release-notes.sh" "$dir/scripts/"
    cat > "$dir/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

## [0.2.0] - 2026-11-01

### Added

- Second release, see [the README](README.md).

## [0.1.0] - 2026-10-27

### Added

- First release.

[Unreleased]: https://github.com/bawdo/caddy_shack/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/bawdo/caddy_shack/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/bawdo/caddy_shack/releases/tag/v0.1.0
EOF
    git init -q -b main "$dir"
    git -C "$dir" add -A
    "${G[@]}" -C "$dir" commit -q -m "Initial"
    echo "$dir"
}

# run_notes DIR VERSION: runs release-notes.sh from DIR, capturing OUT, ERR, CODE.
run_notes() {
    CODE=0
    OUT=$(cd "$1" && GIT_CEILING_DIRECTORIES="$1" GITHUB_REPOSITORY=bawdo/caddy_shack \
        bash scripts/release-notes.sh "$2" 2>"$1/.stderr") || CODE=$?
    ERR=$(cat "$1/.stderr")
}

name=release_notes_first_release
dir=$(notes_repo "$name")
run_notes "$dir" 0.1.0
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
body=$(cd "$dir" && bash scripts/changelog.sh notes 0.1.0 bawdo/caddy_shack)
[ "${OUT:0:${#body}}" = "$body" ] || fail "$name" "does not start with the 0.1.0 body"
grep -qF "gh attestation verify caddy_shack_0.1.0_<os>_<arch>.tar.gz --repo bawdo/caddy_shack" <<<"$OUT" \
    || fail "$name" "no attestation command"
grep -qF "sha256sum -c checksums.txt --ignore-missing" <<<"$OUT" || fail "$name" "no checksum command"
! grep -q "Full Changelog" <<<"$OUT" || fail "$name" "has a Full Changelog line"
echo "ok $name"

name=release_notes_with_previous_tag
dir=$(notes_repo "$name")
"${G[@]}" -C "$dir" tag -a v0.1.0 -m "v0.1.0"
run_notes "$dir" 0.2.0
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
grep -qF "https://github.com/bawdo/caddy_shack/blob/v0.2.0/README.md" <<<"$OUT" || fail "$name" "relative link not made absolute"
[ "$(tail -n 1 <<<"$OUT")" = "**Full Changelog**: https://github.com/bawdo/caddy_shack/compare/v0.1.0...v0.2.0" ] \
    || fail "$name" "last line is not the Full Changelog link"
echo "ok $name"

# The build runs in a copy of the committed tree, with the working tree's
# scripts/ laid over it (so uncommitted script changes are what is tested)
# and a placeholder LICENSE when the tree has none.
BUILD="$ROOT/build"
mkdir -p "$BUILD"
git -C "$REPO" archive HEAD | tar -x -C "$BUILD"
cp "$SCRIPTS"/*.sh "$BUILD/scripts/"
[ -f "$BUILD/LICENSE" ] || touch "$BUILD/LICENSE"

# run_build ARGS...: runs build-release.sh from the build copy, capturing OUT,
# ERR and CODE.
run_build() {
    CODE=0
    OUT=$(cd "$BUILD" && GIT_CEILING_DIRECTORIES="$ROOT" bash scripts/build-release.sh "$@" 2>"$ROOT/.build-stderr") || CODE=$?
    ERR=$(cat "$ROOT/.build-stderr")
}

name=build_release_produces_archives
run_build 0.0.1 deadbeef
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
for target in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
    archive="$BUILD/dist/caddy_shack_0.0.1_$target.tar.gz"
    [ -f "$archive" ] || fail "$name" "missing $archive"
    listing=$(tar -tzf "$archive")
    for file in caddy_shack LICENSE README.md CHANGELOG.md; do
        grep -qx "caddy_shack_0.0.1_$target/$file" <<<"$listing" || fail "$name" "$target archive lacks $file"
    done
done
[ "$(find "$BUILD/dist" -name '*.tar.gz' | wc -l | tr -d ' ')" = 4 ] || fail "$name" "expected four archives"
[ "$(wc -l < "$BUILD/dist/checksums.txt" | tr -d ' ')" = 4 ] || fail "$name" "expected four checksum lines"
if command -v sha256sum >/dev/null 2>&1; then
    (cd "$BUILD/dist" && sha256sum -c --quiet checksums.txt) || fail "$name" "checksums do not match"
else
    (cd "$BUILD/dist" && shasum -a 256 -c --quiet checksums.txt) || fail "$name" "checksums do not match"
fi
echo "ok $name"

name=build_release_smoke_fails_on_wrong_version
CODE=0
OUT=$(cd "$BUILD" && BUILD_RELEASE_EXPECT=v9.9.9 GIT_CEILING_DIRECTORIES="$ROOT" \
    bash scripts/build-release.sh 0.0.1 deadbeef 2>"$ROOT/.build-stderr") || CODE=$?
ERR=$(cat "$ROOT/.build-stderr")
[ "$CODE" -eq 1 ] || fail "$name" "expected exit 1"
[ "$(tail -n 1 <<<"$ERR")" = "build-release: built binary reports caddy_shack v0.0.1, expected caddy_shack v9.9.9" ] \
    || fail "$name" "wrong refusal"
echo "ok $name"

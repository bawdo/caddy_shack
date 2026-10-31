#!/usr/bin/env bash
#
# Tests for scripts/history-scan.sh. Each case builds a throwaway git repo,
# plants one thing in its history, runs the script and asserts on stdout,
# stderr and exit code. Prints "ok <name>" per case and exits non-zero on
# the first failure.
#
# The planted strings (attribution trailers, tokens, keys, home paths) are
# assembled from pieces at runtime so this file never matches the scan
# itself and never looks like a real secret to a push protection check.

set -euo pipefail

SCRIPTS="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/history-scan_test.XXXXXX")"
trap 'rm -rf "$ROOT"' EXIT

OWNER=keith@bawdo.com
# Fixture repos get an empty hooks directory so a global commit-msg hook on
# the developer's machine cannot reject the deliberately planted messages.
mkdir "$ROOT/no-hooks"
G=(git -c commit.gpgsign=false -c tag.gpgSign=false -c "core.hooksPath=$ROOT/no-hooks"
    -c user.name=Keith -c "user.email=$OWNER")

# Fixture pieces, joined here so no line of this file holds the whole string.
TRAILER_KEY="Co-Authored""-By:"
BOT_NAME="Cla""ude"
BOT_ADDR="noreply@""anthropic.com"
SESSION_KEY="${BOT_NAME}-Session"":"
HOME_PATH="/Users/""bawdo/x"
KEY_LINE="-----BEGIN OPENSSH"" PRIVATE KEY-----"
TOKEN="ghp""_$(printf 'a%.0s' {1..36})"
RSA_BEGIN="-----BEGIN RSA"" PRIVATE KEY-----"
RSA_END="-----END RSA"" PRIVATE KEY-----"
B64_LINE="$(printf 'Qk%.0s' {1..32})"

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

# fixture NAME: a repo with one clean commit by the owner and an allow file
# listing the owner's address.
fixture() {
    local dir="$ROOT/$1"
    git init -q -b main "$dir"
    echo "email:$OWNER" > "$dir/.history-scan-allow"
    echo "hello" > "$dir/README.md"
    (cd "$dir" && git add -A && "${G[@]}" commit -q -m "Added the readme")
}

# commit NAME MESSAGE [GIT ARGS...]: commits whatever is in the tree.
commit() {
    local dir="$ROOT/$1" msg="$2"
    shift 2
    (cd "$dir" && git add -A && "${G[@]}" "$@" commit -q --allow-empty -m "$msg")
}

# run NAME [ARGS...]: runs the script inside the fixture repo.
run() {
    local dir="$ROOT/$1" errf="$ROOT/$1.err"
    shift
    CODE=0
    OUT="$(cd "$dir" && GIT_CEILING_DIRECTORIES="$ROOT" bash "$SCRIPTS/history-scan.sh" "$@" 2>"$errf")" || CODE=$?
    ERR="$(cat "$errf")"
}

test_clean_repo_passes() {
    fixture clean
    run clean
    [[ $CODE -eq 0 ]] || fail clean_repo_passes "expected exit 0"
    [[ "$OUT" == "history-scan: clean"* ]] || fail clean_repo_passes "stdout mismatch"
    [[ -z "$ERR" ]] || fail clean_repo_passes "unexpected stderr"
    echo "ok clean_repo_passes"
}

test_flags_attribution_trailer() {
    fixture trailer
    commit trailer "Changed a thing

$TRAILER_KEY $BOT_NAME <$BOT_ADDR>"
    run trailer
    [[ $CODE -eq 1 ]] || fail flags_attribution_trailer "expected exit 1"
    [[ "$ERR" == *"attribution trailer"* ]] || fail flags_attribution_trailer "message missing"
    echo "ok flags_attribution_trailer"
}

test_flags_session_url() {
    fixture session
    commit session "Changed a thing

$SESSION_KEY https://example.invalid/session/1"
    run session
    [[ $CODE -eq 1 ]] || fail flags_session_url "expected exit 1"
    [[ "$ERR" == *"session url"* ]] || fail flags_session_url "message missing"
    echo "ok flags_session_url"
}

test_flags_home_path_in_blob() {
    fixture home
    echo "see $HOME_PATH" > "$ROOT/home/notes.md"
    commit home "Added notes"
    run home
    [[ $CODE -eq 1 ]] || fail flags_home_path_in_blob "expected exit 1"
    [[ "$ERR" == *":notes.md: home path: "* ]] || fail flags_home_path_in_blob "message missing"
    echo "ok flags_home_path_in_blob"
}

test_flags_private_key_in_blob() {
    fixture key
    printf '%s\n%s\n' "$KEY_LINE" "$B64_LINE" > "$ROOT/key/id"
    commit key "Added a key"
    run key
    [[ $CODE -eq 1 ]] || fail flags_private_key_in_blob "expected exit 1"
    [[ "$ERR" == *"private key"* ]] || fail flags_private_key_in_blob "message missing"
    echo "ok flags_private_key_in_blob"
}

test_flags_token_in_deleted_file() {
    fixture token
    echo "token=$TOKEN" > "$ROOT/token/env"
    commit token "Added env"
    rm "$ROOT/token/env"
    commit token "Removed env"
    run token
    [[ $CODE -eq 1 ]] || fail flags_token_in_deleted_file "expected exit 1"
    [[ "$ERR" == *":env: github token: "* ]] || fail flags_token_in_deleted_file "message missing"
    echo "ok flags_token_in_deleted_file"
}

test_flags_unlisted_author_email() {
    fixture email
    echo "more" >> "$ROOT/email/README.md"
    commit email "Changed the readme" -c user.email=someone@example.com
    run email
    [[ $CODE -eq 1 ]] || fail flags_unlisted_author_email "expected exit 1"
    [[ "$ERR" == *"unlisted email: someone@example.com"* ]] || fail flags_unlisted_author_email "message missing"
    echo "ok flags_unlisted_author_email"
}

test_flags_hit_on_unmerged_branch() {
    fixture branch
    (cd "$ROOT/branch" && git checkout -q -b side)
    echo "see $HOME_PATH" > "$ROOT/branch/notes.md"
    commit branch "Added notes"
    (cd "$ROOT/branch" && git checkout -q main)
    run branch
    [[ $CODE -eq 1 ]] || fail flags_hit_on_unmerged_branch "expected exit 1"
    [[ "$ERR" == *"home path"* ]] || fail flags_hit_on_unmerged_branch "message missing"
    echo "ok flags_hit_on_unmerged_branch"
}

test_allow_file_accepts_named_blob() {
    fixture allow
    echo "see $HOME_PATH" > "$ROOT/allow/notes.md"
    commit allow "Added notes"
    echo "blob:notes.md:home path" >> "$ROOT/allow/.history-scan-allow"
    run allow
    [[ $CODE -eq 0 ]] || fail allow_file_accepts_named_blob "expected exit 0"
    [[ "$OUT" == "history-scan: clean"* ]] || fail allow_file_accepts_named_blob "stdout mismatch"
    echo "ok allow_file_accepts_named_blob"
}

test_flags_tag_message() {
    fixture tag
    (cd "$ROOT/tag" && "${G[@]}" tag -a v0.0.1 -m "Release

$TRAILER_KEY $BOT_NAME")
    run tag
    [[ $CODE -eq 1 ]] || fail flags_tag_message "expected exit 1"
    [[ "$ERR" == *"attribution trailer"* ]] || fail flags_tag_message "message missing"
    echo "ok flags_tag_message"
}

test_ignores_pem_placeholder() {
    fixture pemfake
    printf '%s\nKEY\n%s\n' "$RSA_BEGIN" "$RSA_END" > "$ROOT/pemfake/fixture.pem"
    commit pemfake "Added a placeholder key"
    run pemfake
    [[ $CODE -eq 0 ]] || fail ignores_pem_placeholder "expected exit 0"
    [[ "$OUT" == "history-scan: clean"* ]] || fail ignores_pem_placeholder "stdout mismatch"
    echo "ok ignores_pem_placeholder"
}

test_flags_pem_with_real_body() {
    fixture pemreal
    printf '%s\n%s\n%s\n' "$RSA_BEGIN" "$B64_LINE" "$RSA_END" > "$ROOT/pemreal/fixture.pem"
    commit pemreal "Added a key"
    run pemreal
    [[ $CODE -eq 1 ]] || fail flags_pem_with_real_body "expected exit 1"
    [[ "$ERR" == *":fixture.pem: private key: 1:"* ]] || fail flags_pem_with_real_body "message missing"
    echo "ok flags_pem_with_real_body"
}

test_flags_token_after_invalid_utf8_byte() {
    fixture latin
    { printf 'x=caf\xe9 key='; printf 'AKIA%s\n' "$(printf 'A%.0s' {1..16})"; } > "$ROOT/latin/conf.ini"
    commit latin "Added config"
    run latin
    [[ $CODE -eq 1 ]] || fail flags_token_after_invalid_utf8_byte "expected exit 1"
    [[ "$ERR" == *":conf.ini: aws key: "* ]] || fail flags_token_after_invalid_utf8_byte "message missing"
    echo "ok flags_token_after_invalid_utf8_byte"
}

test_flags_pem_in_quoted_lines() {
    fixture quoted
    {
        echo 'KEY = ('
        printf '    "%s\\n"\n' "$RSA_BEGIN" "$B64_LINE"
        printf '    "%s\\n" +\n' "$B64_LINE"
        printf '    "%s\\n"\n' "$RSA_END"
        echo ')'
    } > "$ROOT/quoted/key.py"
    commit quoted "Added a key module"
    run quoted
    [[ $CODE -eq 1 ]] || fail flags_pem_in_quoted_lines "expected exit 1"
    [[ "$ERR" == *":key.py: private key: 2:"* ]] || fail flags_pem_in_quoted_lines "message missing"
    echo "ok flags_pem_in_quoted_lines"
}

test_flags_empty_commit_email() {
    fixture empty
    (cd "$ROOT/empty" && GIT_AUTHOR_EMAIL='' GIT_COMMITTER_EMAIL='' \
        "${G[@]}" -c user.email= commit -q --allow-empty -m "Changed nothing")
    run empty
    [[ $CODE -eq 1 ]] || fail flags_empty_commit_email "expected exit 1"
    [[ "$ERR" == *"unlisted email: (empty)"* ]] || fail flags_empty_commit_email "message missing"
    echo "ok flags_empty_commit_email"
}

test_ignores_pem_mention_in_code() {
    fixture mention
    {
        echo '    if strings.Contains(d, "BEGIN EC PRIVATE KEY") {'
        echo 'func TestCheckConfigDriftFailWhenRoutesAndCertsBothDiffer(t *testing.T) {'
        echo '// A running container whose admin API is down is a broken setup, not a CA'
        echo '}'
    } > "$ROOT/mention/doctor_test.go"
    commit mention "Added a test"
    run mention
    [[ $CODE -eq 0 ]] || fail ignores_pem_mention_in_code "expected exit 0"
    echo "ok ignores_pem_mention_in_code"
}

test_msg_allows_claude_file_names() {
    fixture filenames
    local notes="CLAUDE"".md" local_notes="CLAUDE"".local.md" dot_dir=".cla""ude"
    commit filenames "Ignored agent notes

Ignored $local_notes and updated docs/$notes, then read ~/$dot_dir/settings.json."
    run filenames
    [[ $CODE -eq 0 ]] || fail msg_allows_claude_file_names "expected exit 0"
    [[ "$OUT" == "history-scan: clean"* ]] || fail msg_allows_claude_file_names "stdout mismatch"
    [[ -z "$ERR" ]] || fail msg_allows_claude_file_names "unexpected stderr"
    echo "ok msg_allows_claude_file_names"
}

test_msg_flags_claude_beside_file_name() {
    fixture beside
    local notes="CLAUDE"".md" line
    line="Ignored $notes, written by $BOT_NAME"
    commit beside "Changed a thing

$line"
    run beside
    [[ $CODE -eq 1 ]] || fail msg_flags_claude_beside_file_name "expected exit 1"
    [[ "$ERR" == *": claude: $line"* ]] || fail msg_flags_claude_beside_file_name "original line missing"
    echo "ok msg_flags_claude_beside_file_name"
}

test_msg_flags_bare_name() {
    fixture bare
    commit bare "$BOT_NAME"
    run bare
    [[ $CODE -eq 1 ]] || fail msg_flags_bare_name "expected exit 1"
    [[ "$ERR" == *": claude: $BOT_NAME"* ]] || fail msg_flags_bare_name "message missing"
    echo "ok msg_flags_bare_name"
}

test_msg_flags_claude_as_path_segment() {
    fixture segment
    local notes="CLAUDE"".md" line
    line="Written by $BOT_NAME/$notes"
    commit segment "Changed a thing

$line"
    run segment
    [[ $CODE -eq 1 ]] || fail msg_flags_claude_as_path_segment "expected exit 1"
    [[ "$ERR" == *": claude: $line"* ]] || fail msg_flags_claude_as_path_segment "original line missing"
    echo "ok msg_flags_claude_as_path_segment"
}

test_clean_repo_passes
test_flags_attribution_trailer
test_flags_session_url
test_flags_home_path_in_blob
test_flags_private_key_in_blob
test_flags_token_in_deleted_file
test_flags_unlisted_author_email
test_flags_hit_on_unmerged_branch
test_allow_file_accepts_named_blob
test_flags_tag_message
test_ignores_pem_placeholder
test_flags_pem_with_real_body
test_flags_token_after_invalid_utf8_byte
test_flags_pem_in_quoted_lines
test_flags_empty_commit_email
test_ignores_pem_mention_in_code
test_msg_allows_claude_file_names
test_msg_flags_claude_beside_file_name
test_msg_flags_bare_name
test_msg_flags_claude_as_path_segment

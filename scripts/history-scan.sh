#!/usr/bin/env bash
#
# history-scan.sh [--allow FILE]
#
# Scans the whole history reachable from every ref (local branches,
# remote-tracking branches, tags) for things that must not be published:
# agent attribution in commit and tag messages, secrets and home-directory
# paths in any blob ever committed, and author, committer or tagger emails
# outside the allow list. Run it before pushing to a public remote.
#
# Exits 0 printing "history-scan: clean (N commits, M blobs)", or exits 1
# printing one "history-scan: <commit-or-blob>: <pattern name>: <match>"
# line per hit on stderr.
#
# The allow file (default .history-scan-allow at the repository root,
# optional) holds lines "email:<address>" and "blob:<path>:<pattern name>"
# for accepted hits. Blank lines and lines starting with # are ignored.
#
# Patterns are written so this file never matches them itself (for example
# [.] and [:] in place of a literal character).

set -euo pipefail

# Byte semantics for grep and awk: under a UTF-8 locale an invalid byte can
# hide the rest of a line from BSD grep or make GNU grep treat the whole
# blob as binary. In the C locale only NUL marks a binary file.
export LC_ALL=C

die() {
    echo "history-scan: $*" >&2
    exit 2
}

allow_file=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --allow)
            [[ $# -ge 2 ]] || die "--allow needs a file"
            allow_file="$2"
            shift 2
            ;;
        *) die "unknown argument: $1" ;;
    esac
done

top="$(git rev-parse --show-toplevel 2>/dev/null)" || die "not inside a git repository"
if [[ -z "$allow_file" ]]; then
    allow_file="$top/.history-scan-allow"
elif [[ ! -f "$allow_file" ]]; then
    die "allow file not found: $allow_file"
fi

# Commit and tag message patterns, case-insensitive.
MSG_NAMES=(
    "attribution trailer"
    "anthropic address"
    "session url"
    "generated with"
    "anthropic"
    "claude"
)
MSG_PATTERNS=(
    'co-authored-by:.*(claude|anthropic|openai|chatgpt|gpt|copilot|gemini|codex|cursor|devin|aider|llm|assistant|model|bot)'
    'noreply@anthropic[.]com'
    'claude-session[:]'
    'generated[[:space:]]+with'
    'anthropic'
    'claude'
)

# Blob content patterns, case-insensitive. "certificate" is skipped for
# paths inside a testdata directory. "private key" and "certificate" are
# only reported when the PEM block looks real (see pem_hits).
BLOB_NAMES=(
    "anthropic address"
    "session url"
    "attribution trailer"
    "home path"
    "private key"
    "github token"
    "github pat"
    "aws key"
    "slack token"
    "certificate"
)
BLOB_PATTERNS=(
    'noreply@anthropic[.]com'
    'claude-session[:]'
    'co-authored-by:[[:space:]]*claude'
    '/(Users|home)/bawdo/'
    'BEGIN ((RSA|EC|DSA|OPENSSH|PGP|ENCRYPTED) )?PRIVATE KEY'
    'ghp_[A-Za-z0-9]{36}'
    'github_pat_[A-Za-z0-9_]{22,}'
    'AKIA[0-9A-Z]{16}'
    'xox[baprs]-[A-Za-z0-9-]+'
    '-{5}BEGIN CERTIFICATE-{5}'
)

allowed_emails=()
allowed_blobs=()
if [[ -f "$allow_file" ]]; then
    while IFS= read -r line || [[ -n "$line" ]]; do
        line="${line%$'\r'}"
        case "$line" in
            email:*) allowed_emails+=("$(printf '%s' "${line#email:}" | tr '[:upper:]' '[:lower:]')") ;;
            blob:*) allowed_blobs+=("${line#blob:}") ;;
        esac
    done < "$allow_file"
fi

work="$(mktemp -d "${TMPDIR:-/tmp}/history-scan.XXXXXX")"
trap 'rm -rf "$work"' EXIT
hits="$work/hits"
: > "$hits"

hit() {
    # hit ID NAME MATCH: records one hit, truncating long matches.
    printf 'history-scan: %s: %s: %.200s\n' "$1" "$2" "$3" >> "$hits"
}

email_allowed() {
    local e="$1" a
    for a in ${allowed_emails[@]+"${allowed_emails[@]}"}; do
        [[ "$a" == "$e" ]] && return 0
    done
    return 1
}

blob_allowed() {
    local key="$1" a
    for a in ${allowed_blobs[@]+"${allowed_blobs[@]}"}; do
        [[ "$a" == "$key" ]] && return 0
    done
    return 1
}

# Messages: one "<id><TAB><line>" per line of every commit and tag message.
SOH="$(printf '\001')"
{
    git log --all --format="%x01%H%n%B"
    git for-each-ref refs/tags --format="%01%(objectname)%0a%(contents)"
} | awk -v m="$SOH" 'index($0, m) == 1 { id = substr($0, 2); next } { print id "\t" $0 }' > "$work/messages"

for i in "${!MSG_PATTERNS[@]}"; do
    while IFS=$'\t' read -r id text; do
        hit "$id" "${MSG_NAMES[$i]}" "$text"
    done < <(grep -iE -e "${MSG_PATTERNS[$i]}" "$work/messages" || true)
done

# Emails: author and committer of every commit, tagger of every tag.
{
    git log --all --format='c %H %ae%nc %H %ce'
    git for-each-ref refs/tags --format='t %(objectname) %(taggeremail)'
} | sort -u > "$work/emails"
while read -r kind id email; do
    email="${email#<}"
    email="${email%>}"
    if [[ -z "$email" ]]; then
        # A lightweight tag has no tagger; a commit must have an address.
        [[ "$kind" == t ]] && continue
        hit "$id" "unlisted email" "(empty)"
        continue
    fi
    email="$(printf '%s' "$email" | tr '[:upper:]' '[:lower:]')"
    email_allowed "$email" || hit "$id" "unlisted email" "$email"
done < "$work/emails"

# Blobs: every blob reachable from every ref, each scanned once, binaries
# skipped. rev-list names each object once, with the first path it saw.
git rev-list --all --objects |
    git cat-file --batch-check='%(objecttype) %(objectname) %(rest)' |
    awk '$1 == "blob"' > "$work/blobs"

# awk forms of the two PEM patterns, matched against the upper-cased line.
# Written without a run of five dashes so this file does not match itself.
PEM_KEY='BEGIN ((RSA|EC|DSA|OPENSSH|PGP|ENCRYPTED) )?PRIVATE KEY'
PEM_CERT='----*BEGIN CERTIFICATE----*'

# pem_hits REGEX FILE: prints "<line>:<text>" for each BEGIN line matching
# REGEX whose PEM body holds base64 of 40 or more characters, as every
# real key or certificate does. A body is either:
#   - the following lines up to the END line (or end of file): a line counts
#     when, after dropping string wrapping (quotes, commas, semicolons,
#     \n and \r escapes, surrounding whitespace and a + concatenation
#     operator), it is 40 or more base64 characters and nothing else. So
#     '"MIIE...\n" +' counts, while prose or code such as a long Go test
#     function name inside parentheses does not; or
#   - when END is on the same line as BEGIN (a quoted string with \n
#     escapes), the text between them, holding any base64 run of 40 or more.
# Placeholder bodies such as "KEY" are not reported.
pem_hits() {
    awk -v re="$1" -v q="'" '
        function real_line(s) {
            gsub(/\\[nr]/, "", s)
            gsub("[\r\"`,;" q "]", "", s)
            gsub(/^[ \t]+|[ \t]+$/, "", s)
            sub(/^\+[ \t]+/, "", s)
            sub(/[ \t]+\+$/, "", s)
            return length(s) >= 40 && s ~ /^[A-Za-z0-9+\/=]+$/
        }
        function real_run(s,    n, i, parts) {
            n = split(s, parts, /[^A-Za-z0-9+\/=]+/)
            for (i = 1; i <= n; i++) if (length(parts[i]) >= 40) return 1
            return 0
        }
        function close_block() {
            if (open && real) print start
            open = 0
        }
        open {
            if (index(toupper($0), "-----END") > 0) { close_block(); next }
            if (real_line($0)) real = 1
            next
        }
        match(toupper($0), re) {
            rest = substr($0, RSTART + RLENGTH)
            e = index(toupper(rest), "-----END")
            if (e > 0) {
                if (real_run(substr(rest, 1, e - 1))) print NR ":" $0
            } else {
                open = 1; real = 0; start = NR ":" $0
            }
        }
        END { close_block() }
    ' "$2"
}

any="$(IFS='|'; echo "${BLOB_PATTERNS[*]}")"
blob="$work/blob"
while read -r _ id path; do
    git cat-file blob "$id" > "$blob"
    grep -qIiE -e "$any" "$blob" || continue
    for i in "${!BLOB_PATTERNS[@]}"; do
        name="${BLOB_NAMES[$i]}"
        if [[ "$name" == "certificate" && "$path" =~ (^|/)testdata/ ]]; then
            continue
        fi
        blob_allowed "$path:$name" && continue
        while IFS= read -r match; do
            hit "$id:$path" "$name" "$match"
        done < <(
            case "$name" in
                "private key") pem_hits "$PEM_KEY" "$blob" ;;
                certificate) pem_hits "$PEM_CERT" "$blob" ;;
                *) grep -nIiE -e "${BLOB_PATTERNS[$i]}" "$blob" || true ;;
            esac
        )
    done
done < "$work/blobs"

if [[ -s "$hits" ]]; then
    cat "$hits" >&2
    exit 1
fi

commits="$(git rev-list --all --count)"
blobs="$(wc -l < "$work/blobs" | tr -d ' ')"
echo "history-scan: clean ($commits commits, $blobs blobs)"

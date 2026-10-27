#!/usr/bin/env bash
#
# Checks the repository's documents: no em or en dashes in tracked text files
# (docs/superpowers/ holds historical records kept verbatim and is skipped),
# every relative link in root *.md and .github/**/*.md resolves, and the
# required community files exist. Prints "docs-check: <file>:<line>: <reason>"
# to stderr and exits 1 on any problem.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

REQUIRED=(
    LICENSE
    CHANGELOG.md
    CONTRIBUTING.md
    SECURITY.md
    CODE_OF_CONDUCT.md
    .github/PULL_REQUEST_TEMPLATE.md
    .github/CODEOWNERS
    .github/ISSUE_TEMPLATE/bug_report.yml
    .github/ISSUE_TEMPLATE/feature_request.yml
    .github/ISSUE_TEMPLATE/config.yml
    .github/workflows/scorecard.yml
    .codecov.yml
)

status=0
problem() {
    echo "docs-check: $1" >&2
    status=1
}

for f in "${REQUIRED[@]}"; do
    [[ -f "$f" ]] || problem "$f: required file missing"
done

# Byte sequences for U+2013 and U+2014, so the match does not depend on locale.
DASHES=$'\xe2\x80\x93|\xe2\x80\x94'

while IFS= read -r -d '' f; do
    [[ -f "$f" ]] || continue
    case "$f" in docs/superpowers/*) continue ;; esac
    while IFS=: read -r n _; do
        [[ -n "$n" ]] && problem "$f:$n: em or en dash"
    done < <(grep -nIE "$DASHES" "$f" || true)
done < <(git ls-files -z)

while IFS= read -r -d '' f; do
    [[ -f "$f" ]] || continue
    case "$f" in
        *.md) ;;
        *) continue ;;
    esac
    case "$f" in
        */*) [[ "$f" == .github/* ]] || continue ;;
    esac
    dir="$(dirname "$f")"
    while IFS=: read -r n match; do
        target="${match#](}"
        target="${target%)}"
        case "$target" in
            http* | mailto:* | '#'*) continue ;;
        esac
        target="${target%%#*}"
        [[ -z "$target" ]] && continue
        [[ -e "$dir/$target" ]] || problem "$f:$n: broken link $target"
    done < <(grep -noE '\]\([^)]+\)' "$f" || true)
done < <(git ls-files -z)

if [[ $status -eq 0 ]]; then
    echo "docs are consistent"
fi
exit $status

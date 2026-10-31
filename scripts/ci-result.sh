#!/usr/bin/env bash
#
# CI result gate: fails unless every expected job passed.
#
#   EVENT=<github.event_name> NEEDS=<toJSON(needs)> scripts/ci-result.sh
#   scripts/ci-result.sh --jobs     list the expected job names, one per line
#
# A job passes on "success". A "skipped" result passes only where the event
# allows it: pull_request and push may skip fuzz, schedule may skip static,
# test, integration and build, and every other event may skip nothing. A job
# missing from NEEDS fails too, as does any extra job NEEDS reports.

set -euo pipefail

JOBS=(static vuln test integration build fuzz)

die() {
    echo "ci-result: $*" >&2
    exit 1
}

if [ "${1:-}" = "--jobs" ]; then
    printf '%s\n' "${JOBS[@]}"
    exit 0
fi

[ -n "${EVENT:-}" ] || die "EVENT is not set"
[ -n "${NEEDS:-}" ] || die "NEEDS is not set"

case "$EVENT" in
    pull_request|push) may_skip='["fuzz"]' ;;
    schedule) may_skip='["static","test","integration","build"]' ;;
    *) may_skip='[]' ;;
esac

all=$(printf '%s\n' "${JOBS[@]}" | jq -R . | jq -sc .)

echo "Event: $EVENT (may skip: $may_skip)"
jq -r 'if type == "object" then to_entries[] | "  \(.key): \(.value.result)" else empty end' <<<"$NEEDS"

bad=$(jq -r --argjson all "$all" --argjson skip "$may_skip" '
    if type != "object" then "  job results are not an object"
    else . as $needs | ($all + keys | unique)[]
        | . as $job | ($needs[$job].result // "missing") as $r
        | if $r == "success" then empty
          elif $r == "skipped" and ($skip | any(. == $job)) then empty
          else "  \($job): \($r)" end
    end' <<<"$NEEDS")

if [ -n "$bad" ]; then
    echo "Not passed:"
    echo "$bad"
    exit 1
fi
echo "Every expected job passed."

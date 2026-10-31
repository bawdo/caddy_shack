#!/usr/bin/env bash
#
# Tests for scripts/ci-result.sh. Each case builds a NEEDS object, runs the
# script for an event and asserts on stdout and exit code.
# Prints "ok <name>" per case and exits non-zero on the first failure.

set -euo pipefail

SCRIPT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/ci-result.sh"

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

# needs JOB=RESULT...: prints a NEEDS object with those jobs.
needs() {
    local json="" pair
    for pair in "$@"; do
        json+="${json:+,}\"${pair%%=*}\":{\"result\":\"${pair#*=}\"}"
    done
    echo "{$json}"
}

JOBS=(static vuln test integration build fuzz)

# all_success [JOB=RESULT...]: the six expected jobs as success, with overrides.
all_success() {
    local job over pairs=()
    for job in "${JOBS[@]}"; do
        for over in "$@"; do
            if [ "${over%%=*}" = "$job" ]; then
                pairs+=("$over")
                continue 2
            fi
        done
        pairs+=("$job=success")
    done
    for over in "$@"; do
        case " ${JOBS[*]} " in *" ${over%%=*} "*) ;; *) pairs+=("$over") ;; esac
    done
    needs "${pairs[@]}"
}

# run EVENT JSON: runs the script, capturing OUT, ERR and CODE.
run() {
    local errfile
    errfile="$(mktemp "${TMPDIR:-/tmp}/ci-result_test.XXXXXX")"
    CODE=0
    OUT=$(EVENT="$1" NEEDS="$2" bash "$SCRIPT" 2>"$errfile") || CODE=$?
    ERR=$(cat "$errfile")
    rm -f "$errfile"
}

expect_code() {
    [ "$CODE" -eq "$2" ] || fail "$1" "expected exit $2"
}

expect_line() {
    printf '%s\n' "$OUT" | grep -qxF -- "$2" || fail "$1" "missing line: $2"
}

t=pr_passes_with_fuzz_skipped
run pull_request "$(all_success fuzz=skipped)"
expect_code $t 0
[ "${OUT##*$'\n'}" = "Every expected job passed." ] || fail $t "last line wrong"
echo "ok $t"

t=push_passes_with_fuzz_skipped
run push "$(all_success fuzz=skipped)"
expect_code $t 0
echo "ok $t"

t=pr_passes_with_fuzz_run
run pull_request "$(all_success)"
expect_code $t 0
echo "ok $t"

t=pr_fails_on_skipped_static
run pull_request "$(all_success static=skipped)"
expect_code $t 1
expect_line $t "  static: skipped"
echo "ok $t"

t=schedule_passes_with_vuln_and_fuzz_only
run schedule "$(all_success static=skipped test=skipped integration=skipped build=skipped)"
expect_code $t 0
echo "ok $t"

t=schedule_fails_on_vuln_failure
run schedule "$(all_success static=skipped test=skipped integration=skipped build=skipped vuln=failure)"
expect_code $t 1
expect_line $t "  vuln: failure"
echo "ok $t"

t=dispatch_fails_on_skipped_fuzz
run workflow_dispatch "$(all_success fuzz=skipped)"
expect_code $t 1
echo "ok $t"

t=unknown_event_expects_everything
run merge_group "$(all_success fuzz=skipped)"
expect_code $t 1
echo "ok $t"

t=missing_job_fails
run pull_request "$(needs static=success vuln=success test=success build=success fuzz=success)"
expect_code $t 1
expect_line $t "  integration: missing"
echo "ok $t"

t=cancelled_job_fails
run pull_request "$(all_success build=cancelled)"
expect_code $t 1
expect_line $t "  build: cancelled"
echo "ok $t"

t=extra_job_is_expected
run pull_request "$(all_success lint=skipped)"
expect_code $t 1
expect_line $t "  lint: skipped"
echo "ok $t"

t=needs_not_an_object_fails
run pull_request '[]'
expect_code $t 1
expect_line $t "  job results are not an object"
echo "ok $t"

t=jobs_lists_expected_order
CODE=0
OUT=$(bash "$SCRIPT" --jobs 2>&1) || CODE=$?
expect_code $t 0
[ "$OUT" = "$(printf 'static\nvuln\ntest\nintegration\nbuild\nfuzz')" ] || fail $t "wrong job list"
echo "ok $t"

WORKFLOW="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)/.github/workflows/ci.yml"

# The needs list of the ci-result job must name exactly the jobs the script
# expects, so adding a job to one without the other fails here.
t=ci_yml_needs_matches_jobs
CODE=0
expected=$(bash "$SCRIPT" --jobs | paste -sd, - | sed 's/,/, /g')
actual=$(sed -n '/^  ci-result:/,$p' "$WORKFLOW" | sed -n 's/^    needs: \[\(.*\)\]$/\1/p')
[ "[$actual]" = "[$expected]" ] || fail $t "needs is [$actual], script expects [$expected]"
echo "ok $t"

# A scheduled run needs its own concurrency group so its long fuzz run never
# queues or cancels a push to main on the same ref.
t=ci_yml_schedule_has_own_concurrency_group
CODE=0
grep -qxF "  group: ci-\${{ github.event_name == 'schedule' && 'schedule-' || '' }}\${{ github.ref }}" "$WORKFLOW" \
    || fail $t "concurrency group line missing from ci.yml"
echo "ok $t"

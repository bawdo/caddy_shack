#!/usr/bin/env bash
#
# Applies the GitHub repository settings for going public, one phase at a
# time:
#
#   scripts/repo-settings.sh pre-public    description, homepage, flags, topics
#   GO_PUBLIC=yes scripts/repo-settings.sh public
#                                          makes the repository public
#   scripts/repo-settings.sh post-public   vulnerability reporting, secret
#                                          scanning, Dependabot alerts and fixes
#   scripts/repo-settings.sh rulesets      the main and release-tags rulesets
#   scripts/repo-settings.sh show          prints the current settings
#
# REPO (default bawdo/caddy_shack) names the repository. Every write sends the
# full desired state (PATCH or PUT; rulesets are found by name and replaced in
# place), so any phase can be re-run. Each write prints its method and path
# before it runs. With DRY_RUN set, writes are printed with their bodies and
# not sent; reads still run where a phase needs them to work out its writes.

set -euo pipefail

REPO="${REPO:-bawdo/caddy_shack}"
R="repos/$REPO"

die() {
    echo "repo-settings: $*" >&2
    exit 1
}

WORK="$(mktemp -d "${TMPDIR:-/tmp}/repo-settings.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

# write METHOD PATH [BODY]: prints "METHOD PATH" and sends it, with BODY (a
# JSON string) as the request body when given. Under DRY_RUN it prints the
# body too and sends nothing. This is the only place a write is sent.
write() {
    local method="$1" path="$2" body="${3:-}" file
    echo "$method $path"
    if [[ -n "$body" ]]; then
        file="$WORK/body.json"
        jq . <<<"$body" > "$file"
    fi
    if [[ -n "${DRY_RUN:-}" ]]; then
        [[ -n "$body" ]] && sed 's/^/    /' "$file"
        return 0
    fi
    local args=(api -X "$method" "$path") out code=0
    [[ -n "$body" ]] && args+=(--input "$file")
    # On failure gh prints the response body (with GitHub's errors array
    # naming any rejected field) on stdout, so keep it for the stderr report.
    out=$(gh "${args[@]}") || code=$?
    if [[ $code -ne 0 ]]; then
        [[ -n "$out" ]] && echo "$out" >&2
        die "$method $path failed (exit $code)"
    fi
}

pre_public() {
    write PATCH "$R" "$(jq -n '{
        description: "A CLI that runs a local Caddy in Docker and gives your projects stable HTTPS URLs under .test",
        homepage: "https://github.com/bawdo/caddy_shack",
        delete_branch_on_merge: true,
        has_wiki: false,
        has_projects: false,
        has_discussions: false,
        allow_update_branch: true
    }')"
    write PUT "$R/topics" "$(jq -n '{names: ["caddy", "go", "golang", "cli", "docker", "https",
        "local-development", "reverse-proxy", "tls"]}')"
    if [[ -n "${DRY_RUN:-}" ]]; then
        echo "(DRY_RUN: not checking for the CODECOV_TOKEN secret)"
    elif ! gh secret list --repo "$REPO" --json name --jq '.[].name' 2>/dev/null | grep -qx CODECOV_TOKEN; then
        echo "repo-settings: warning: no CODECOV_TOKEN secret; add it with gh secret set CODECOV_TOKEN --repo $REPO" >&2
    fi
}

public() {
    [[ "${GO_PUBLIC:-}" == yes ]] || die "set GO_PUBLIC=yes to make the repository public"
    write PATCH "$R" '{"visibility":"public"}'
}

post_public() {
    write PUT "$R/private-vulnerability-reporting"
    write PATCH "$R" '{"security_and_analysis":{"secret_scanning":{"status":"enabled"},"secret_scanning_push_protection":{"status":"enabled"}}}'
    write PUT "$R/vulnerability-alerts"
    write PUT "$R/automated-security-fixes"
}

# ruleset NAME BODY: replaces the ruleset called NAME when it exists,
# otherwise creates it.
ruleset() {
    local id
    id=$(gh api "$R/rulesets" | jq -r --arg n "$1" '[.[] | select(.name == $n) | .id][0] // empty')
    if [[ -n "$id" ]]; then
        write PUT "$R/rulesets/$id" "$2"
    else
        write POST "$R/rulesets" "$2"
    fi
}

rulesets() {
    local run_id contexts n
    run_id=$(gh run list --repo "$REPO" --workflow ci.yml --branch main --status success --limit 1 \
        --json databaseId --jq '.[0].databaseId // empty')
    [[ -n "$run_id" ]] || die "no successful CI run on main yet; merge and let CI pass first"
    contexts=$(gh api "$R/actions/runs/$run_id/jobs" --jq '.jobs[].name' | sort -u)
    n=$(grep -c . <<<"$contexts" || true)
    [[ "$n" -eq 5 ]] || die "expected 5 status check contexts, found $n"

    ruleset main "$(jq -n --arg contexts "$contexts" '{
        name: "main",
        target: "branch",
        enforcement: "active",
        bypass_actors: [{actor_id: 5, actor_type: "RepositoryRole", bypass_mode: "always"}],
        conditions: {ref_name: {include: ["refs/heads/main"], exclude: []}},
        rules: [
            {type: "deletion"},
            {type: "non_fast_forward"},
            {type: "pull_request", parameters: {
                required_approving_review_count: 0,
                dismiss_stale_reviews_on_push: false,
                require_code_owner_review: false,
                require_last_push_approval: false,
                required_review_thread_resolution: false
            }},
            {type: "required_status_checks", parameters: {
                strict_required_status_checks_policy: true,
                required_status_checks: ($contexts | split("\n") | map({context: .}))
            }}
        ]
    }')"
    ruleset release-tags "$(jq -n '{
        name: "release-tags",
        target: "tag",
        enforcement: "active",
        bypass_actors: [],
        conditions: {ref_name: {include: ["refs/tags/v*"], exclude: []}},
        rules: [{type: "deletion"}, {type: "non_fast_forward"}, {type: "update"}]
    }')"
}

# get PATH JQ FALLBACK: prints GET PATH filtered by JQ, or FALLBACK when the
# read fails (gh prints the error body on stdout, so it is discarded).
get() {
    local out
    if out=$(gh api "$1" --jq "$2" 2>/dev/null); then echo "$out"; else echo "$3"; fi
}

# enabled PATH: "enabled" when GET PATH succeeds (204), else "disabled".
enabled() {
    if gh api "$1" --silent >/dev/null 2>&1; then echo enabled; else echo disabled; fi
}

show() {
    get "$R" '
        "visibility: \(.visibility)",
        "description: \(.description)",
        "homepage: \(.homepage)",
        "delete_branch_on_merge: \(.delete_branch_on_merge)",
        "has_wiki: \(.has_wiki)",
        "has_projects: \(.has_projects)",
        "has_discussions: \(.has_discussions)",
        "allow_update_branch: \(.allow_update_branch)",
        "topics: \(.topics | join(" "))",
        "secret_scanning: \(.security_and_analysis.secret_scanning.status // "unknown")",
        "secret_scanning_push_protection: \(.security_and_analysis.secret_scanning_push_protection.status // "unknown")"' \
        "repo-settings: cannot read $R"
    local onoff='if .enabled then "enabled" else "disabled" end'
    echo "private_vulnerability_reporting: $(get "$R/private-vulnerability-reporting" "$onoff" unknown)"
    echo "vulnerability_alerts: $(enabled "$R/vulnerability-alerts")"
    echo "automated_security_fixes: $(get "$R/automated-security-fixes" "$onoff" disabled)"
    echo "rulesets: $(get "$R/rulesets" '[.[].name] | join(" ")' unavailable)"
}

case "${1:-}" in
    pre-public) pre_public ;;
    public) public ;;
    post-public) post_public ;;
    rulesets) rulesets ;;
    show) show ;;
    *) die "usage: repo-settings.sh pre-public|public|post-public|rulesets|show" ;;
esac

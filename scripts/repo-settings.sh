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
#   scripts/repo-settings.sh rulesets      the main, release-tags and
#                                          release-tag-creation rulesets; main
#                                          requires the CI result job and
#                                          CodeQL results, with no bypass
#                                          actors; only admins can create v*
#                                          tags
#   scripts/repo-settings.sh harden        Actions allowlist and SHA pinning,
#                                          fork approval, merge methods, CodeQL,
#                                          immutable releases
#   scripts/repo-settings.sh show          prints the current settings
#
# REPO (default bawdo/caddy_shack) names the repository. Every write sends the
# full desired state (PATCH or PUT; rulesets are found by name and replaced in
# place), so any phase can be re-run. Each write prints its method and path
# before it runs. With DRY_RUN set, writes are printed with their bodies and
# not sent; reads still run where a phase needs them to work out its writes.
# WORKFLOWS_DIR (default .github/workflows next to this script's repository)
# is where the harden phase reads the actions in use.

set -euo pipefail

REPO="${REPO:-bawdo/caddy_shack}"
R="repos/$REPO"
WORKFLOWS_DIR="${WORKFLOWS_DIR:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)/.github/workflows}"

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
    local run_id jobs
    run_id=$(gh run list --repo "$REPO" --workflow ci.yml --branch main --event push --status success --limit 1 \
        --json databaseId --jq '.[0].databaseId // empty')
    [[ -n "$run_id" ]] || die "no successful CI run on main yet; merge and let CI pass first"
    jobs=$(gh api "$R/actions/runs/$run_id/jobs" --jq '.jobs[].name')
    grep -qxF 'CI result' <<<"$jobs" \
        || die "no CI result job in the latest successful CI run on main; merge the CI gate first"

    # Only the CI result job is required: it fails unless every other job
    # passed. integration_id 15368 is the GitHub Actions app, so only Actions
    # can report that check.
    ruleset main "$(jq -n '{
        name: "main",
        target: "branch",
        enforcement: "active",
        bypass_actors: [],
        conditions: {ref_name: {include: ["~DEFAULT_BRANCH"], exclude: []}},
        rules: [
            {type: "deletion"},
            {type: "non_fast_forward"},
            {type: "required_signatures"},
            {type: "pull_request", parameters: {
                required_approving_review_count: 0,
                dismiss_stale_reviews_on_push: false,
                require_code_owner_review: false,
                require_last_push_approval: false,
                required_review_thread_resolution: true,
                allowed_merge_methods: ["merge"]
            }},
            {type: "required_status_checks", parameters: {
                strict_required_status_checks_policy: true,
                required_status_checks: [{context: "CI result", integration_id: 15368}]
            }},
            {type: "code_scanning", parameters: {
                code_scanning_tools: [{
                    tool: "CodeQL",
                    security_alerts_threshold: "high_or_higher",
                    alerts_threshold: "errors"
                }]
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
    # Only the admin role (RepositoryRole 5, the same id this repository's
    # main ruleset used for admin before this branch) can create release
    # tags, so write access alone cannot start a release from an unmerged
    # commit. It is a separate ruleset so that update and deletion above keep
    # no bypass at all.
    ruleset release-tag-creation "$(jq -n '{
        name: "release-tag-creation",
        target: "tag",
        enforcement: "active",
        bypass_actors: [{actor_id: 5, actor_type: "RepositoryRole", bypass_mode: "always"}],
        conditions: {ref_name: {include: ["refs/tags/v*"], exclude: []}},
        rules: [{type: "creation"}]
    }')"
}

# third_party_actions: prints "owner/repo@*" for every action the workflows
# use that GitHub does not own (actions/* and github/*), one per line, sorted
# and deduplicated. Local reusable workflows (./...) are not actions. Dies if
# there are no workflows or an action is not pinned to a 40-hex commit SHA,
# since the harden phase makes GitHub refuse such a workflow anyway.
third_party_actions() {
    local files file ref name
    files=$(find "$WORKFLOWS_DIR" -maxdepth 1 \( -name '*.yml' -o -name '*.yaml' \) 2>/dev/null | sort)
    [[ -n "$files" ]] || die "no workflows found in $WORKFLOWS_DIR"
    while IFS= read -r file; do
        while IFS= read -r ref; do
            [[ "$ref" == ./* ]] && continue
            [[ "$ref" =~ ^([^@]+)@([0-9a-f]{40})$ ]] \
                || die "$(basename "$file"): $ref is not pinned to a commit SHA"
            name="${BASH_REMATCH[1]}"
            case "$name" in
                actions/* | github/*) continue ;;
            esac
            # owner/repo only: a path inside the repository shares its allowlist entry.
            echo "$(cut -d/ -f1,2 <<<"$name")@*"
        done < <(sed -nE 's/^[[:space:]]*-?[[:space:]]*uses:[[:space:]]*([^[:space:]#]+).*/\1/p' "$file")
    done <<<"$files" | sort -u
}

harden() {
    local patterns
    patterns=$(third_party_actions)
    # allowed_actions must be "selected" before the selection can be set.
    write PUT "$R/actions/permissions" '{"enabled":true,"allowed_actions":"selected","sha_pinning_required":true}'
    write PUT "$R/actions/permissions/selected-actions" "$(jq -n --arg p "$patterns" '{
        github_owned_allowed: true,
        verified_allowed: false,
        patterns_allowed: ($p | split("\n") | map(select(. != "")))
    }')"
    write PUT "$R/actions/permissions/fork-pr-contributor-approval" '{"approval_policy":"all_external_contributors"}'
    # Merge commits only, so every commit on main keeps its author's signature
    # and the history matches what the release gate checks.
    write PATCH "$R" '{"allow_merge_commit":true,"allow_squash_merge":false,"allow_rebase_merge":false,"allow_auto_merge":true}'
    write PATCH "$R/code-scanning/default-setup" '{"state":"configured","query_suite":"extended","languages":["actions","go"]}'
    # Once published, a release's tag and assets cannot change. The publish
    # script creates a draft, attaches the assets, then publishes, which fits.
    write PUT "$R/immutable-releases"
}

# read_api PATH JQ: prints GET PATH filtered by JQ. When the read fails it
# says so on stderr and returns 1 (gh prints the error body on stdout, so it
# is discarded).
read_api() {
    local out
    if out=$(gh api "$1" --jq "$2" 2>/dev/null); then
        echo "$out"
    else
        echo "repo-settings: cannot read $1" >&2
        return 1
    fi
}

# get PATH JQ FALLBACK: prints GET PATH filtered by JQ, or FALLBACK when the
# read fails, for reads where a failure means the feature is off.
get() {
    local out
    if out=$(gh api "$1" --jq "$2" 2>/dev/null); then echo "$out"; else echo "$3"; fi
}

# enabled PATH: "enabled" when GET PATH succeeds (204), else "disabled".
enabled() {
    if gh api "$1" --silent >/dev/null 2>&1; then echo enabled; else echo disabled; fi
}

# show: prints what it can read, then exits 1 if any read failed.
show() {
    local failed=0 value
    read_api "$R" '
        "visibility: \(.visibility)",
        "description: \(.description)",
        "homepage: \(.homepage)",
        "delete_branch_on_merge: \(.delete_branch_on_merge)",
        "has_wiki: \(.has_wiki)",
        "has_projects: \(.has_projects)",
        "has_discussions: \(.has_discussions)",
        "allow_update_branch: \(.allow_update_branch)",
        "merge_methods: \([["merge", .allow_merge_commit], ["squash", .allow_squash_merge], ["rebase", .allow_rebase_merge]] | map(select(.[1]) | .[0]) | join(" "))",
        "allow_auto_merge: \(.allow_auto_merge)",
        "topics: \(.topics | join(" "))",
        "secret_scanning: \(.security_and_analysis.secret_scanning.status // "unknown")",
        "secret_scanning_push_protection: \(.security_and_analysis.secret_scanning_push_protection.status // "unknown")"' \
        || failed=1
    local onoff='if .enabled then "enabled" else "disabled" end'
    value=$(read_api "$R/private-vulnerability-reporting" "$onoff") || { value=unknown; failed=1; }
    echo "private_vulnerability_reporting: $value"
    echo "vulnerability_alerts: $(enabled "$R/vulnerability-alerts")"
    echo "automated_security_fixes: $(get "$R/automated-security-fixes" "$onoff" disabled)"
    value=$(read_api "$R/rulesets" '[.[].name] | join(" ")') || { value=unavailable; failed=1; }
    echo "rulesets: $value"
    echo "allowed_actions: $(get "$R/actions/permissions" '.allowed_actions' unknown)"
    echo "sha_pinning_required: $(get "$R/actions/permissions" '.sha_pinning_required' unknown)"
    echo "fork_pr_approval: $(get "$R/actions/permissions/fork-pr-contributor-approval" '.approval_policy' unknown)"
    echo "code_scanning_default_setup: $(get "$R/code-scanning/default-setup" '.state' unknown)"
    echo "code_scanning_query_suite: $(get "$R/code-scanning/default-setup" '.query_suite' unknown)"
    # GET answers 200 with enabled false when off (the docs say 404), so both
    # fall back to disabled.
    echo "immutable_releases: $(get "$R/immutable-releases" "$onoff" disabled)"
    return "$failed"
}

case "${1:-}" in
    pre-public) pre_public ;;
    public) public ;;
    post-public) post_public ;;
    rulesets) rulesets ;;
    harden) harden ;;
    show) show ;;
    *) die "usage: repo-settings.sh pre-public|public|post-public|rulesets|harden|show" ;;
esac

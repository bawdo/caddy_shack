#!/usr/bin/env bash
#
# Tests for scripts/repo-settings.sh. Every gh call goes to a fake gh put
# first on PATH, which logs its arguments, copies any --input body to a
# second log and answers reads from canned files, so nothing here touches
# the network or changes a real repository. Prints "ok <name>" per case and
# exits non-zero on the first failure.

set -euo pipefail

SCRIPTS="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/repo-settings_test.XXXXXX")"
trap 'rm -rf "$ROOT"' EXIT

R=repos/bawdo/caddy_shack

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

# The fake gh. FAKE_GH_DIR holds the canned answers and the logs:
#   runs.json      what "run list" prints
#   jobs.json      body of GET .../actions/runs/ID/jobs
#   rulesets.json  body of GET .../rulesets
#   secrets.json   what "secret list" prints
#   gh.log         one line per call, the arguments joined by spaces
#   bodies.log     one line per --input body: "METHOD PATH <compact json>"
mkdir -p "$ROOT/bin"
cat > "$ROOT/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
echo "$*" >> "$FAKE_GH_DIR/gh.log"
jq_expr=""
input=""
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
    case "${args[$i]}" in
        --jq) jq_expr="${args[$((i + 1))]}" ;;
        --input) input="${args[$((i + 1))]}" ;;
    esac
done
answer() {
    if [ -n "$jq_expr" ]; then jq -r "$jq_expr" "$1"; else cat "$1"; fi
}
case "$1 $2" in
    "run list") answer "$FAKE_GH_DIR/runs.json"; exit 0 ;;
    "secret list") answer "$FAKE_GH_DIR/secrets.json"; exit 0 ;;
    "api -X")
        method="$3"
        path="$4"
        ;;
    "api "*)
        method=GET
        path="$2"
        ;;
    *)
        echo "fake gh: unexpected call: $*" >&2
        exit 2
        ;;
esac
if [ "$method" != GET ]; then
    if [ -n "$input" ]; then
        echo "$method $path $(jq -c . "$input")" >> "$FAKE_GH_DIR/bodies.log"
    fi
    echo '{}'
    exit 0
fi
case "$path" in
    */actions/runs/*/jobs) answer "$FAKE_GH_DIR/jobs.json" ;;
    */rulesets) answer "$FAKE_GH_DIR/rulesets.json" ;;
    *)
        echo "fake gh: unexpected read: $*" >&2
        exit 2
        ;;
esac
EOF
chmod +x "$ROOT/bin/gh"

FIVE='{"jobs":[{"name":"Static checks"},{"name":"Test (ubuntu-latest)"},{"name":"Test (macos-latest)"},{"name":"Integration tests"},{"name":"Build and smoke"}]}'

# fake NAME: a fresh directory of canned answers (a successful CI run with
# five jobs, no rulesets, no secrets) and empty logs. Prints its path.
fake() {
    local dir="$ROOT/$1"
    mkdir -p "$dir"
    : > "$dir/gh.log"
    : > "$dir/bodies.log"
    echo '[{"databaseId":42}]' > "$dir/runs.json"
    echo "$FIVE" > "$dir/jobs.json"
    echo '[]' > "$dir/rulesets.json"
    echo '[]' > "$dir/secrets.json"
    echo "$dir"
}

# run DIR [VAR=VALUE...] PHASE: runs repo-settings.sh PHASE with the fake gh
# first on PATH and any extra environment, capturing OUT, ERR and CODE.
run() {
    local dir="$1"
    shift
    CODE=0
    OUT=$(env -u DRY_RUN -u GO_PUBLIC -u REPO PATH="$ROOT/bin:$PATH" FAKE_GH_DIR="$dir" \
        "${@:1:$#-1}" bash "$SCRIPTS/repo-settings.sh" "${!#}" 2>"$dir/.stderr") || CODE=$?
    ERR=$(cat "$dir/.stderr")
}

# body NAME DIR METHOD PATH JQ: asserts the first body sent with METHOD to
# PATH satisfies the jq filter JQ.
body() {
    local json
    json=$(awk -v m="$3" -v p="$4" '$1 == m && $2 == p { $1 = ""; $2 = ""; print; exit }' "$2/bodies.log")
    [ -n "$json" ] || fail "$1" "no $3 $4 body: $(cat "$2/bodies.log")"
    jq -e "$5" <<<"$json" >/dev/null || fail "$1" "$3 $4 body fails $5: $json"
}

# count DIR PATTERN: how many gh log lines contain PATTERN.
count() {
    grep -cF -- "$2" "$1/gh.log" || true
}

name=pre_public_patches_settings_and_topics
dir=$(fake "$name")
run "$dir" pre-public
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
grep -qx -- "api -X PATCH $R --input .*" "$dir/gh.log" || fail "$name" "no PATCH $R"
grep -q -- "^api -X PUT $R/topics --input " "$dir/gh.log" || fail "$name" "no PUT $R/topics"
body "$name" "$dir" PATCH "$R" '.delete_branch_on_merge == true and .has_projects == false
    and .has_wiki == false and .has_discussions == false and .allow_update_branch == true
    and (.description | startswith("A CLI that runs a local Caddy"))'
body "$name" "$dir" PUT "$R/topics" '.names | length == 9 and index("local-development") != null'
grep -qF "CODECOV_TOKEN" <<<"$ERR" || fail "$name" "no warning about the missing CODECOV_TOKEN"
echo "ok $name"

name=public_refuses_without_go_public
dir=$(fake "$name")
run "$dir" public
[ "$CODE" -eq 1 ] || fail "$name" "expected exit 1"
[ "$ERR" = "repo-settings: set GO_PUBLIC=yes to make the repository public" ] || fail "$name" "wrong refusal"
[ "$(count "$dir" PATCH)" -eq 0 ] || fail "$name" "sent a PATCH"
echo "ok $name"

name=public_patches_visibility
dir=$(fake "$name")
run "$dir" GO_PUBLIC=yes public
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
body "$name" "$dir" PATCH "$R" '.visibility == "public"'
echo "ok $name"

name=rulesets_needs_five_contexts
dir=$(fake "$name")
jq '.jobs |= .[0:4]' <<<"$FIVE" > "$dir/jobs.json"
run "$dir" rulesets
[ "$CODE" -eq 1 ] || fail "$name" "expected exit 1"
[ "$ERR" = "repo-settings: expected 5 status check contexts, found 4" ] || fail "$name" "wrong refusal"
[ "$(count "$dir" POST)" -eq 0 ] && [ "$(count "$dir" "-X PUT")" -eq 0 ] || fail "$name" "wrote a ruleset"
echo "ok $name"

name=rulesets_creates_when_absent
dir=$(fake "$name")
run "$dir" rulesets
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
[ "$(count "$dir" "api -X POST $R/rulesets --input")" -eq 2 ] || fail "$name" "expected two POSTs"
body "$name" "$dir" POST "$R/rulesets" '.name == "main" and .target == "branch" and .enforcement == "active"
    and .conditions.ref_name.include == ["refs/heads/main"]
    and .bypass_actors == [{"actor_id":5,"actor_type":"RepositoryRole","bypass_mode":"always"}]
    and ([.rules[].type] == ["deletion","non_fast_forward","pull_request","required_status_checks"])
    and (.rules[2].parameters.required_approving_review_count == 0)
    and (.rules[3].parameters.strict_required_status_checks_policy == true)
    and ([.rules[3].parameters.required_status_checks[].context] | length == 5 and index("Build and smoke") != null)'
tag_body=$(grep '^POST ' "$dir/bodies.log" | sed -n 2p | cut -d' ' -f3-)
jq -e '.name == "release-tags" and .target == "tag" and .enforcement == "active" and .bypass_actors == []
    and .conditions.ref_name.include == ["refs/tags/v*"]
    and ([.rules[].type] == ["deletion","non_fast_forward","update"])' <<<"$tag_body" >/dev/null \
    || fail "$name" "wrong tag ruleset: $tag_body"
echo "ok $name"

name=rulesets_updates_when_present
dir=$(fake "$name")
echo '[{"id":101,"name":"main","target":"branch"},{"id":202,"name":"release-tags","target":"tag"}]' > "$dir/rulesets.json"
run "$dir" rulesets
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
[ "$(count "$dir" "api -X PUT $R/rulesets/101 --input")" -eq 1 ] || fail "$name" "main not updated in place"
[ "$(count "$dir" "api -X PUT $R/rulesets/202 --input")" -eq 1 ] || fail "$name" "release-tags not updated in place"
[ "$(count "$dir" POST)" -eq 0 ] || fail "$name" "created a ruleset"
body "$name" "$dir" PUT "$R/rulesets/101" '.name == "main"'
body "$name" "$dir" PUT "$R/rulesets/202" '.name == "release-tags"'
echo "ok $name"

name=dry_run_runs_nothing
dir=$(fake "$name")
run "$dir" DRY_RUN=1 pre-public
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
[ ! -s "$dir/gh.log" ] || fail "$name" "ran gh: $(cat "$dir/gh.log")"
grep -qx "PATCH $R" <<<"$OUT" || fail "$name" "stdout does not list PATCH $R"
grep -qx "PUT $R/topics" <<<"$OUT" || fail "$name" "stdout does not list PUT $R/topics"
echo "ok $name"

name=show_fails_on_read_error
dir=$(fake "$name")
run "$dir" show
[ "$CODE" -eq 1 ] || fail "$name" "expected exit 1"
grep -qx "repo-settings: cannot read $R" <<<"$ERR" || fail "$name" "no read error for $R on stderr"
grep -qx "repo-settings: cannot read $R/private-vulnerability-reporting" <<<"$ERR" \
    || fail "$name" "no read error for private-vulnerability-reporting on stderr"
! grep -q "cannot read" <<<"$OUT" || fail "$name" "read error on stdout"
grep -qx "private_vulnerability_reporting: unknown" <<<"$OUT" || fail "$name" "no private_vulnerability_reporting line"
grep -qx "rulesets: " <<<"$OUT" || fail "$name" "did not print the rulesets it could read"
echo "ok $name"

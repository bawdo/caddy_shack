#!/usr/bin/env bash
#
# Tests for scripts/outdated.sh. Every go call goes to a fake go put first on
# PATH, which logs its arguments and answers module queries from canned
# files, so nothing here touches the network or the module cache. Only
# "go mod edit -json", which just parses the fixture's go.mod, is handed to
# the real go. The script runs without a terminal, so these cover the report
# and never reach the upgrade prompt. Prints "ok <name>" per case and exits
# non-zero on the first failure.

set -euo pipefail

SCRIPT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/outdated.sh"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/outdated_test.XXXXXX")"
trap 'rm -rf "$ROOT"' EXIT

REAL_GO="$(command -v go)"
export REAL_GO

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

# The fake go. FAKE_GO_DIR holds the canned answers and the log:
#   all.json         what "list -m -u -json all" prints
#   all.err          when it exists, that call prints it to stderr and fails
#   latest/<p>.json  what "list -m -json <path>@latest" prints, with every
#                    "/" in the path written as "_"; no file means no such
#                    module, which fails as the proxy's 404 does
#   go_latest        what "list -m -f {{.Version}} go@latest" prints
#   go.log           one line per call, the arguments joined by spaces
mkdir -p "$ROOT/bin"
cat > "$ROOT/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
echo "$*" >> "$FAKE_GO_DIR/go.log"
case "$*" in
    "mod edit -json") exec "$REAL_GO" mod edit -json ;;
    "list -m -u -json all")
        if [ -f "$FAKE_GO_DIR/all.err" ]; then
            cat "$FAKE_GO_DIR/all.err" >&2
            exit 1
        fi
        cat "$FAKE_GO_DIR/all.json"
        ;;
    "list -m -f {{.Version}} go@latest")
        [ -f "$FAKE_GO_DIR/go_latest" ] || exit 1
        cat "$FAKE_GO_DIR/go_latest"
        ;;
    "list -m -json "*@latest)
        mod="${4%@latest}"
        file="$FAKE_GO_DIR/latest/${mod//\//_}.json"
        [ -f "$file" ] || { echo "go: module $mod: not found" >&2; exit 1; }
        cat "$file"
        ;;
    *)
        echo "fake go: unexpected call: $*" >&2
        exit 2
        ;;
esac
EOF
chmod +x "$ROOT/bin/go"

OLD="2020-01-01T00:00:00Z"
FRESH="$(jq -rn 'now - 2 * 86400 | todate')"

# latest DIR PATH VERSION [TIME]: cans the answer for PATH@latest.
latest() {
    mkdir -p "$1/latest"
    printf '{"Path":"%s","Version":"%s","Time":"%s"}\n' "$2" "$3" "${4:-$OLD}" \
        > "$1/latest/${2//\//_}.json"
}

# fixture NAME: a fresh module whose pins are all current: one direct
# requirement with no update, CI pinning golangci-lint v2.15.0, govulncheck
# v1.9.0 through VULN_VERSION and Go 1.26.x, the Makefile pinning the same
# govulncheck, and the latest releases matching. Prints its path.
fixture() {
    local dir="$ROOT/$1"
    mkdir -p "$dir/.github/workflows"
    : > "$dir/go.log"
    cat > "$dir/go.mod" <<'EOF'
module example.com/main

go 1.26.0

require example.com/direct v1.2.0
EOF
    cat > "$dir/all.json" <<'EOF'
{"Path":"example.com/main","Main":true}
{"Path":"example.com/direct","Version":"v1.2.0"}
EOF
    cat > "$dir/.github/workflows/ci.yml" <<'EOF'
env:
  VULN_VERSION: v1.9.0
jobs:
  lint:
    steps:
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: '1.26.x'
      - name: Run golangci-lint
        uses: golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a # v9.3.0
        with:
          # A comment between the uses line and the input.
          version: v2.15.0
EOF
    cat > "$dir/Makefile" <<'EOF'
VULN_VERSION := v1.9.0

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(VULN_VERSION) ./...
EOF
    latest "$dir" github.com/golangci/golangci-lint/v2 v2.15.0
    latest "$dir" golang.org/x/vuln v1.9.0
    echo 1.26.3 > "$dir/go_latest"
    echo "$dir"
}

# run DIR [VAR=VALUE...]: runs outdated.sh from DIR with the fake go first on
# PATH, no terminal and any extra environment, capturing OUT, ERR and CODE.
run() {
    local dir="$1"
    shift
    CODE=0
    OUT=$(cd "$dir" && env -u COOLDOWN_DAYS -u CI_FILE -u MAKE_FILE \
        PATH="$ROOT/bin:$PATH" FAKE_GO_DIR="$dir" GIT_CEILING_DIRECTORIES="$dir" "$@" \
        bash "$SCRIPT" < /dev/null 2> "$dir/.stderr") || CODE=$?
    ERR=$(cat "$dir/.stderr")
}

# has NAME TEXT: asserts stdout contains TEXT.
has() {
    grep -qF -- "$2" <<< "$OUT" || fail "$1" "stdout lacks: $2"
}

# lacks NAME TEXT: asserts stdout does not contain TEXT.
lacks() {
    ! grep -qF -- "$2" <<< "$OUT" || fail "$1" "stdout has: $2"
}

name=everything_current
dir=$(fixture "$name")
run "$dir"
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
has "$name" "All direct modules are up to date."
has "$name" "No direct module has a newer major version."
has "$name" "golangci-lint  v2.15.0    up to date"
has "$name" "govulncheck    v1.9.0     up to date"
has "$name" "Go             1.26.x     up to date (latest 1.26.3)"
lacks "$name" "indirect requirement"
lacks "$name" "disagree"
lacks "$name" "Not on a terminal"
echo "ok $name"

name=bad_cooldown_rejected
dir=$(fixture "$name")
run "$dir" COOLDOWN_DAYS=week
[ "$CODE" -eq 1 ] || fail "$name" "expected exit 1"
grep -qF "COOLDOWN_DAYS must be a whole number" <<< "$ERR" || fail "$name" "no cooldown error"
echo "ok $name"

name=go_list_failure_stops_the_run
dir=$(fixture "$name")
echo 'go: loading module retractions for example.com/direct@v1.2.0: dial tcp: refused' > "$dir/all.err"
run "$dir"
[ "$CODE" -eq 1 ] || fail "$name" "expected exit 1"
grep -qF "go list failed" <<< "$ERR" || fail "$name" "no go list failure message"
grep -qF "dial tcp: refused" <<< "$ERR" || fail "$name" "go's error not passed through"
lacks "$name" "up to date"
echo "ok $name"

name=direct_updates_listed_with_cooldown
dir=$(fixture "$name")
cat > "$dir/all.json" <<EOF
{"Path":"example.com/main","Main":true}
{"Path":"example.com/direct","Version":"v1.2.0","Update":{"Version":"v1.3.0","Time":"$OLD"}}
{"Path":"example.com/fresh","Version":"v0.4.0","Update":{"Version":"v0.4.1","Time":"$FRESH"}}
EOF
run "$dir"
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
grep -qE '^  example\.com/direct +v1\.2\.0 +v1\.3\.0 +[0-9]+d$' <<< "$OUT" || fail "$name" "no old update row"
grep -qE '^  example\.com/fresh +v0\.4\.0 +v0\.4\.1 +2d, too fresh$' <<< "$OUT" || fail "$name" "fresh update not flagged"
has "$name" "Not on a terminal, so not offering upgrades."
echo "ok $name"

name=retracted_and_deprecated_reported
dir=$(fixture "$name")
cat > "$dir/all.json" <<'EOF'
{"Path":"example.com/main","Main":true}
{"Path":"example.com/direct","Version":"v1.2.0","Retracted":["broken release","use v1.2.1"]}
{"Path":"example.com/old","Version":"v1.0.0","Deprecated":"moved to example.com/new"}
{"Path":"example.com/transitive","Version":"v1.0.0","Indirect":true,"Retracted":["ignored"]}
EOF
run "$dir"
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
has "$name" "! example.com/direct v1.2.0: retracted: broken release; use v1.2.1"
has "$name" "! example.com/old v1.0.0: deprecated: moved to example.com/new"
lacks "$name" "example.com/transitive"
echo "ok $name"

name=indirect_count_only_from_go_mod
dir=$(fixture "$name")
cat > "$dir/go.mod" <<'EOF'
module example.com/main

go 1.26.0

require example.com/direct v1.2.0

require (
	example.com/listed-a v1.0.0 // indirect
	example.com/listed-b v1.0.0 // indirect
	example.com/listed-c v1.0.0 // indirect
)
EOF
cat > "$dir/all.json" <<EOF
{"Path":"example.com/main","Main":true}
{"Path":"example.com/direct","Version":"v1.2.0"}
{"Path":"example.com/listed-a","Version":"v1.0.0","Indirect":true,"Update":{"Version":"v1.1.0","Time":"$OLD"}}
{"Path":"example.com/listed-b","Version":"v1.0.0","Indirect":true,"Update":{"Version":"v1.0.1","Time":"$OLD"}}
{"Path":"example.com/listed-c","Version":"v1.0.0","Indirect":true}
{"Path":"example.com/graph-only-1","Version":"v1.0.0","Indirect":true,"Update":{"Version":"v2.0.0","Time":"$OLD"}}
{"Path":"example.com/graph-only-2","Version":"v1.0.0","Indirect":true,"Update":{"Version":"v1.9.0","Time":"$OLD"}}
EOF
run "$dir"
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
has "$name" "2 indirect requirement(s) in go.mod have newer releases."
has "$name" "go mod tidy never upgrades."
lacks "$name" "graph-only"
echo "ok $name"

name=new_major_versions_walked
dir=$(fixture "$name")
cat > "$dir/all.json" <<'EOF'
{"Path":"example.com/main","Main":true}
{"Path":"example.com/direct","Version":"v1.2.0"}
{"Path":"example.com/lib/v2","Version":"v2.1.0"}
{"Path":"gopkg.in/yaml.v2","Version":"v2.4.0"}
{"Path":"example.com/legacy","Version":"v5.0.0+incompatible"}
EOF
latest "$dir" example.com/lib/v3 v3.0.2
latest "$dir" example.com/lib/v4 v4.1.0 "$FRESH"
latest "$dir" gopkg.in/yaml.v3 v3.0.1
run "$dir"
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
grep -qE '^  example\.com/lib/v2 +v2\.1\.0 +-> example\.com/lib/v4 v4\.1\.0 2d, too fresh$' <<< "$OUT" \
    || fail "$name" "did not walk lib up to v4"
grep -qE '^  gopkg\.in/yaml\.v2 +v2\.4\.0 +-> gopkg\.in/yaml\.v3 v3\.0\.1 [0-9]+d$' <<< "$OUT" \
    || fail "$name" "no gopkg.in major"
lacks "$name" "example.com/direct "
grep -qx "list -m -json example.com/direct/v2@latest" "$dir/go.log" || fail "$name" "direct v2 not probed"
grep -qx "list -m -json example.com/lib/v5@latest" "$dir/go.log" || fail "$name" "walk did not probe past v4"
! grep -q "legacy" "$dir/go.log" || fail "$name" "probed a +incompatible module"
echo "ok $name"

name=vuln_pins_disagree
dir=$(fixture "$name")
sed -i.bak 's/^VULN_VERSION := v1.9.0$/VULN_VERSION := v1.8.0/' "$dir/Makefile"
run "$dir"
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
has "$name" "! pins disagree: .github/workflows/ci.yml:2 has v1.9.0, Makefile:1 has v1.8.0"
echo "ok $name"

name=vuln_update_names_both_pins
dir=$(fixture "$name")
latest "$dir" golang.org/x/vuln v1.10.0
run "$dir"
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
grep -qE '^  govulncheck +v1\.9\.0 +-> v1\.10\.0 +[0-9]+d$' <<< "$OUT" || fail "$name" "no govulncheck update line"
has "$name" "pinned in .github/workflows/ci.yml:2, Makefile:1"
lacks "$name" "disagree"
echo "ok $name"

name=vuln_quoted_and_commented_pins
dir=$(fixture "$name")
sed -i.bak 's/^  VULN_VERSION: v1.9.0$/  VULN_VERSION: "v1.9.0" # bump with the Makefile/' "$dir/.github/workflows/ci.yml"
sed -i.bak 's/^VULN_VERSION := v1.9.0$/VULN_VERSION ?= v1.9.0 # bump with CI/' "$dir/Makefile"
run "$dir"
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
has "$name" "govulncheck    v1.9.0     up to date"
lacks "$name" "disagree"
echo "ok $name"

name=vuln_missing_from_makefile
dir=$(fixture "$name")
printf 'vuln:\n\tgovulncheck ./...\n' > "$dir/Makefile"
run "$dir"
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
has "$name" "govulncheck    v1.9.0     up to date"
has "$name" "no VULN_VERSION in Makefile, so make vuln may not match CI"
echo "ok $name"

name=lint_pin_missing
dir=$(fixture "$name")
sed -i.bak '/version: v2.15.0/d' "$dir/.github/workflows/ci.yml"
run "$dir"
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
has "$name" "golangci-lint  pin not found (looked in .github/workflows/ci.yml)"
echo "ok $name"

name=newer_go_line_couples_lint
dir=$(fixture "$name")
echo 1.27.4 > "$dir/go_latest"
run "$dir"
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
has "$name" "Go             1.26.x     -> 1.27.4"
has "$name" "go.mod says go 1.26.0, the minimum supported version"
has "$name" "bump it in the same change (.github/workflows/ci.yml:13)."
echo "ok $name"

name=ci_file_missing
dir=$(fixture "$name")
rm "$dir/.github/workflows/ci.yml"
run "$dir"
[ "$CODE" -eq 0 ] || fail "$name" "expected exit 0"
has "$name" ".github/workflows/ci.yml not found, skipping."
echo "ok $name"

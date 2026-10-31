#!/usr/bin/env bash
#
# caddy_shack Local Build Validation
#
# Runs the quality checks every developer should run before pushing.
# Adapted from the Jellyfish pre-CI script for caddy_shack's single-binary
# cobra CLI layout (module root builds ./).
#
# Usage: ./scripts/pre-ci-check.sh [--fix gofmt]
#
# Options:
#   --fix gofmt   Auto-fix formatting issues with gofmt -s -w before checking

set -e
set -o pipefail

# Colours for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Colour

# Results tracking
declare -a RESULTS
declare -a FAILURES
TOTAL_CHECKS=0
PASSED_CHECKS=0
COVERAGE=""
COVERAGE_DELTA=""
FORMAT_FAILED=false
NO_TESTS_YET=false
VERSION_WARNING=""

# Fix mode flags
FIX_GOFMT=false

# Helper functions
print_header() {
    echo -e "\n${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n"
}

print_step() {
    echo -e "${YELLOW}▶${NC} $1"
}

record_result() {
    local name="$1"
    local status="$2"
    local details="$3"

    TOTAL_CHECKS=$((TOTAL_CHECKS + 1))

    if [ "$status" = "PASS" ]; then
        PASSED_CHECKS=$((PASSED_CHECKS + 1))
        RESULTS+=("${GREEN}✓${NC} $name")
    else
        RESULTS+=("${RED}✗${NC} $name")
        FAILURES+=("  - $name: $details")
    fi
}

# LDFLAGS mirror the Makefile so build / smoke checks exercise the same
# version-stamped binary that `make build` produces.
build_ldflags() {
    local VERSION COMMIT TAG DIRTY
    VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
    COMMIT=$(git rev-parse HEAD 2>/dev/null || echo "")
    TAG=$(git describe --exact-match --tags HEAD 2>/dev/null || echo "")
    if [ -n "$(git status --porcelain 2>/dev/null)" ]; then
        DIRTY=true
    else
        DIRTY=false
    fi
    echo "-X github.com/bawdo/caddy_shack/internal/version.Version=$VERSION \
-X github.com/bawdo/caddy_shack/internal/version.Commit=$COMMIT \
-X github.com/bawdo/caddy_shack/internal/version.Tag=$TAG \
-X github.com/bawdo/caddy_shack/internal/version.Dirty=$DIRTY"
}

# Clean up old artefacts
cleanup() {
    print_step "Cleaning up old test artefacts..."
    rm -f coverage/pre-ci-*.out coverage/pre-ci-*.txt
    mkdir -p coverage
}

# Check 1: Go version
check_go_version() {
    print_header "Check 1: Go Version"
    print_step "Verifying Go installation..."

    if ! command -v go &> /dev/null; then
        record_result "Go Version" "FAIL" "Go not installed"
        echo -e "${RED}✗ Go is not installed${NC}"
        return 1
    fi

    local GO_VERSION
    GO_VERSION=$(go version | awk '{print $3}')
    echo "Detected: $GO_VERSION"

    # caddy_shack requires Go 1.25+ (see go.mod). Regex covers 1.25 through
    # 1.99 to avoid spurious warnings when the toolchain is upgraded.
    if [[ "$GO_VERSION" =~ go1\.(2[5-9]|[3-9][0-9])\. ]]; then
        record_result "Go Version" "PASS" ""
        echo -e "${GREEN}✓ Go version meets caddy_shack requirement (1.25+)${NC}"
    else
        VERSION_WARNING="caddy_shack requires Go 1.25+, you have $GO_VERSION"
        record_result "Go Version" "PASS" ""
        echo -e "${YELLOW}⚠ Warning: $VERSION_WARNING${NC}"
    fi
}

# Check 2: Dependencies
check_dependencies() {
    print_header "Check 2: Dependencies"
    print_step "Downloading dependencies..."

    if go mod download 2>&1 | tee coverage/pre-ci-deps.txt; then
        record_result "Dependencies" "PASS" ""
        echo -e "${GREEN}✓ Dependencies downloaded successfully${NC}"
    else
        record_result "Dependencies" "FAIL" "Failed to download dependencies"
        echo -e "${RED}✗ Failed to download dependencies${NC}"
        return 1
    fi
}

# Check 3: gofmt formatting
check_formatting() {
    print_header "Check 3: Code Formatting (gofmt -s)"

    if [ "$FIX_GOFMT" = true ]; then
        print_step "Auto-fixing formatting with gofmt -s -w ..."
        FIXED=$(gofmt -s -l . 2>&1 | grep -v '^\.git' || true)
        if [ -n "$FIXED" ]; then
            gofmt -s -w .
            echo -e "${YELLOW}⚠ Fixed formatting in:${NC}"
            awk '{ print "  " $0 }' <<< "$FIXED"
        fi
    fi

    print_step "Checking code formatting..."

    UNFORMATTED=$(gofmt -s -l . 2>&1 | grep -v '^\.git' || true)

    if [ -z "$UNFORMATTED" ]; then
        record_result "Code Formatting" "PASS" ""
        echo -e "${GREEN}✓ All files are properly formatted${NC}"
    else
        FORMAT_FAILED=true
        record_result "Code Formatting" "FAIL" "Files need formatting"
        echo -e "${RED}✗ The following files need formatting:${NC}"
        awk '{ print "  " $0 }' <<< "$UNFORMATTED"
        return 1
    fi
}

# Check 4: Tests with race detector
check_tests() {
    print_header "Check 4: Tests (with race detector)"
    print_step "Running test suite..."

    if [[ "$OSTYPE" == "darwin"* ]] || [[ "$OSTYPE" == "linux-gnu"* ]]; then
        if go test -race -v ./... 2>&1 | tee coverage/pre-ci-tests.txt; then
            record_result "Tests" "PASS" ""
            echo -e "${GREEN}✓ All tests passed${NC}"
        else
            record_result "Tests" "FAIL" "Test failures detected"
            echo -e "${RED}✗ Tests failed${NC}"
            return 1
        fi
    else
        if go test -v ./... 2>&1 | tee coverage/pre-ci-tests.txt; then
            record_result "Tests" "PASS" ""
            echo -e "${GREEN}✓ All tests passed${NC}"
        else
            record_result "Tests" "FAIL" "Test failures detected"
            echo -e "${RED}✗ Tests failed${NC}"
            return 1
        fi
    fi
}

# Check 5: Linting
check_linting() {
    print_header "Check 5: Linting (golangci-lint)"
    print_step "Running golangci-lint..."

    if ! command -v golangci-lint &> /dev/null; then
        record_result "Linting" "FAIL" "golangci-lint not installed"
        echo -e "${RED}✗ golangci-lint is not installed${NC}"
        echo -e "${YELLOW}Install: brew install golangci-lint${NC}"
        return 1
    fi

    if golangci-lint run --timeout=5m 2>&1 | tee coverage/pre-ci-lint.txt; then
        record_result "Linting" "PASS" ""
        echo -e "${GREEN}✓ No linting issues found${NC}"
    else
        record_result "Linting" "FAIL" "Linting issues detected"
        echo -e "${RED}✗ Linting failed${NC}"
        return 1
    fi
}

# Check 6: Coverage
check_coverage() {
    print_header "Check 6: Test Coverage"
    print_step "Generating coverage report..."

    # Detect whether any package has test files at all.
    # If none, this is a "no tests yet" situation.
    local TEST_PKGS
    TEST_PKGS=$(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... 2>/dev/null | grep -v '^$' || true)

    if [ -z "$TEST_PKGS" ]; then
        NO_TESTS_YET=true
        COVERAGE="0.0"
        record_result "Coverage Generation" "PASS" ""
        echo -e "${YELLOW}⚠ No tests yet: coverage check skipped${NC}"
        echo -e "  Coverage target gate suppressed for this run."
        return 0
    fi

    local COVERPKG
    COVERPKG=$(go list ./... | grep -v testutil | tr '\n' ',' | sed 's/,$//')

    local PREV_COVERAGE=""
    local COVERAGE_STORE="coverage/.last-coverage"
    if [ -f "$COVERAGE_STORE" ]; then
        # tail -1 + whitespace strip defends against historical pollution
        # of the cache file (a previous version of this script used an
        # un-anchored grep that captured multiple percentages).
        PREV_COVERAGE=$(tail -1 "$COVERAGE_STORE" | tr -d '[:space:]')
    fi

    if go test -race -coverprofile=coverage/pre-ci-coverage.out -covermode=atomic -coverpkg="$COVERPKG" ./... 2>&1 | tee coverage/pre-ci-coverage.txt; then
        # Anchor on '^total:' so the summary row is matched, not function
        # names that happen to contain the word "total" (e.g. totalResults).
        # tail -1 is a belt-and-braces guard against future format changes.
        COVERAGE=$(go tool cover -func=coverage/pre-ci-coverage.out | grep '^total:' | tail -1 | awk '{print $3}' | sed 's/%//')

        if [ -z "$COVERAGE" ]; then
            COVERAGE="0.0"
        fi

        echo "$COVERAGE" > "$COVERAGE_STORE"

        if [ -n "$PREV_COVERAGE" ]; then
            COVERAGE_DELTA=$(echo "scale=1; $COVERAGE - $PREV_COVERAGE" | bc)
        fi

        record_result "Coverage Generation" "PASS" ""
        echo -e "${GREEN}✓ Coverage report generated${NC}"
        echo -e "  Total coverage: ${BLUE}${COVERAGE}%${NC}"

        if (( $(echo "$COVERAGE >= 80" | bc -l) )); then
            echo -e "  ${GREEN}✓ Meets coverage target (80%)${NC}"
        else
            echo -e "  ${YELLOW}⚠ Below coverage target (80%)${NC}"
        fi
    else
        record_result "Coverage Generation" "FAIL" "Coverage generation failed"
        echo -e "${RED}✗ Coverage generation failed${NC}"
        return 1
    fi
}

# Check 7: Build
check_build() {
    print_header "Check 7: Build"
    print_step "Building caddy_shack binary with version ldflags into a temporary directory..."

    local TMPBIN
    TMPBIN=$(mktemp -d)
    trap 'rm -rf "$TMPBIN"' RETURN

    local LDFLAGS
    LDFLAGS=$(build_ldflags)

    if go build -ldflags "$LDFLAGS" -o "$TMPBIN/caddy_shack" . 2>&1 | tee coverage/pre-ci-build.txt; then
        record_result "Build" "PASS" ""
        echo -e "${GREEN}✓ Build successful${NC}"
        echo -e "  Built: $(find "$TMPBIN" -mindepth 1 -maxdepth 1 -print0 | xargs -0 -n1 basename | tr '\n' ' ')"
    else
        record_result "Build" "FAIL" "Build failed"
        echo -e "${RED}✗ Build failed${NC}"
        return 1
    fi
}

# Check 8: Vulnerability scan
check_vulnerability_scan() {
    print_header "Check 8: Vulnerability Scan (govulncheck)"
    print_step "Running govulncheck..."

    if ! command -v govulncheck &> /dev/null; then
        record_result "Vulnerability Scan" "FAIL" "govulncheck not installed"
        echo -e "${RED}✗ govulncheck is not installed${NC}"
        echo -e "${YELLOW}Install: go install golang.org/x/vuln/cmd/govulncheck@latest${NC}"
        return 1
    fi

    if govulncheck ./... 2>&1 | tee coverage/pre-ci-vuln.txt; then
        record_result "Vulnerability Scan" "PASS" ""
        echo -e "${GREEN}✓ No vulnerabilities found${NC}"
    else
        record_result "Vulnerability Scan" "FAIL" "Vulnerabilities detected"
        echo -e "${RED}✗ Vulnerability scan failed${NC}"
        return 1
    fi
}

# Check 9: CLI smoke (binary launches, `version` prints identity)
check_cli_smoke() {
    print_header "Check 9: CLI Smoke (caddy_shack version prints build identity)"
    print_step "Building caddy_shack for smoke test..."

    local TMPBIN
    TMPBIN=$(mktemp -d)
    trap 'rm -rf "$TMPBIN"' RETURN

    local LDFLAGS
    LDFLAGS=$(build_ldflags)

    if ! go build -ldflags "$LDFLAGS" -o "$TMPBIN/caddy_shack" . 2>&1 | tee coverage/pre-ci-smoke-build.txt; then
        record_result "CLI Smoke" "FAIL" "caddy_shack build failed"
        echo -e "${RED}✗ caddy_shack build failed${NC}"
        return 1
    fi

    print_step "Running caddy_shack version..."
    if ! "$TMPBIN/caddy_shack" version >"$TMPBIN/smoke.log" 2>&1; then
        record_result "CLI Smoke" "FAIL" "caddy_shack version exited non-zero"
        echo -e "${RED}✗ caddy_shack version exited non-zero${NC}"
        echo "Output:"
        sed 's/^/  /' "$TMPBIN/smoke.log"
        return 1
    fi

    # First line is "caddy_shack <version>": assert that shape rather than
    # a specific version string so the check survives any tag.
    if head -1 "$TMPBIN/smoke.log" | grep -q '^caddy_shack '; then
        record_result "CLI Smoke" "PASS" ""
        echo -e "${GREEN}✓ caddy_shack version printed build identity${NC}"
        sed 's/^/  /' "$TMPBIN/smoke.log"
    else
        record_result "CLI Smoke" "FAIL" "caddy_shack version output unexpected"
        echo -e "${RED}✗ caddy_shack version output did not begin with 'caddy_shack '${NC}"
        echo "Output:"
        sed 's/^/  /' "$TMPBIN/smoke.log"
        return 1
    fi
}

# CI runs shellcheck on the scripts; without it installed, warn rather than fail.
lint_scripts() {
    if command -v shellcheck &> /dev/null; then
        # CI pins shellcheck to the version in .github/workflows/ci.yml; a
        # different version here can disagree with it, so say which ran.
        echo "shellcheck $(shellcheck --version | awk '/^version:/ { print $2 }')"
        shellcheck scripts/*.sh
    else
        echo -e "${YELLOW}⚠ shellcheck is not installed; skipping the script lint that CI runs${NC}"
    fi
}

# Check 10: Changelog and docs (CHANGELOG.md, documents, shell script tests)
check_changelog() {
    print_header "Check 10: Changelog and docs (changelog.sh, docs-check.sh, shellcheck, script tests)"
    print_step "Validating CHANGELOG.md and documents, linting the scripts and running the script tests..."

    if { scripts/changelog.sh check && scripts/docs-check.sh && lint_scripts && make test-scripts; } 2>&1 | tee coverage/pre-ci-changelog.txt; then
        record_result "Changelog and docs" "PASS" ""
        echo -e "${GREEN}✓ CHANGELOG.md and documents are consistent, the scripts lint clean and their tests pass${NC}"
    else
        record_result "Changelog and docs" "FAIL" "changelog, docs check, shellcheck or script tests failed"
        echo -e "${RED}✗ Changelog and docs check failed${NC}"
        return 1
    fi
}

# Check 11: Fuzz (every fuzz target, a few seconds each)
check_fuzz() {
    print_header "Check 11: Fuzz (make fuzz, 5s per target)"
    print_step "Running every fuzz target for 5s each..."

    if make fuzz FUZZTIME=5s 2>&1 | tee coverage/pre-ci-fuzz.txt; then
        record_result "Fuzz" "PASS" ""
        echo -e "${GREEN}✓ Every fuzz target held its properties${NC}"
    else
        record_result "Fuzz" "FAIL" "a fuzz target failed; any crasher is in internal/*/testdata/fuzz/"
        echo -e "${RED}✗ Fuzz check failed${NC}"
        echo -e "  Crashing inputs are saved under internal/*/testdata/fuzz/; reproduce with go test, fix the bug, then keep the file as a regression seed."
        return 1
    fi
}

# Generate summary report
generate_report() {
    print_header "Local Build Readiness Report"

    echo "Check Results:"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    for result in "${RESULTS[@]}"; do
        echo -e "  $result"
    done

    echo ""
    echo "Summary:"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo -e "  Total checks: $TOTAL_CHECKS"
    echo -e "  Passed: ${GREEN}$PASSED_CHECKS${NC}"
    echo -e "  Failed: ${RED}$((TOTAL_CHECKS - PASSED_CHECKS))${NC}"
    if [ "$NO_TESTS_YET" = true ]; then
        echo -e "  Coverage: ${YELLOW}skipped (no tests yet)${NC}"
    elif [ -n "$COVERAGE" ]; then
        if [ -n "$COVERAGE_DELTA" ]; then
            local DELTA_DISPLAY=""
            if (( $(echo "$COVERAGE_DELTA > 0" | bc -l) )); then
                DELTA_DISPLAY="${GREEN}(+${COVERAGE_DELTA}%)${NC}"
            elif (( $(echo "$COVERAGE_DELTA < 0" | bc -l) )); then
                DELTA_DISPLAY="${RED}(${COVERAGE_DELTA}%)${NC}"
            else
                DELTA_DISPLAY="${NC}(no change)"
            fi
            echo -e "  Coverage: ${BLUE}${COVERAGE}%${NC} ${DELTA_DISPLAY}"
        else
            echo -e "  Coverage: ${BLUE}${COVERAGE}%${NC} (first run)"
        fi
    fi

    SUCCESS_RATE=$(echo "scale=0; ($PASSED_CHECKS * 100) / $TOTAL_CHECKS" | bc)

    echo ""
    if [ "$SUCCESS_RATE" -eq 100 ]; then
        echo -e "Build Readiness: ${GREEN}${SUCCESS_RATE}%${NC}"
        echo -e "${GREEN}✓ All checks passed!${NC}"
    elif [ "$SUCCESS_RATE" -ge 80 ]; then
        echo -e "Build Readiness: ${YELLOW}${SUCCESS_RATE}%${NC} ⚠"
        echo -e "${YELLOW}⚠ Some checks failed.${NC}"
    else
        echo -e "Build Readiness: ${RED}${SUCCESS_RATE}%${NC} ✗"
        echo -e "${RED}✗ Multiple checks failed.${NC}"
    fi

    if [ ${#FAILURES[@]} -gt 0 ]; then
        echo ""
        echo "Failures:"
        echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
        for failure in "${FAILURES[@]}"; do
            echo -e "${RED}$failure${NC}"
        done
    fi

    if [ "$FORMAT_FAILED" = true ]; then
        echo ""
        echo -e "${YELLOW}Tip: re-run with --fix gofmt to auto-fix formatting:${NC}"
        echo -e "  ${BLUE}./scripts/pre-ci-check.sh --fix gofmt${NC}"
    fi

    if [ -n "$VERSION_WARNING" ]; then
        echo ""
        echo -e "${YELLOW}⚠ $VERSION_WARNING${NC}"
    fi

    echo ""
    echo "Detailed logs saved in: coverage/pre-ci-*.txt  (checks 1 to 11)"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

# Main execution
main() {
    # Resolve repo root from the script's own location so the script works
    # whether invoked from the repo root or a subdirectory.
    local SCRIPT_DIR
    SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
    cd "$SCRIPT_DIR/.." || { echo -e "${RED}Could not cd to repo root from $SCRIPT_DIR${NC}"; exit 1; }

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --fix)
                case "$2" in
                    gofmt) FIX_GOFMT=true; shift 2 ;;
                    *) echo -e "${RED}Unknown --fix target: $2${NC}"; echo "Usage: $0 [--fix gofmt]"; exit 1 ;;
                esac
                ;;
            *) echo -e "${RED}Unknown option: $1${NC}"; echo "Usage: $0 [--fix gofmt]"; exit 1 ;;
        esac
    done

    echo -e "${BLUE}"
    echo "╔════════════════════════════════════════════════════════════════════╗"
    if [ "$FIX_GOFMT" = true ]; then
    echo "║          caddy_shack Local Build Validation [--fix gofmt]         ║"
    else
    echo "║               caddy_shack Local Build Validation                  ║"
    fi
    echo "╚════════════════════════════════════════════════════════════════════╝"
    echo -e "${NC}"

    cleanup

    check_go_version || true
    check_dependencies || true
    check_formatting || true
    check_tests || true
    check_linting || true
    check_coverage || true
    check_build || true
    check_vulnerability_scan || true
    check_cli_smoke || true
    check_changelog || true
    check_fuzz || true

    generate_report

    if [ $PASSED_CHECKS -eq $TOTAL_CHECKS ]; then
        exit 0
    else
        exit 1
    fi
}

main "$@"

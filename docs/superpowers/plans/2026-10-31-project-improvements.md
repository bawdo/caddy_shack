# Project Improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring over jellyfish's CI gate, ruleset hardening, release isolation, provenance bundle and fuzzing, plus small docs and Scorecard fixes, without weakening any check caddy_shack already has.

**Architecture:** Testable shell first (`ci-result.sh`, `publish-release.sh`, `repo-settings.sh`), then the workflows that call it. Fuzz targets land before the CI job that runs them. Live settings change only in the last task, after the integration branch merges, by re-running `scripts/repo-settings.sh`.

**Tech Stack:** Go 1.26.x, `go test -fuzz`, bash with jq, GitHub Actions, `gh`, actionlint, shellcheck v0.11.0.

**Spec:** `docs/superpowers/specs/2026-10-31-project-improvements-design.md`

## Global Constraints

- Kaizen shape: integration branch `kaizen/project_improvements`. Each task runs in its own worktree on a dev branch cut from it and merges back with a merge commit. Remove worktrees and dev branches once merged.
- After every task: `make test-scripts`, `go test -race ./...` and `golangci-lint run` pass, and `actionlint` is clean whenever a workflow changed. `make test-scripts` and `go test` need the Bash sandbox off (they bind loopback ports and cross-compile); lint runs sandboxed with `GOCACHE=$TMPDIR/gocache GOLANGCI_LINT_CACHE=$TMPDIR/golangci`.
- Shell scripts pass `shellcheck` v0.11.0 (`shellcheck scripts/*.sh`) and follow the house layout: header comment with usage, `set -euo pipefail`, a `die` helper printing `<script>: <message>` to stderr.
- Every new script gets a `scripts/<name>_test.sh` in the existing style (prints `ok <name>`, exits non-zero on the first failure) and runs under `make test-scripts` with no Makefile change.
- Every action reference stays pinned to a 40-hex SHA with a version comment. No new third-party actions; `actions/*` and `github/*` only.
- Never weaken an existing assertion to pass a test. If expected text changes, update the assertion to the new exact text.
- Commits: signed with `git commit -S`, past tense, subject 60 characters or fewer, no agent attribution of any kind.
- Australian English. No em or en dashes anywhere (`scripts/docs-check.sh` enforces this outside `docs/superpowers/`).
- No live repository writes before Task 8, and Task 8 only with Keith's go-ahead.

## Review Focus

1. A release re-run that finds a stale draft and is handed a broken bundle: `publish-release.sh` must refuse before deleting the draft, leaving it there for the next attempt. Pinned in Task 5 by `publish_refuses_bad_bundle_before_touching_draft`.
2. A job added to `ci.yml` but not to `ci-result.sh` (or the reverse): the gate must never pass with a job missing from its view. Pinned in Task 1 by `extra_job_is_expected` and in Task 3 by `ci_yml_needs_matches_jobs`.
3. `repo-settings.sh rulesets` run before the new `ci.yml` is on `main`: it must refuse, not require a check that never reports. Pinned in Task 4 by `rulesets_needs_ci_result_job`.
4. The weekly schedule firing while a push to `main` is running CI: neither may cancel or queue behind the other. Pinned in Task 3 by `ci_yml_schedule_has_own_concurrency_group`.
5. A `state.json` written by an old version (schema 1) after the fuzz work: it must still migrate, and every `Load` failure on readable bytes must be `*ErrCorrupt`, so the CLI keeps its exit code 7. Pinned in Task 2 by `FuzzLoad` and the existing state tests staying green unchanged.

---

### Task 1: CI result gate script

**Files:**
- Create: `scripts/ci-result.sh`
- Test: `scripts/ci-result_test.sh`

**Interfaces:**
- Produces: `scripts/ci-result.sh` reads env `EVENT` (a `github.event_name`) and `NEEDS` (the `toJSON(needs)` object). Exits 0 when every expected job passed, else prints `Not passed:` then one `  <job>: <result>` line per bad job and exits 1. `scripts/ci-result.sh --jobs` prints the expected job names, one per line, in this order: `static vuln test integration build fuzz`.
- Allowed skips: `pull_request` and `push` may skip `fuzz`; `schedule` may skip `static test integration build`; every other event (including `workflow_dispatch`) may skip nothing.

- [ ] **Step 1: Write the failing tests** in `scripts/ci-result_test.sh`. A helper `needs JOB=RESULT...` builds the JSON (`{"static":{"result":"success"},...}`); `run EVENT JSON` captures OUT, ERR, CODE. Cases:
  - `pr_passes_with_fuzz_skipped`: all `success`, `fuzz` `skipped`, EVENT `pull_request` → exit 0, stdout ends `Every expected job passed.`
  - `push_passes_with_fuzz_skipped`: same with EVENT `push` → exit 0.
  - `pr_passes_with_fuzz_run`: all six `success` → exit 0.
  - `pr_fails_on_skipped_static`: `static` `skipped` → exit 1, stdout has the line `  static: skipped`.
  - `schedule_passes_with_vuln_and_fuzz_only`: `vuln` and `fuzz` `success`, other four `skipped` → exit 0.
  - `schedule_fails_on_vuln_failure`: `vuln` `failure` → exit 1, line `  vuln: failure`.
  - `dispatch_fails_on_skipped_fuzz`: EVENT `workflow_dispatch`, `fuzz` `skipped` → exit 1.
  - `unknown_event_expects_everything`: EVENT `merge_group`, `fuzz` `skipped` → exit 1.
  - `missing_job_fails`: `integration` absent from NEEDS → exit 1, line `  integration: missing`.
  - `cancelled_job_fails`: `build` `cancelled` → exit 1, line `  build: cancelled`.
  - `extra_job_is_expected`: an extra key `lint` `skipped` on `pull_request` → exit 1, line `  lint: skipped`.
  - `needs_not_an_object_fails`: NEEDS `[]` → exit 1, line `  job results are not an object`.
  - `jobs_lists_expected_order`: `--jobs` output equals the six names above, newline separated.

- [ ] **Step 2: Run to verify it fails**

Run: `bash scripts/ci-result_test.sh`
Expected: FAIL, `scripts/ci-result.sh` does not exist.

- [ ] **Step 3: Implement `scripts/ci-result.sh`.** Port the jq from jellyfish's `ci-result` job: expected set is the hardcoded list unioned with `NEEDS` keys, a result of `success` passes, `skipped` passes only when the event allows it, anything else (including absent, reported as `missing`) is printed. Print `Event: $EVENT (may skip: ...)` and each job's result before judging, as jellyfish does, so the log shows the whole picture.

- [ ] **Step 4: Run to verify it passes**

Run: `bash scripts/ci-result_test.sh && shellcheck scripts/ci-result.sh scripts/ci-result_test.sh`
Expected: thirteen `ok` lines, shellcheck silent.

- [ ] **Step 5: Commit**

```bash
git add scripts/ci-result.sh scripts/ci-result_test.sh
git commit -S -m "Added a CI result gate script with tests"
```

### Task 2: Fuzz targets, make fuzz and the pre-ci check

**Files:**
- Create: `internal/certstore/certstore_fuzz_test.go`, `internal/state/state_fuzz_test.go`, `internal/app/names_fuzz_test.go`, `internal/docker/cli_fuzz_test.go`
- Modify: `Makefile` (new `fuzz` target, `FUZZTIME`, `FUZZ_TARGETS`, `.PHONY`), `scripts/pre-ci-check.sh` (new `check_fuzz`, wired into `main` after `check_changelog`)

**Interfaces:**
- Produces: `make fuzz [FUZZTIME=15s]` runs each target below in turn, failing on the first crash. Task 3's CI job calls it with `FUZZTIME` `5m` (schedule) or `15s` (dispatch).
- `FUZZ_TARGETS` (`<package>:<Name>`), in this order:
  `./internal/certstore:FuzzValidDomain ./internal/state:FuzzLoad ./internal/app:FuzzValidLANName ./internal/app:FuzzValidateCertDomain ./internal/docker:FuzzParseContainerInfo ./internal/docker:FuzzParseBridgeGateway ./internal/docker:FuzzParseContainerStatus`

Each target lives in the package's internal test package (`package certstore`, `package state`, `package app`, `package docker`), seeds `f.Add` with every literal input the existing tests for that function already use, and asserts the properties below with `t.Fatalf` naming the input.

- [ ] **Step 1: Write the fuzz targets**
  - `FuzzValidDomain(f)`: when `validDomain(d) == nil`, then `filepath.IsLocal(d)`, `!strings.ContainsAny(d, "/\\")`, and `New(dir).CertPath(d)` starts with `dir + string(filepath.Separator)` for `dir := t.TempDir()`.
  - `FuzzLoad(f)`: writes the bytes to `t.TempDir()/state.json` and calls `Load`. On error, `IsCorrupt(err)` must be true. On success, `SchemaVersion == SchemaVersion`, `Backends != nil`, `Certs != nil`, and `Save` to a second path followed by `Load` of it returns no error. Seeds: a schema 1 file, a schema 2 file with one backend and one cert, `{}`, `null`, `{"schema_version":3}`.
  - `FuzzValidLANName(f)`: when `validLANName(n) == nil`, then `len(n) <= 253`, `n == strings.ToLower(n)`, `n != "test"`, `!strings.HasSuffix(n, ".test")`, and every `.`-separated label is 1 to 63 bytes and neither starts nor ends with `-`.
  - `FuzzValidateCertDomain(f)`: when `validateCertDomain(d) == nil`, then `!strings.HasPrefix(d, "*.")`, `!strings.HasSuffix(d, ".test")`, `!strings.ContainsAny(d, "/\\")` and `!strings.Contains(d, "..")`.
  - `FuzzParseContainerInfo(f)`: must not panic. Seed with the inspect JSON fixtures in `cli_test.go`.
  - `FuzzParseBridgeGateway(f)`: when it returns `gw, nil`, then `net.ParseIP(gw).To4() != nil`.
  - `FuzzParseContainerStatus(f)`: error if and only if the state is `StateAbsent`.

- [ ] **Step 2: Run the seed corpus and confirm a short fuzz finds nothing**

Run: `go test -race ./internal/certstore ./internal/state ./internal/app ./internal/docker && for t in FuzzValidDomain:certstore FuzzLoad:state; do go test ./internal/${t#*:} -run='^$' -fuzz="^${t%%:*}\$" -fuzztime=10s; done`
Expected: PASS. If a property fails, stop and report the input with a diagnosis: it is a real bug, not a test to loosen.

- [ ] **Step 3: Add the `fuzz` target to the Makefile** with jellyfish's loop (`pkg=$${t%%:*}; name=$${t##*:}; go test "$$pkg" -run='^$$' -fuzz="^$$name\$$" -fuzztime=$(FUZZTIME)`), `FUZZTIME ?= 15s`, help text `Run every fuzz target for FUZZTIME each (default 15s)`.

- [ ] **Step 4: Add `check_fuzz` to `scripts/pre-ci-check.sh`** in the style of the other checks: header `Check N: Fuzz (make fuzz, 5s per target)`, runs `make fuzz FUZZTIME=5s` teed to `coverage/pre-ci-fuzz.txt`, failure detail names `internal/*/testdata/fuzz/`. Renumber any "checks 1 to N" text in the report.

- [ ] **Step 5: Verify**

Run: `make fuzz FUZZTIME=3s && make help | grep -q '^  fuzz ' && shellcheck scripts/pre-ci-check.sh && go test -race ./... && golangci-lint run`
Expected: seven `==>` headers, no failures; lint clean. `git status` shows no `testdata/fuzz/` directories (none should be written without a crash).

- [ ] **Step 6: Commit**

```bash
git add internal/certstore/certstore_fuzz_test.go internal/state/state_fuzz_test.go \
  internal/app/names_fuzz_test.go internal/docker/cli_fuzz_test.go Makefile scripts/pre-ci-check.sh
git commit -S -m "Added fuzz targets for path, name and parser input"
```

### Task 3: CI workflow restructure

**Files:**
- Modify: `.github/workflows/ci.yml`
- Test: `scripts/ci-result_test.sh` (two workflow consistency cases)

**Interfaces:**
- Consumes: `scripts/ci-result.sh` and its `--jobs` list (Task 1); `make fuzz` (Task 2).
- Produces: a job named `CI result` (job id `ci-result`) that Task 4's ruleset requires. Job ids `static vuln test integration build fuzz`.

- [ ] **Step 1: Write the failing consistency tests** in `scripts/ci-result_test.sh`:
  - `ci_yml_needs_matches_jobs`: the `needs:` list of the `ci-result` job, read from `.github/workflows/ci.yml` with sed, equals `scripts/ci-result.sh --jobs` joined by `, ` inside brackets, that is `[static, vuln, test, integration, build, fuzz]`.
  - `ci_yml_schedule_has_own_concurrency_group`: `ci.yml` contains the exact line `  group: ci-${{ github.event_name == 'schedule' && 'schedule-' || '' }}${{ github.ref }}`.

- [ ] **Step 2: Run to verify they fail**

Run: `bash scripts/ci-result_test.sh`
Expected: FAIL at `ci_yml_needs_matches_jobs`.

- [ ] **Step 3: Restructure `ci.yml`**
  - Triggers: keep `push: branches: [ main ]`, `pull_request`, `workflow_dispatch`, `workflow_call` (with its `CODECOV_TOKEN` secret). Add `schedule: - cron: '23 4 * * 1'`. Rewrite the header comment to say which events run which jobs, using the may-skip table from Task 1.
  - Concurrency: the group line pinned above, `cancel-in-progress` unchanged. Comment why the prefix is literal and why the schedule gets its own group.
  - `static`: move the govulncheck step out; add `if: github.event_name != 'schedule'`.
  - New `vuln` job (`name: Vulnerability scan`, ubuntu, 10 minutes, no `if`): checkout without credentials, setup-go as the others, `go run golang.org/x/vuln/cmd/govulncheck@${{ env.VULN_VERSION }} ./...`.
  - `test`, `integration`, `build`: add `if: github.event_name != 'schedule'`.
  - New `fuzz` job (`name: Fuzz`, ubuntu): `if: github.event_name == 'schedule' || github.event_name == 'workflow_dispatch'`, `timeout-minutes: ${{ github.event_name == 'schedule' && 60 || 15 }}`, `make fuzz FUZZTIME="$FUZZTIME"` with `FUZZTIME` `5m` or `15s`, then on `failure()` upload `internal/*/testdata/fuzz/` as `fuzz-failures` (`if-no-files-found: ignore`, `retention-days: 14`) with `actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2`. Comment that a release re-run (`workflow_dispatch` through `release.yml`) fuzzes for 15s on purpose (spec, Decisions).
  - New `ci-result` job (`name: CI result`, ubuntu, 5 minutes, `if: always()`, `needs: [static, vuln, test, integration, build, fuzz]`): checkout without credentials, then `scripts/ci-result.sh` with `EVENT: ${{ github.event_name }}` and `NEEDS: ${{ toJSON(needs) }}` passed through `env:`, never interpolated into the script.

- [ ] **Step 4: Verify**

Run: `bash scripts/ci-result_test.sh && actionlint && make test-scripts`
Expected: fifteen `ok` lines from the gate tests, actionlint silent, all script suites pass.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/ci.yml scripts/ci-result_test.sh
git commit -S -m "Gated CI on one result job and added weekly fuzzing"
```

### Task 4: Ruleset and CodeQL hardening in repo-settings.sh

**Files:**
- Modify: `scripts/repo-settings.sh` (`rulesets`, `harden`, `show`)
- Test: `scripts/repo-settings_test.sh`

**Interfaces:**
- Consumes: the `CI result` job name (Task 3).
- Produces: the `main` ruleset body Task 8 applies.

- [ ] **Step 1: Update the tests first**
  - Replace `FIVE` with a fixture `JOBS` whose job names are `Static checks`, `Vulnerability scan`, `Test (ubuntu-latest)`, `Test (macos-latest)`, `Integration tests`, `Build and smoke`, `CI result`.
  - Replace `rulesets_needs_five_contexts` with `rulesets_needs_ci_result_job`: jobs without `CI result` → exit 1, stderr exactly `repo-settings: no CI result job in the latest successful CI run on main; merge the CI gate first`, no POST or PUT.
  - In `rulesets_creates_when_absent`, the `main` body must satisfy: `.conditions.ref_name.include == ["~DEFAULT_BRANCH"]`, `.bypass_actors == []`, rule types in order `["deletion","non_fast_forward","required_signatures","pull_request","required_status_checks","code_scanning"]`, `.rules[3].parameters.required_review_thread_resolution == true`, `.rules[3].parameters.allowed_merge_methods == ["merge"]`, `.rules[4].parameters.required_status_checks == [{"context":"CI result","integration_id":15368}]`, `.rules[5].parameters.code_scanning_tools == [{"tool":"CodeQL","security_alerts_threshold":"high_or_higher","alerts_threshold":"errors"}]`. The release-tags assertions stay as they are.
  - In `harden_sends_every_setting_in_order`, the default-setup body asserts `.query_suite == "extended"`.
  - In `show_fails_on_read_error`, expect the line `code_scanning_query_suite: unknown`.

- [ ] **Step 2: Run to verify they fail**

Run: `bash scripts/repo-settings_test.sh`
Expected: FAIL at `rulesets_needs_ci_result_job`.

- [ ] **Step 3: Implement**
  - `rulesets`: keep the latest successful run lookup and its "no successful CI run" refusal. Replace the five-context count with a check that the job names include `CI result` (refusal text above). Build the `main` body with the values the tests pin; drop the `contexts` plumbing. Comment that `15368` is the GitHub Actions app, so only Actions can report the check.
  - `harden`: `query_suite` `extended`.
  - `show`: add `code_scanning_query_suite: $(get "$R/code-scanning/default-setup" '.query_suite' unknown)` after the default setup line.
  - Update the header comment where it describes the rulesets phase.

- [ ] **Step 4: Verify**

Run: `bash scripts/repo-settings_test.sh && shellcheck scripts/repo-settings.sh scripts/repo-settings_test.sh`
Expected: every case `ok`, shellcheck silent. Do not run the script against the live repository.

- [ ] **Step 5: Commit**

```bash
git add scripts/repo-settings.sh scripts/repo-settings_test.sh
git commit -S -m "Required CI result and CodeQL on main, dropped bypass"
```

### Task 5: Split the release into build and publish jobs, attach the bundle

**Files:**
- Modify: `scripts/publish-release.sh`, `.github/workflows/release.yml`, `CHANGELOG.md` (`[Unreleased]`)
- Test: `scripts/release-workflow_test.sh`

**Interfaces:**
- Produces: `scripts/publish-release.sh vX.Y.Z NOTES_FILE BUNDLE_FILE`. Before any `gh` call it compacts `BUNDLE_FILE` with `jq -c .` into `dist/caddy_shack_X.Y.Z_provenance.intoto.jsonl` and requires exactly one line, dying with `publish-release: BUNDLE_FILE is not a single JSON document` otherwise. It attaches that file after `dist/checksums.txt`. Usage line: `usage: $0 vX.Y.Z NOTES_FILE BUNDLE_FILE`.

- [ ] **Step 1: Update the tests first.** `fake` writes `$dir/bundle.json` holding a two-line pretty JSON object; existing publish cases pass it as the third argument.
  - `publish_creates_when_missing` additionally asserts the create call ends `dist/checksums.txt dist/caddy_shack_0.1.0_provenance.intoto.jsonl` and that file has one line equal to `jq -c . bundle.json`.
  - New `publish_refuses_bad_bundle_before_touching_draft`: `is_draft` `true`, bundle file `not json` → exit 1 with the refusal above, and the gh log contains no `release delete` and no `release create`.
  - New `publish_needs_a_bundle`: two arguments only → exit 1, stderr is the usage line.

- [ ] **Step 2: Run to verify they fail**

Run: `bash scripts/release-workflow_test.sh`
Expected: FAIL at the first publish case.

- [ ] **Step 3: Implement the `publish-release.sh` change** and update its header comment.

- [ ] **Step 4: Split `release.yml`.** Keep the header comment's promises and update it to describe the split (adapt jellyfish's "Only the publish job can write..." paragraph).
  - `build` job (`name: Build release`, `needs: ci`, ubuntu, 20 minutes, workflow-level `contents: read` only): every step the current `publish` job runs up to and including `Build and smoke test the archives`, unchanged. `verify-tag.sh` keeps `GH_TOKEN: ${{ github.token }}`; it only reads. Then upload `dist/*.tar.gz` and `dist/checksums.txt` as artifact `dist`, and `${{ runner.temp }}/notes.md` as `release-notes`, both `if-no-files-found: error`, `retention-days: 1`.
  - `publish` job (`name: Publish release`, `needs: build`, ubuntu, 10 minutes, permissions `contents: write`, `id-token: write`, `attestations: write`, same `TAG` env): sparse checkout of the tag with `sparse-checkout: scripts/publish-release.sh`, `sparse-checkout-cone-mode: false`, `persist-credentials: false`, `ref: refs/tags/${{ inputs.tag || github.ref_name }}`; download `dist` to `dist` and `release-notes` to `notes` with `actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333 # v8.0.2`; `cd dist && sha256sum -c checksums.txt`; attest with `id: attest`; then `scripts/publish-release.sh "$TAG" notes/notes.md "$BUNDLE_PATH"` with `GH_TOKEN: ${{ github.token }}`, `GH_REPO: ${{ github.repository }}` and `BUNDLE_PATH: ${{ steps.attest.outputs.bundle-path }}`. No setup-go and no Go anywhere in this job.

- [ ] **Step 5: Add to `[Unreleased]` in CHANGELOG.md**, under `### Added`: `- Releases attach their build provenance as a Sigstore bundle, caddy_shack_<version>_provenance.intoto.jsonl, so an archive can be verified without GitHub's attestation API.`

- [ ] **Step 6: Verify**

Run: `make test-scripts && actionlint && scripts/changelog.sh check && scripts/docs-check.sh`
Expected: all pass. `grep -n 'setup-go\|go ' .github/workflows/release.yml` shows Go only inside the `build` job.

- [ ] **Step 7: Commit**

```bash
git add scripts/publish-release.sh scripts/release-workflow_test.sh .github/workflows/release.yml CHANGELOG.md
git commit -S -m "Split release build from publish and attached bundle"
```

### Task 6: Tighter verification instructions

**Files:**
- Modify: `scripts/release-notes.sh`, `SECURITY.md` (`## Verifying a release`), `CHANGELOG.md` (`[Unreleased]`)
- Test: `scripts/release-workflow_test.sh` (`release_notes_first_release`)

**Interfaces:**
- Consumes: the bundle filename from Task 5.

- [ ] **Step 1: Update `release_notes_first_release` first.** The verify block must now contain, as exact lines: `  --source-ref refs/tags/v0.1.0 \`, `  --deny-self-hosted-runners`, a `--bundle caddy_shack_0.1.0_provenance.intoto.jsonl` line in a second `gh attestation verify` command, and `sha256sum -c checksums.txt --ignore-missing`.

- [ ] **Step 2: Run to verify it fails**

Run: `bash scripts/release-workflow_test.sh`
Expected: FAIL at `release_notes_first_release`.

- [ ] **Step 3: Update `release-notes.sh`** to print both commands (API and `--bundle`), modelled on jellyfish's notes but with the tag source ref.

- [ ] **Step 4: Rewrite SECURITY.md's verify section.** The pinned command (`--repo`, `--signer-workflow`, `--source-ref refs/tags/v<version>`, `--deny-self-hosted-runners`); a paragraph saying `--repo` alone only proves some workflow in this repository signed it, from any ref, and the three flags pin it to the release workflow run from that release's tag on a GitHub-hosted runner; the `--bundle` variant, noting v0.1.0 predates the bundle; the offline variant with `gh attestation trusted-root > trusted_root.jsonl` and `--custom-trusted-root trusted_root.jsonl`; keep the `shasum` note for macOS.

- [ ] **Step 5: Add to `[Unreleased]`**, under `### Changed`: `- The verification commands in the release notes and SECURITY.md pin the release tag and refuse self-hosted runners, with a variant that checks the attached bundle offline.`

- [ ] **Step 6: Verify**

Run: `make test-scripts && scripts/docs-check.sh && scripts/changelog.sh check`
Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add scripts/release-notes.sh scripts/release-workflow_test.sh SECURITY.md CHANGELOG.md
git commit -S -m "Pinned the source ref and runner in release checks"
```

### Task 7: Docs, templates and Scorecard tidy

**Files:**
- Modify: `.github/ISSUE_TEMPLATE/bug_report.yml`, `CONTRIBUTING.md`, `.gitignore`, `.github/workflows/scorecard.yml`

- [ ] **Step 1: Bug report privacy block.** First body item, `type: markdown`: do not paste private keys or ACME account keys (anything under the certificate store), internal hostnames or LAN addresses you would rather keep private; trim `doctor` output to the parts that show the problem.

- [ ] **Step 2: CONTRIBUTING.md.**
  - Under "Before you start", add the line linking `[Code of Conduct](CODE_OF_CONDUCT.md)`.
  - Step 2's list of what `make pre-ci` runs gains `govulncheck` and `a short fuzz run of 5s per target`, plus the sentence: if you touch code that parses untrusted input or `state.json`, run `make fuzz` for longer (15s each by default, or set `FUZZTIME`).
  - Step 4 becomes: the `CI result` check and CodeQL must pass before merging, and it lands as a merge commit so signed commits keep their signatures. No reviewer requirement, but expect questions.

- [ ] **Step 3: `.gitignore`.** Add `CLAUDE.local.md` beside the existing Claude/Superpowers entries.

- [ ] **Step 4: `scorecard.yml`.** Add `timeout-minutes: 10` to `analysis`. Reduce job permissions to `security-events: write` and `id-token: write`. Drop the private repository comment and the `if:` on the upload step. Add jellyfish's three-line header comment saying what Scorecard checks.

- [ ] **Step 5: Verify**

Run: `actionlint && scripts/docs-check.sh && make test-scripts`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add .github/ISSUE_TEMPLATE/bug_report.yml CONTRIBUTING.md .gitignore .github/workflows/scorecard.yml
git commit -S -m "Tidied contributor docs, bug template and Scorecard"
```

### Task 8: Final review, merge, then apply the live settings

Not code. Every step that writes to GitHub waits for Keith's go-ahead.

- [ ] **Step 1: Three reviewers on the integration branch** (security, quality, compliance with the house commit and style rules), each fed the spec, this plan and `git diff main...kaizen/project_improvements`. Fix confirmed findings in their own dev branch and merge back.

- [ ] **Step 2: Verify the branch yourself.** `git log --format='%h %G? %s' main..kaizen/project_improvements` shows every commit `G`, past tense, 60 characters or fewer, no attribution trailers. Re-run `make test-scripts`, `go test -race ./...` three times, `golangci-lint run`, `actionlint`, `make fuzz FUZZTIME=5s`.

- [ ] **Step 3: Run `scripts/history-scan.sh`** (fetch PR heads last, see the release memory gotcha), then with go-ahead push the branch and open the PR. The PR's own run shows the `CI result` job passing with `fuzz` skipped. Confirm a `workflow_dispatch` run on the branch passes with all six jobs.

- [ ] **Step 4: After merge and a green CI on `main`**, with go-ahead: `DRY_RUN=1 scripts/repo-settings.sh rulesets`, read the bodies, then `scripts/repo-settings.sh rulesets` and `scripts/repo-settings.sh harden`.

- [ ] **Step 5: Confirm live state.**

Run: `scripts/repo-settings.sh show` and `gh api repos/bawdo/caddy_shack/rulesets --jq '.[].id' | xargs -I{} gh api repos/bawdo/caddy_shack/rulesets/{} --jq '{name, bypass_actors, rules: [.rules[].type]}'`
Expected: `code_scanning_query_suite: extended`; `main` has `bypass_actors: []` and rule types ending `required_status_checks`, `code_scanning`. Then open a trivial PR (a CHANGELOG typo fix or similar) and confirm `CI result` and CodeQL are listed as required.

- [ ] **Step 6: The first real release is the proof of Task 5.** Watch that run: `build` has no write permissions, `publish` has no Go, and the release carries four archives, `checksums.txt` and the `.intoto.jsonl` bundle. Then `gh attestation verify` one archive with both commands from its notes.

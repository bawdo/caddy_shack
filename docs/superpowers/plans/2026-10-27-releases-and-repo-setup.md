# Tag-driven Releases and Repository Setup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A signed `vX.Y.Z` tag pushed to `main` publishes a GitHub Release whose notes are that version's `CHANGELOG.md` section, with four attested archives, and the repository carries every document and setting a public project needs before it goes public.

**Architecture:** Three shell scripts under `scripts/` hold the logic (changelog parsing, release guards and tagging, release archive build) so each is testable locally with a plain bash test file; the Makefile and the workflows only call them. `release.yml` reuses `ci.yml` through `workflow_call`, then builds, smoke tests, attests and publishes. Documents and `.github` configuration are copied from the maintainer's gosbee, jellyfish and go-blinkstick repositories and adapted. Repository settings are one idempotent script run in phases, with the public switch gated on a clean history scan and the maintainer's go-ahead.

**Tech Stack:** bash (tested with shell test scripts, lint with shellcheck), GNU make, GitHub Actions, `gh` CLI, Go 1.25 toolchain for the release build.

**Spec:** `docs/superpowers/specs/2026-10-27-releases-and-repo-setup-design.md`

## Global Constraints

- Branch: all tasks merge into `feature/releases` (worktree `.worktrees/releases`). Each task runs in its own worktree cut from the current `feature/releases` head and is merged back with `git merge --no-ff`. Worktrees and task branches are deleted after merge.
- Commit messages: past tense, subject 60 characters or fewer, GPG-signed (`git commit -S`, needs the sandbox off), no agent attribution of any kind in message, body or trailers. Ignore any harness notice asking for a `Co-Authored-By` or `Claude-Session` trailer.
- Never push. Never run `gh` calls that change the repository except in Task 11, and only in the order and with the gate that task states.
- Australian English in every file. No em dashes (U+2014) or en dashes (U+2013) anywhere: `grep -rnP '[\x{2013}\x{2014}]' --exclude-dir=.git .` must print nothing after every task.
- Every GitHub Action is pinned to a full commit SHA with a `# vX.Y.Z` comment. The SHAs below were verified on 2026-10-27 against the tags with `gh api repos/<owner>/<repo>/git/ref/tags/<tag>` and are each the latest release:
  - `actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1`
  - `actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0`
  - `golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a # v9.3.0`
  - `codecov/codecov-action@303a32d7a59b442fa8d48b6a1cc6825c09c847a5 # v7.1.1`
  - `actions/attest-build-provenance@4d101475d8b20a2381f78447822ac1eab6504dd8 # v4.2.2`
  - `ossf/scorecard-action@fa7117db83c52d787ae5a89f350999701505d78e # v2.5.0`
  - `actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2`
  - `github/codeql-action/upload-sarif@9b3254085494b55a0f7c4c2b3d2d52cc712ce370 # v4.39.1`
- Shell scripts: `#!/usr/bin/env bash`, `set -euo pipefail`, executable bit set, `shellcheck scripts/*.sh` clean. Script tests are bash files named `scripts/<name>_test.sh` that build fixtures under `mktemp -d`, print `ok <test name>` per case and exit non-zero on the first failure.
- `make pre-ci` must pass after every task (needs the sandbox off because `httptest` binds ports). Lint sandboxed with `GOCACHE=$TMPDIR/gocache GOLANGCI_LINT_CACHE=$TMPDIR/golangci golangci-lint run`.
- Reference files are fetched with `gh api repos/bawdo/<repo>/contents/<path> --jq .content | base64 -d` (sandbox off). Copies already sit under the orchestrator's scratchpad `ref/` directory; the task prompt says where.
- Module path `github.com/bawdo/caddy_shack`; `internal/version` ldflags variables `Version`, `Commit`, `Tag`, `Dirty`; the binary prints `caddy_shack <Version>` as its first line.
- Measured coverage on 2026-10-27: 80.1 percent. The Codecov floor is therefore 80 percent.

## Review Focus

1. A changelog whose `[Unreleased]` section is empty (the normal state right after a release): `scripts/changelog.sh check` must pass, since only released versions need a body. Pinned in Task 1.
2. A `## [0.1.0] - 2026-10-27` heading written with a `v` by habit (`## [v0.1.0] - ...`): `check` must fail with a message naming the line, not silently treat it as unreleased. Pinned in Task 1.
3. `make release RELEASE_VERSION=0.2.0` run on a branch other than `main`, or with the newest changelog version still `0.1.0`: each guard must refuse before `git tag` runs, and no tag may exist afterwards. Pinned in Task 2.
4. A tag pushed from a commit that is on `main` but whose tag object is unsigned, or signed with a key GitHub does not know: the publish job must stop before building. Pinned in Task 3 by the `verify-tag.sh` test using a fake `gh` on `PATH`.
5. A re-run of the publish job after a partial failure that left a draft release: the draft is deleted and recreated; a published release of the same name stops the job. Pinned in Task 3 by the `publish-release.sh` test using a fake `gh`.

---

### Task 1: CHANGELOG.md and scripts/changelog.sh

**Files:**
- Create: `CHANGELOG.md`
- Create: `scripts/changelog.sh`
- Create: `scripts/changelog_test.sh`
- Modify: `scripts/pre-ci-check.sh` (add a check after "CLI Smoke")

**Interfaces:**
- Produces: `scripts/changelog.sh versions` prints released versions newest first, one per line, as `X.Y.Z` with no `v`. `scripts/changelog.sh notes X.Y.Z [owner/repo]` prints the section body; with a repo, relative links become `https://github.com/<owner/repo>/blob/vX.Y.Z/<path>`. `scripts/changelog.sh check` exits 0 and prints `CHANGELOG.md is consistent (N versions)` or exits 1 with one `changelog: ...` line per problem. `CHANGELOG` env var overrides the file path. The script never reads the network; the tag comparison uses `git tag -l 'v[0-9]*'` and tolerates a directory that is not a git repository (no tags).
- Reference: `ref/gosbee/scripts/changelog.sh` (structure, `notes`, `absolute_links`, duplicate and link checks) and `ref/jellyfish/scripts/changelog-version.sh` (`semver_cmp`, `highest_tag`, "not behind the highest tag" check). Headings here carry no `v`, unlike gosbee.

- [ ] **Step 1: Write `scripts/changelog_test.sh` with these cases, each building a fixture file and asserting on stdout, stderr and exit code**

```
versions_lists_newest_first        fixture with [Unreleased], [0.2.0], [0.1.0] -> "0.2.0\n0.1.0"
notes_prints_section_body          notes 0.1.0 -> the lines between its heading and the next "## ", blank lines trimmed, link reference lines excluded
notes_rewrites_relative_links      notes 0.1.0 bawdo/caddy_shack on a body containing "[guide](docs/x.md)" -> "(https://github.com/bawdo/caddy_shack/blob/v0.1.0/docs/x.md)"; "[a](https://e.com)", "[b](#anchor)" and "[c](mailto:x@y)" unchanged
notes_unknown_version_fails        notes 9.9.9 -> exit 1, stderr contains "no section for 9.9.9"
check_passes_on_empty_unreleased   [Unreleased] with no body, [0.1.0] with body and link -> exit 0, stdout "CHANGELOG.md is consistent (1 versions)"
check_rejects_v_prefix             "## [v0.1.0] - 2026-10-27" -> exit 1, stderr contains "bad heading: ## [v0.1.0] - 2026-10-27"
check_rejects_missing_date         "## [0.1.0]" -> exit 1, stderr contains "missing release date"
check_rejects_duplicate_version    two [0.1.0] headings -> exit 1, stderr contains "duplicate versions: 0.1.0"
check_rejects_missing_link         [0.1.0] with body, no "[0.1.0]: https://" line -> exit 1, stderr contains "missing link reference for [0.1.0]"
check_rejects_empty_body           [0.1.0] heading immediately followed by the link block -> exit 1, stderr contains "empty section for 0.1.0"
check_rejects_version_behind_tag   fixture in a temp git repo with tag v0.2.0 and newest changelog 0.1.0 -> exit 1, stderr contains "newest changelog version 0.1.0 is behind tag v0.2.0"
check_rejects_tag_without_section  temp git repo with tag v0.1.0 and v0.0.1, changelog has only 0.1.0 -> exit 1, stderr contains "tag v0.0.1 has no section"
check_outside_git_repo_passes      fixture in a plain temp dir (GIT_CEILING_DIRECTORIES set to it) -> exit 0
```

Run the git-based cases inside `git -C "$tmp" init -q` repositories with `git -C "$tmp" commit --allow-empty -q -m x` and `git -C "$tmp" tag v0.2.0`; run the script with `cd "$tmp"`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash scripts/changelog_test.sh`
Expected: fails at the first case because `scripts/changelog.sh` does not exist.

- [ ] **Step 3: Write `scripts/changelog.sh`**

Adapt gosbee's script. `SEMVER='[0-9]+\.[0-9]+\.[0-9]+'`, heading regexes `^## \[Unreleased\]$` and `^## \[($SEMVER)\] - [0-9]{4}-[0-9]{2}-[0-9]{2}$`. `check` collects every problem before exiting, prints each as `changelog: <message>` on stderr, and runs: heading shape, missing date, duplicates, link reference, empty body, every `v*` tag has a section, newest version not behind the highest tag (jellyfish `semver_cmp`). Use `perl -pe` for `absolute_links` as gosbee does.

- [ ] **Step 4: Run the test to verify it passes, and shellcheck**

Run: `bash scripts/changelog_test.sh && shellcheck scripts/changelog.sh scripts/changelog_test.sh`
Expected: thirteen `ok` lines, shellcheck silent.

- [ ] **Step 5: Write `CHANGELOG.md`**

Header lines as gosbee's (Keep a Changelog 1.1.0, Semantic Versioning). Then `## [Unreleased]` with no body, then `## [0.1.0] - 2026-10-27` with one `### Added` section whose bullets summarise what ships: install, start, stop, restart and upgrade of the Caddy container; register and deregister with container-mode upstreams and subdomains; list, status and sync; doctor with fixes; native Linux relay; LAN exposure; Let's Encrypt via manual DNS-01; `version`. Draw wording from `README.md` sections, two lines per bullet at most. Link block: `[Unreleased]: https://github.com/bawdo/caddy_shack/compare/v0.1.0...HEAD` and `[0.1.0]: https://github.com/bawdo/caddy_shack/releases/tag/v0.1.0`.

- [ ] **Step 6: Verify the real changelog**

Run: `scripts/changelog.sh check && scripts/changelog.sh notes 0.1.0 bawdo/caddy_shack`
Expected: `CHANGELOG.md is consistent (1 versions)` and the 0.1.0 body.

- [ ] **Step 7: Add Check 10 "Changelog" to `scripts/pre-ci-check.sh`**

A `check_changelog` function after `check_cli_smoke`, same `print_header` / `record_result` pattern, running `scripts/changelog.sh check` and `bash scripts/changelog_test.sh`, output to `coverage/pre-ci-changelog.txt`. Register it wherever the other checks are called. Update the header comment's check count.

- [ ] **Step 8: Run pre-ci and commit**

Run (sandbox off): `make pre-ci`
Expected: all checks pass including "Changelog".

```bash
git add CHANGELOG.md scripts/changelog.sh scripts/changelog_test.sh scripts/pre-ci-check.sh
git commit -S -m "Added the changelog and its checking script"
```

---

### Task 2: make release, make changelog, and ci.yml rework

**Files:**
- Create: `scripts/release.sh`
- Create: `scripts/release_test.sh`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/dependabot.yml`

**Interfaces:**
- Consumes: `scripts/changelog.sh check` and `versions` (Task 1).
- Produces: `scripts/release.sh X.Y.Z` creates the signed annotated tag `vX.Y.Z` with message `vX.Y.Z` on HEAD and prints `git push origin vX.Y.Z` as its last line, or exits 1 with `release: <reason>` before tagging. Guards, in order: argument matches `^[0-9]+\.[0-9]+\.[0-9]+$`; `git status --porcelain` empty; current branch is `main`; `git fetch origin main` succeeds and `git rev-parse HEAD` equals `git rev-parse origin/main`; `scripts/changelog.sh check` passes; `scripts/changelog.sh versions | head -1` equals the argument; `git rev-parse -q --verify refs/tags/vX.Y.Z` fails (no such tag). Env `RELEASE_BRANCH` (default `main`) and `RELEASE_REMOTE` (default `origin`) exist so the test can run against a fixture. `RELEASE_SIGN=0` skips `-s` on `git tag` (test only; the Makefile never sets it).
- Produces: `ci.yml` is callable with `workflow_call` and passes the `static`, `test`, `integration` and `build` jobs; job names unchanged: `Static checks`, `Test (${{ matrix.os }})`, `Integration tests`, `Build and smoke`.

- [ ] **Step 1: Write `scripts/release_test.sh`**

Each case builds a bare "origin" repo and a clone under `mktemp -d`, copies `scripts/changelog.sh` and `scripts/release.sh` into the clone, commits a `CHANGELOG.md` whose newest version is `0.1.0`, pushes to the bare origin, and runs the script with `RELEASE_SIGN=0`:

```
refuses_bad_version          release.sh 1.2 -> exit 1, stderr "release: version must be X.Y.Z"
refuses_dirty_tree           touch extra file -> exit 1, stderr "release: working tree is not clean"
refuses_wrong_branch         git checkout -b topic -> exit 1, stderr "release: current branch is topic, not main"
refuses_behind_origin        extra commit on origin -> exit 1, stderr "release: main is not up to date with origin/main"
refuses_version_mismatch     release.sh 0.2.0 -> exit 1, stderr "release: newest changelog version is 0.1.0, not 0.2.0"
refuses_existing_tag         git tag v0.1.0 first -> exit 1, stderr "release: tag v0.1.0 already exists"
tags_and_prints_push         clean clone, release.sh 0.1.0 -> exit 0, `git tag -l v0.1.0` prints v0.1.0, `git cat-file -t v0.1.0` is tag, last stdout line "git push origin v0.1.0"
never_pushes                 after tags_and_prints_push, `git -C origin tag -l` is empty
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash scripts/release_test.sh`
Expected: fails because `scripts/release.sh` does not exist.

- [ ] **Step 3: Write `scripts/release.sh`**

Guards in the order above, each `die`ing with the exact message. Tag with `git tag -s "v$1" -m "v$1"` (drop `-s` when `RELEASE_SIGN=0`). Print the push command last.

- [ ] **Step 4: Run the test and shellcheck**

Run: `bash scripts/release_test.sh && shellcheck scripts/release.sh scripts/release_test.sh`
Expected: eight `ok` lines, shellcheck silent.

- [ ] **Step 5: Add the Makefile targets**

```make
changelog: ## Check CHANGELOG.md is well formed and matches the tags
	scripts/changelog.sh check

release: ## Tag a release: make release RELEASE_VERSION=X.Y.Z (never pushes)
	@test -n "$(RELEASE_VERSION)" || { echo "usage: make release RELEASE_VERSION=X.Y.Z"; exit 1; }
	scripts/release.sh $(RELEASE_VERSION)
```

Add both to `.PHONY`. Verify: `make release` prints the usage line and exits 1; `make changelog` prints the consistent line.

- [ ] **Step 6: Rework `.github/workflows/ci.yml`**

- Remove `tags: [ 'v*' ]` from `on.push`.
- Add `workflow_call:` with `secrets: CODECOV_TOKEN: { required: false }`.
- Concurrency group becomes the literal `ci-${{ github.ref }}`; keep `cancel-in-progress` as is.
- `static` job: checkout with `fetch-depth: 0` and `persist-credentials: false`; add a step `Check CHANGELOG.md` running `scripts/changelog.sh check` and `bash scripts/changelog_test.sh` before the lint step; add a step `Lint shell scripts` running `shellcheck scripts/*.sh` (shellcheck is preinstalled on `ubuntu-latest`).
- Every `uses:` pinned per Global Constraints. Every checkout gets `persist-credentials: false`. The golangci-lint `version: v2.9.0` input stays.
- Add a comment above `on:` saying releases are triggered by `release.yml`, which calls this workflow.

- [ ] **Step 7: Align `.github/dependabot.yml`**

Replace with gosbee's structure for `gomod` and `github-actions` on `/` only (no `/tools`): weekly on Monday with `day: monday` and `time: "09:00"`, `timezone: Australia/Sydney`, `open-pull-requests-limit: 5`, `cooldown: default-days: 7`, `commit-message: prefix: "Bumped"`, `labels: [dependencies]`, groups `go-minor-patch` and `github-actions-minor-patch` on minor and patch.

- [ ] **Step 8: Validate the YAML, run pre-ci, commit**

Run: `python3 -c "import yaml,sys; [yaml.safe_load(open(f)) for f in sys.argv[1:]]" .github/workflows/ci.yml .github/dependabot.yml && grep -n 'uses:' .github/workflows/ci.yml`
Expected: no error; every `uses:` line has a 40-hex SHA and a `# v` comment.

Run (sandbox off): `make pre-ci`
Expected: pass.

```bash
git add scripts/release.sh scripts/release_test.sh Makefile .github/workflows/ci.yml .github/dependabot.yml
git commit -S -m "Added the release tag guard and made CI callable"
```

---

### Task 3: Release workflow and its scripts

**Files:**
- Create: `scripts/build-release.sh`
- Create: `scripts/verify-tag.sh`
- Create: `scripts/publish-release.sh`
- Create: `scripts/release-notes.sh`
- Create: `scripts/release-workflow_test.sh`
- Modify: `.gitignore` (add `/dist/`)
- Create: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: `scripts/changelog.sh check` and `notes` (Task 1); `ci.yml` via `workflow_call` (Task 2).
- Produces: `scripts/build-release.sh X.Y.Z COMMIT` builds `dist/caddy_shack_X.Y.Z_<os>_<arch>.tar.gz` for darwin and linux times amd64 and arm64, each containing a top-level directory `caddy_shack_X.Y.Z_<os>_<arch>/` with `caddy_shack`, `LICENSE`, `README.md`, `CHANGELOG.md`; writes `dist/checksums.txt` with `sha256sum` (falls back to `shasum -a 256` when `sha256sum` is absent); then extracts the archive for the host platform into `$TMPDIR` and fails unless `caddy_shack version` prints `caddy_shack vX.Y.Z` on its first line. Build flags: `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X ...Version=vX.Y.Z -X ...Commit=COMMIT -X ...Tag=vX.Y.Z -X ...Dirty=false"`.
- Produces: `scripts/verify-tag.sh vX.Y.Z` (env `GITHUB_REPOSITORY`) exits 0 when `gh api repos/$GITHUB_REPOSITORY/git/ref/tags/vX.Y.Z` is an annotated tag (`.object.type == "tag"`) and `gh api repos/$GITHUB_REPOSITORY/git/tags/<sha>` has `.verification.verified == true` and `.verification.reason == "valid"`; otherwise exits 1 with `verify-tag: <reason>`. GitHub checks the signature against the keys on the maintainer's account, so no key is imported into the runner.
- Produces: `scripts/publish-release.sh vX.Y.Z NOTES_FILE` (env `GITHUB_REPOSITORY`): if `gh release view vX.Y.Z --json isDraft --jq .isDraft` prints `false`, exit 1 with `publish-release: vX.Y.Z is already published`; if `true`, `gh release delete vX.Y.Z --yes`; then `gh release create vX.Y.Z --draft --verify-tag --title vX.Y.Z --notes-file NOTES_FILE dist/*.tar.gz dist/checksums.txt`, then `gh release edit vX.Y.Z --draft=false --latest`, then print `gh release view vX.Y.Z --json url --jq .url`.
- Produces: `scripts/release-notes.sh X.Y.Z` prints `changelog.sh notes X.Y.Z $GITHUB_REPOSITORY`, a blank line, a "Verify a downloaded archive with:" block (`gh attestation verify caddy_shack_X.Y.Z_<os>_<arch>.tar.gz --repo bawdo/caddy_shack` and `sha256sum -c checksums.txt --ignore-missing`), and, when a lower `v*` tag exists, `**Full Changelog**: https://github.com/bawdo/caddy_shack/compare/vPREV...vX.Y.Z` using jellyfish's previous-tag pipeline.

- [ ] **Step 1: Write `scripts/release-workflow_test.sh`**

Cases use a fake `gh` placed first on `PATH` (a bash script that dispatches on its arguments and reads canned JSON from files the test writes):

```
verify_tag_accepts_valid_signed_tag   ref -> {"object":{"type":"tag","sha":"abc"}}, tags/abc -> {"verification":{"verified":true,"reason":"valid"}} -> exit 0
verify_tag_rejects_lightweight_tag    ref -> {"object":{"type":"commit",...}} -> exit 1, stderr "verify-tag: v0.1.0 is not an annotated tag"
verify_tag_rejects_unverified         verified false, reason "unsigned" -> exit 1, stderr "verify-tag: v0.1.0 signature not verified by GitHub (unsigned)"
publish_stops_on_published_release    release view prints false -> exit 1, stderr "publish-release: v0.1.0 is already published"; fake gh log shows no "release create"
publish_replaces_draft                release view prints true -> fake gh log shows "release delete v0.1.0 --yes" then "release create v0.1.0 --draft --verify-tag" then "release edit v0.1.0 --draft=false --latest"
publish_creates_when_missing          release view exits 1 -> log shows no delete, then create, then edit
release_notes_first_release           temp repo with no tags: output starts with the 0.1.0 body, contains "gh attestation verify", contains no "Full Changelog"
release_notes_with_previous_tag       temp repo with tag v0.1.0, notes for 0.2.0: last line "**Full Changelog**: https://github.com/bawdo/caddy_shack/compare/v0.1.0...v0.2.0"
build_release_produces_archives       run build-release.sh 0.0.1 deadbeef from the repo root -> four dist/*.tar.gz, checksums.txt with four lines, `tar -tzf` of each lists caddy_shack_0.0.1_<os>_<arch>/caddy_shack, LICENSE, README.md, CHANGELOG.md; exit 0 (smoke test ran on the host archive)
build_release_smoke_fails_on_wrong_version  BUILD_RELEASE_EXPECT=v9.9.9 env override makes the smoke assertion expect v9.9.9 -> exit 1, stderr "build-release: built binary reports caddy_shack v0.0.1, expected caddy_shack v9.9.9"
```

The build cases need `LICENSE` to exist; until Task 4 lands, create a placeholder `LICENSE` in the temp copy of the repo used by the test (copy the tree with `git archive HEAD | tar -x -C "$tmp"` then `touch "$tmp/LICENSE"` if missing). The build cases take a minute; keep them last.

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash scripts/release-workflow_test.sh`
Expected: fails at the first case because `scripts/verify-tag.sh` does not exist.

- [ ] **Step 3: Write the four scripts**

`scripts/verify-tag.sh`, `scripts/publish-release.sh`, `scripts/release-notes.sh`, `scripts/build-release.sh` per the Interfaces block. `build-release.sh` loops `for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64` as gosbee does and copies the three files as jellyfish does; host platform for the smoke test from `go env GOOS GOARCH`.

- [ ] **Step 4: Run the test and shellcheck**

Run (sandbox off, the build writes `dist/`): `bash scripts/release-workflow_test.sh && shellcheck scripts/*.sh`
Expected: ten `ok` lines, shellcheck silent. Add `/dist/` to `.gitignore`.

- [ ] **Step 5: Write `.github/workflows/release.yml`**

Header comment (what triggers it, that notes come from the changelog, the re-run path). `permissions: contents: read`. `on: push: tags: ['v*']` and `workflow_dispatch` with input `tag` (string, required, description "Existing vX.Y.Z tag to publish again"). `concurrency: group: release-${{ github.ref }}`, `cancel-in-progress: false`. Jobs:

1. `ci`: `uses: ./.github/workflows/ci.yml`, `secrets: CODECOV_TOKEN: ${{ secrets.CODECOV_TOKEN }}`.
2. `publish`: `needs: ci`, `ubuntu-latest`, `timeout-minutes: 20`, permissions `contents: write`, `id-token: write`, `attestations: write`, env `GH_TOKEN: ${{ github.token }}`, `TAG: ${{ inputs.tag || github.ref_name }}`. Steps: checkout (`fetch-depth: 0`, `persist-credentials: false`, `ref: ${{ inputs.tag || github.ref }}`); `Check the tag is on main` (`git merge-base --is-ancestor "$(git rev-parse "$TAG^{commit}")" origin/main`); `Check the tag is signed` (`scripts/verify-tag.sh "$TAG"`); `Check the changelog` (`scripts/changelog.sh check`); `Write release notes` (`scripts/release-notes.sh "${TAG#v}" > "$RUNNER_TEMP/notes.md"` then `cat` it); setup-go `1.25.x`, `check-latest: true`, `cache: true`; `Build and smoke test the archives` (`scripts/build-release.sh "${TAG#v}" "$(git rev-parse "$TAG^{commit}")"`); `Attest build provenance` with `subject-checksums: dist/checksums.txt`; `Publish the release` (`scripts/publish-release.sh "$TAG" "$RUNNER_TEMP/notes.md"`).

Every `uses:` pinned per Global Constraints. Shell steps use `set -euo pipefail` through the scripts; the inline `merge-base` step is `run: |` with `set -euo pipefail` and an `::error::` line as gosbee has.

- [ ] **Step 6: Validate, run pre-ci, commit**

Run: `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/release.yml'))" && grep -n 'uses:' .github/workflows/release.yml`
Expected: no error, three pinned `uses:` lines plus the `./.github/workflows/ci.yml` call.

Run (sandbox off): `make pre-ci`
Expected: pass.

```bash
git add scripts/build-release.sh scripts/verify-tag.sh scripts/publish-release.sh scripts/release-notes.sh scripts/release-workflow_test.sh .github/workflows/release.yml .gitignore
git commit -S -m "Added the tag-driven release workflow"
```

---

### Task 4: Licence and community documents

**Files:**
- Create: `LICENSE`
- Create: `CONTRIBUTING.md`
- Create: `SECURITY.md`
- Create: `CODE_OF_CONDUCT.md`
- Create: `scripts/docs-check.sh`
- Create: `scripts/docs-check_test.sh`

**Interfaces:**
- Consumes: the release ritual from Task 2 (`make release RELEASE_VERSION=X.Y.Z`, then the printed push command) and the verification commands from Task 3.
- Produces: `scripts/docs-check.sh` exits 1 with `docs-check: <file>:<line>: <reason>` for any em or en dash in a tracked text file, any `[text](relative/path)` link in a root `*.md` or `.github/**/*.md` file whose target does not exist, or any of the required files missing (`LICENSE`, `CHANGELOG.md`, `CONTRIBUTING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`, `.github/PULL_REQUEST_TEMPLATE.md`, `.github/CODEOWNERS`). Exits 0 printing `docs are consistent` otherwise. Later tasks add to its required list.
- Reference: `ref/go-blinkstick/LICENSE` (copy verbatim, already "Copyright (c) 2026 Keith Bawden"), `ref/jellyfish/CONTRIBUTING.md` and `ref/jellyfish/SECURITY.md` (voice and shape), `ref/gosbee/SECURITY.md` (scope section), `ref/go-blinkstick/CODE_OF_CONDUCT.md` (copy verbatim, Contributor Covenant 2.1 with the `@bawdo` enforcement line; its American spellings are the Covenant's own text and stay).

- [ ] **Step 1: Write `scripts/docs-check.sh` and a case list in `scripts/docs-check_test.sh`**

```
fails_on_em_dash            temp tree whose README.md line 1 has the U+2014 character (write it with printf, not a literal) -> exit 1, stderr contains "README.md:1: em or en dash"
fails_on_broken_link        "[x](docs/missing.md)" -> exit 1, stderr contains "README.md:1: broken link docs/missing.md"
fails_on_missing_required   no SECURITY.md -> exit 1, stderr contains "SECURITY.md: required file missing"
passes_on_clean_tree        all required files present, links resolve -> exit 0, stdout "docs are consistent"
```

Run: `bash scripts/docs-check_test.sh`
Expected: fails because the script does not exist. Write the script (`git ls-files` for the file list, `grep -nP '[\x{2013}\x{2014}]'`, a `grep -oE '\]\([^)]+\)'` pass skipping `http`, `mailto:` and `#` targets, stripping `#fragment` from the rest). Rerun: four `ok` lines.

- [ ] **Step 2: Write `LICENSE` and `CODE_OF_CONDUCT.md`**

Byte-for-byte copies of the go-blinkstick files. Verify: `diff LICENSE <ref>/LICENSE` silent.

- [ ] **Step 3: Write `SECURITY.md`**

Shape of jellyfish's, sections: intro paragraph on what counts as a security bug here (the admin API trust boundary and the local root CA key, Let's Encrypt key handling, the LAN listener; point at the README's "Security notes"), Supported versions (newest release only), Reporting a vulnerability (`https://github.com/bawdo/caddy_shack/security/advisories/new`, what to include: `caddy_shack version` and `caddy_shack doctor` output, the Docker engine, acknowledgement within seven days, credit unless declined), Verifying a release (the two commands from Task 3's notes, with `<version>`, `<os>`, `<arch>` placeholders).

- [ ] **Step 4: Write `CONTRIBUTING.md`**

Shape of jellyfish's: one person, light process; open an issue for anything beyond a small fix; security via SECURITY.md; making a change (`make pre-ci`, changelog line under `[Unreleased]`, signed past-tense commits with a subject of 60 characters or fewer, Australian English, pull request, CI must pass); "How releases happen": rename `[Unreleased]` to `[X.Y.Z] - YYYY-MM-DD`, add the compare link, start a fresh empty `[Unreleased]`, merge to `main`, then on an up-to-date `main` run `make release RELEASE_VERSION=X.Y.Z` and the `git push origin vX.Y.Z` it prints; the workflow re-runs CI, checks the tag is on `main` and signed, builds the four archives, attests the checksums and publishes with the changelog section as the notes; a failed publish is re-run from the Actions tab with the tag name; a defect gets a new patch version, never a moved tag. Mention `scripts/changelog.sh notes X.Y.Z bawdo/caddy_shack` to preview notes.

- [ ] **Step 5: Verify and commit**

Run: `scripts/docs-check.sh; echo exit=$?`
Expected: exit 1 naming only `.github/PULL_REQUEST_TEMPLATE.md` and `.github/CODEOWNERS` as missing (Task 5 adds them). No dash or link failures.

Run (sandbox off): `make pre-ci`
Expected: pass.

```bash
git add LICENSE CONTRIBUTING.md SECURITY.md CODE_OF_CONDUCT.md scripts/docs-check.sh scripts/docs-check_test.sh
git commit -S -m "Added the licence and community documents"
```

---

### Task 5: GitHub templates, Codecov and Scorecard

**Files:**
- Create: `.github/PULL_REQUEST_TEMPLATE.md`
- Create: `.github/ISSUE_TEMPLATE/bug_report.yml`
- Create: `.github/ISSUE_TEMPLATE/feature_request.yml`
- Create: `.github/ISSUE_TEMPLATE/config.yml`
- Create: `.github/CODEOWNERS`
- Create: `.github/workflows/scorecard.yml`
- Modify: `.codecov.yml`
- Modify: `scripts/docs-check.sh` (required list gains the three issue forms and `scorecard.yml`)
- Modify: `scripts/pre-ci-check.sh` (Check 10 also runs `scripts/docs-check.sh`)
- Modify: `.github/workflows/ci.yml` (static job runs `scripts/docs-check.sh` and `bash scripts/docs-check_test.sh` in the changelog step, renamed `Check changelog and docs`)

**Interfaces:**
- Consumes: `scripts/docs-check.sh` (Task 4).
- Reference: `ref/jellyfish/.github/PULL_REQUEST_TEMPLATE.md`, `ref/go-blinkstick/.github/ISSUE_TEMPLATE/*`, `ref/go-blinkstick/.github/CODEOWNERS`, `ref/go-blinkstick/.github/workflows/scorecard.yml` (copy, pins already match Global Constraints), `ref/jellyfish/.codecov.yml`.

- [ ] **Step 1: Extend `scripts/docs-check.sh` required list and its test**

Add `.github/ISSUE_TEMPLATE/bug_report.yml`, `.github/ISSUE_TEMPLATE/feature_request.yml`, `.github/ISSUE_TEMPLATE/config.yml`, `.github/workflows/scorecard.yml`, `.codecov.yml`. Add a test case `fails_on_missing_issue_form`. Run `bash scripts/docs-check_test.sh`: five `ok` lines. Run `scripts/docs-check.sh`: lists the missing files.

- [ ] **Step 2: Write the templates**

- PR template: jellyfish's, with the checklist items `make pre-ci` passes, `CHANGELOG.md` updated under `[Unreleased]` (or not user-visible), and "If this is a release: `[Unreleased]` renamed to `[X.Y.Z] - YYYY-MM-DD` with its link, and a new empty `[Unreleased]` added above it".
- `bug_report.yml`: go-blinkstick's shape with fields: `what` (textarea, required), `repro` (textarea "How to reproduce", the commands run, `render: shell`, required), `os` (input, placeholder "macOS 26.1, Omarchy, Ubuntu 26.04", required), `engine` (dropdown "Docker engine": Docker Desktop, OrbStack, Colima, native Linux Docker, Other; required), `version` (textarea "Output of caddy_shack version", `render: text`, required), `doctor` (textarea "Output of caddy_shack doctor", `render: text`, not required), `extra`.
- `feature_request.yml`: go-blinkstick's with the checkbox replaced by "I checked the README and this is not already supported."
- `config.yml`: go-blinkstick's with the caddy_shack advisories URL.
- `CODEOWNERS`: `* @bawdo`.
- `scorecard.yml`: byte-for-byte copy of go-blinkstick's.

- [ ] **Step 3: Set the Codecov floor and ratchet**

Replace `.codecov.yml` with jellyfish's structure: project `default` target `80%`, threshold `1%`, `informational: false`; `no-regression` target `auto`, threshold `1%`, `informational: false`; patch target `70%`, threshold `0.5%`, `informational: true`; rest unchanged. Add a comment above `default` giving the measured figure and date.

- [ ] **Step 4: Wire docs-check into pre-ci and CI**

Per the Files list. Validate: `python3 -c "import yaml,sys; [yaml.safe_load(open(f)) for f in sys.argv[1:]]" .github/ISSUE_TEMPLATE/*.yml .github/workflows/*.yml .codecov.yml`.

- [ ] **Step 5: Verify and commit**

Run: `scripts/docs-check.sh`
Expected: `docs are consistent`.

Run (sandbox off): `make pre-ci`
Expected: pass.

```bash
git add .github .codecov.yml scripts/docs-check.sh scripts/docs-check_test.sh scripts/pre-ci-check.sh
git commit -S -m "Added issue forms, PR template, Scorecard and Codecov floor"
```

---

### Task 6: README install, badges and release section

**Files:**
- Modify: `README.md` (lines 1-31 for the title, badges and Install; the Development section at the end)

**Interfaces:**
- Consumes: archive names and verification commands (Task 3), `CONTRIBUTING.md` (Task 4).
- Reference: `ref/jellyfish/README.md` lines 1-40 for the badge and install voice; `ref/go-blinkstick/README.md` for the Scorecard badge.

- [ ] **Step 1: Badges under the title**

CI (`actions/workflows/ci.yml/badge.svg`), Codecov (`https://codecov.io/gh/bawdo/caddy_shack/graph/badge.svg`, no token parameter; the maintainer adds one if the graph needs it), Go Reference (`https://pkg.go.dev/badge/github.com/bawdo/caddy_shack.svg`), Licence (`https://img.shields.io/badge/License-MIT-yellow.svg` linking to `LICENSE`), OpenSSF Scorecard (`https://api.scorecard.dev/projects/github.com/bawdo/caddy_shack/badge` linking to `https://scorecard.dev/viewer/?uri=github.com/bawdo/caddy_shack`).

- [ ] **Step 2: Rewrite the Install section**

Two subsections. "From a release": the four archive names, download the archive and `checksums.txt` from the Releases page, then the four commands (`gh attestation verify caddy_shack_<version>_<os>_<arch>.tar.gz --repo bawdo/caddy_shack`, `sha256sum -c checksums.txt --ignore-missing` with a note that macOS uses `shasum -a 256 -c`, `tar -xzf`, `install -m 0755 .../caddy_shack /usr/local/bin/caddy_shack`); the attestation line needs `gh`, skip it without, keep the checksum. "From source": `go install github.com/bawdo/caddy_shack@latest` (Go 1.25 or newer) and `make install` from a clone.

- [ ] **Step 3: Add a Releases subsection to Development**

Three sentences: releases are cut from a signed tag by `make release`, notes come from `CHANGELOG.md`, see `CONTRIBUTING.md` for the ritual. Mention `make changelog`.

- [ ] **Step 4: Verify and commit**

Run: `scripts/docs-check.sh && grep -c 'badge' README.md`
Expected: `docs are consistent`, badge count 5.

```bash
git add README.md
git commit -S -m "Added install, badge and release sections to README"
```

---

### Task 7: History scan script

**Files:**
- Create: `scripts/history-scan.sh`
- Create: `scripts/history-scan_test.sh`
- Create: `.history-scan-allow`
- Modify: `CONTRIBUTING.md` (one line under the release section: `scripts/history-scan.sh` is run before anything is pushed to a public remote)

**Interfaces:**
- Produces: `scripts/history-scan.sh [--allow FILE]` scans every ref in `git for-each-ref` (local branches, remote-tracking branches, tags) and exits 0 printing `history-scan: clean (N commits, M blobs)` or exits 1 printing one `history-scan: <commit-or-blob>: <pattern name>: <match>` line per hit on stderr. Patterns, case-insensitive:
  - commit messages and tag messages: `co-authored-by:` naming any bot or model, `noreply@anthropic.com`, `claude-session:`, `generated with`, `anthropic`, `claude`;
  - blob contents (every blob reachable from every ref, each scanned once, binaries skipped with `grep -I`): `noreply@anthropic.com`, `claude-session:`, `co-authored-by: claude`, `/Users/bawdo/`, `/home/bawdo/`, `BEGIN (RSA|EC|OPENSSH|PGP) PRIVATE KEY`, `ghp_[A-Za-z0-9]{36}`, `github_pat_[A-Za-z0-9_]{22,}`, `AKIA[0-9A-Z]{16}`, `xox[baprs]-[A-Za-z0-9-]+`, `-----BEGIN CERTIFICATE-----` outside `testdata` directories;
  - committer and author emails: anything other than the addresses listed in the allow file's `email:` lines.
  The allow file (default `.history-scan-allow`, optional) has lines `email:<address>` and `blob:<path>:<pattern name>` for accepted hits. The repository ships one listing `email:keith@bawdo.com` and `email:keith@arrix.com.au`.

- [ ] **Step 1: Write `scripts/history-scan_test.sh`**

Each case builds a temp repo and plants one thing:

```
clean_repo_passes                   one commit by keith@bawdo.com -> exit 0, stdout starts "history-scan: clean"
flags_attribution_trailer           commit body "Co-Authored-By: Claude <noreply@anthropic.com>" -> exit 1, stderr contains "attribution trailer"
flags_session_url                   body "Claude-Session: https://..." -> exit 1, stderr contains "session url"
flags_home_path_in_blob             file containing "/Users/bawdo/x" -> exit 1, stderr contains "home path"
flags_private_key_in_blob           file containing "-----BEGIN OPENSSH PRIVATE KEY-----" -> exit 1, stderr contains "private key"
flags_token_in_deleted_file         file with "ghp_" + 36 chars committed then deleted in a later commit -> exit 1 (history, not tree)
flags_unlisted_author_email         commit by someone@example.com -> exit 1, stderr contains "unlisted email"
flags_hit_on_unmerged_branch        planted on a branch not merged to main -> exit 1
allow_file_accepts_named_blob       allow line "blob:notes.md:home path" -> exit 0
flags_tag_message                   annotated tag with "Co-Authored-By: Claude" -> exit 1
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash scripts/history-scan_test.sh`
Expected: fails because the script does not exist.

- [ ] **Step 3: Write `scripts/history-scan.sh` and `.history-scan-allow`**

Blobs: `git rev-list --all --objects | awk 'NF==2'` gives blob and tree ids with paths; filter to blobs with `git cat-file --batch-check`; scan each with `git cat-file -p <id> | grep -nIiE <pattern>`; report as `<id>:<path>`. Messages: `git log --all --format='%H%x00%B%x00%an <%ae>%x00%cn <%ce>' -z`. Tags: `git for-each-ref refs/tags --format='%(objectname) %(contents)'`.

- [ ] **Step 4: Run the test, shellcheck, then the real scan**

Run: `bash scripts/history-scan_test.sh && shellcheck scripts/history-scan.sh scripts/history-scan_test.sh`
Expected: ten `ok` lines, shellcheck silent.

Run: `scripts/history-scan.sh`
Expected: `history-scan: clean (...)`. Note: on 2026-10-27 `git log --all` had no attribution or home-path hits and only one author; the single tree mention of the home directory in `docs/superpowers/plans/2026-05-27-mvp-vertical-slice.md` has no trailing slash and does not match. If the scan is not clean, stop and report every line; do not edit history.

- [ ] **Step 5: Commit**

```bash
git add scripts/history-scan.sh scripts/history-scan_test.sh .history-scan-allow CONTRIBUTING.md
git commit -S -m "Added the history scan run before going public"
```

---

### Task 8: Repository settings script

**Files:**
- Create: `scripts/repo-settings.sh`
- Create: `scripts/repo-settings_test.sh`
- Create: `docs/superpowers/plans/2026-10-27-go-public-runbook.md`

**Interfaces:**
- Produces: `scripts/repo-settings.sh <phase>` with phases `pre-public`, `public`, `post-public`, `rulesets`, `show`. Env `REPO` (default `bawdo/caddy_shack`). Every call is idempotent (PATCH or PUT with the full desired state; rulesets are looked up by name and updated in place when present). The script prints each `gh api` call's method and path before running it, and with `DRY_RUN=1` prints without running. Phases:
  - `pre-public`: `PATCH /repos/{R}` with `description` "A CLI that runs a local Caddy in Docker and gives your projects stable HTTPS URLs under .test", `homepage` `https://github.com/bawdo/caddy_shack`, `delete_branch_on_merge: true`, `has_wiki: false`, `has_projects: false`, `has_discussions: false`, `allow_update_branch: true`; `PUT /repos/{R}/topics` with names `caddy`, `go`, `golang`, `cli`, `docker`, `https`, `local-development`, `reverse-proxy`, `tls`; prints a warning (not a failure) if `gh secret list` does not list `CODECOV_TOKEN`.
  - `public`: refuses unless `GO_PUBLIC=yes` is set, then `PATCH /repos/{R}` `visibility: public`.
  - `post-public`: `PUT /repos/{R}/private-vulnerability-reporting`; `PATCH /repos/{R}` with `security_and_analysis: { secret_scanning: enabled, secret_scanning_push_protection: enabled }`; `PUT /repos/{R}/vulnerability-alerts`; `PUT /repos/{R}/automated-security-fixes`.
  - `rulesets`: branch ruleset `main` targeting `refs/heads/main`, enforcement `active`, bypass actor `RepositoryRole` id 5 (admin) `always`, rules `deletion`, `non_fast_forward`, `pull_request` (required approving review count 0, dismiss stale reviews false, require last push approval false, required review thread resolution false), `required_status_checks` with `strict_required_status_checks_policy: true` and the five contexts read from the latest successful CI run on `main` (`gh run list --workflow ci.yml --branch main --status success --limit 1 --json databaseId`, then `gh api repos/{R}/actions/runs/{id}/jobs --jq '.jobs[].name'`); the script fails if that list does not have exactly five names. Tag ruleset `release-tags` targeting `refs/tags/v*`, enforcement `active`, no bypass actors, rules `deletion`, `non_fast_forward`, `update`.
  - `show`: prints visibility, the repo settings above, topics, security flags and ruleset names.

- [ ] **Step 1: Write `scripts/repo-settings_test.sh`**

A fake `gh` on `PATH` that logs every invocation to a file and returns canned JSON. Cases:

```
pre_public_patches_settings_and_topics   log contains "api -X PATCH repos/bawdo/caddy_shack" and "api -X PUT repos/bawdo/caddy_shack/topics"; the PATCH body (the fake writes --input to a file) has delete_branch_on_merge true and has_projects false
public_refuses_without_go_public         exit 1, stderr "repo-settings: set GO_PUBLIC=yes to make the repository public", log has no PATCH
public_patches_visibility                GO_PUBLIC=yes -> PATCH body visibility "public"
rulesets_needs_five_contexts             fake jobs list returns four names -> exit 1, stderr "repo-settings: expected 5 status check contexts, found 4"
rulesets_creates_when_absent             fake list returns [] -> log has "POST repos/.../rulesets" twice
rulesets_updates_when_present            fake list returns both names with ids -> log has "PUT repos/.../rulesets/<id>" twice, no POST
dry_run_runs_nothing                     DRY_RUN=1 pre-public -> log empty, stdout lists the calls
```

- [ ] **Step 2: Run the test to verify it fails, write the script, run again, shellcheck**

Run: `bash scripts/repo-settings_test.sh` (fails), write the script, rerun: seven `ok` lines; `shellcheck scripts/repo-settings.sh scripts/repo-settings_test.sh` silent.

- [ ] **Step 3: Dry-run against the real repository name**

Run (sandbox off): `DRY_RUN=1 scripts/repo-settings.sh pre-public && DRY_RUN=1 scripts/repo-settings.sh rulesets`
Expected: the calls print; nothing is sent. (The `rulesets` dry run still reads the job names from the latest CI run; if there is no successful run on `main` yet it says so and exits 1, which is acceptable before merge.)

- [ ] **Step 4: Write the runbook**

`docs/superpowers/plans/2026-10-27-go-public-runbook.md`: the ordered commands for Task 11 (merge, scan, pre-public, go-ahead, public, post-public, rulesets, release) with what each prints when it worked, so the maintainer can re-run any phase alone.

- [ ] **Step 5: Commit**

```bash
git add scripts/repo-settings.sh scripts/repo-settings_test.sh docs/superpowers/plans/2026-10-27-go-public-runbook.md
git commit -S -m "Added the idempotent repository settings script"
```

---

### Task 9: Branch review and closeout

Run by the orchestrator on `feature/releases` after Tasks 1 to 8 are merged.

- [ ] **Step 1: Whole-branch checks**

Run (sandbox off): `make pre-ci && bash scripts/changelog_test.sh && bash scripts/release_test.sh && bash scripts/release-workflow_test.sh && bash scripts/docs-check_test.sh && bash scripts/history-scan_test.sh && bash scripts/repo-settings_test.sh && scripts/history-scan.sh && scripts/docs-check.sh && shellcheck scripts/*.sh && grep -rnP '[\x{2013}\x{2014}]' --exclude-dir=.git . ; git worktree list; git branch --list 'task/*'`
Expected: everything passes, no dashes, only the `releases` worktree remains, no task branches remain.

- [ ] **Step 2: Three reviewers**

Dispatch three reviewer subagents on `git diff kaizen/lean-and-secure...feature/releases`: security (workflow permissions, token handling, injection through tag names and inputs, what the scan misses), quality (script correctness, test coverage of guards, idempotency), compliance (commit messages and signatures via `git log --format='%h %G? %s' kaizen/lean-and-secure..feature/releases`, no attribution, Australian English, no dashes, subjects at most 60 characters). Fix what they find as further signed commits, re-run Step 1.

- [ ] **Step 3: Close the R&D record**

Use `wombat-rnd:rnd-close` on `feature/releases` to append the closeout to the design doc: fit, proven, disproven and divergence. `v0.1.0` is not yet cut, so the "Desired outcome" bullet about it is recorded as pending Task 11.

---

### Task 10: Hand-off for merge

Run by the orchestrator. Nothing is pushed by agents.

- [ ] **Step 1: Print the commands for the maintainer**

```bash
git push origin feature/releases
gh pr create --base main --head feature/releases --title "Added tag-driven releases and public repo setup" --body-file docs/superpowers/plans/2026-10-27-go-public-runbook.md
```

The kaizen PR merges first, then this one (this branch contains the kaizen head, so merging it alone also works). After merge, CI on `main` must be green before Task 11.

---

### Task 11: Go public and cut v0.1.0

Run by the orchestrator from a clean checkout of `main` after the maintainer has merged and CI on `main` has passed. Each step needs the sandbox off. Stop and report at the first failure.

- [ ] **Step 1: Scan**

Run: `git fetch --all --tags && scripts/history-scan.sh`
Expected: `history-scan: clean`. Any hit stops the work; the maintainer decides what to do.

- [ ] **Step 2: Pre-public settings**

Run: `scripts/repo-settings.sh pre-public && scripts/repo-settings.sh show`
Expected: description, topics and flags as specified; the warning about `CODECOV_TOKEN` if the secret is missing (on 2026-10-27 it was missing; the maintainer adds it with `gh secret set CODECOV_TOKEN` or Codecov's OIDC path, then re-runs CI).

- [ ] **Step 3: Explicit go-ahead**

Ask the maintainer, with the scan output and the `show` output in front of them, whether to make the repository public. Proceed only on an explicit yes in this session.

- [ ] **Step 4: Public, then post-public, then rulesets**

Run: `GO_PUBLIC=yes scripts/repo-settings.sh public && scripts/repo-settings.sh post-public && scripts/repo-settings.sh rulesets && scripts/repo-settings.sh show`
Expected: visibility public, security features on, two rulesets listed. The Scorecard workflow starts on its next scheduled run or a push to `main`.

- [ ] **Step 5: Cut v0.1.0**

Run: `git checkout main && git pull --ff-only && make release RELEASE_VERSION=0.1.0`
Expected: the guards pass, the tag is created and signed, the last line is `git push origin v0.1.0`. Give that command to the maintainer; they push.

- [ ] **Step 6: Acceptance**

After the maintainer pushes the tag: `gh run watch` on the Release run, then
`gh release view v0.1.0 --json body --jq .body | diff - <(scripts/release-notes.sh 0.1.0)` (empty diff), download one archive and run `gh attestation verify caddy_shack_0.1.0_<os>_<arch>.tar.gz --repo bawdo/caddy_shack` and `sha256sum -c checksums.txt --ignore-missing`, and `go install github.com/bawdo/caddy_shack@v0.1.0 && caddy_shack version` reports `v0.1.0`. Record the results in the design doc's closeout and update the memory note `releases-resume-point` to "done".

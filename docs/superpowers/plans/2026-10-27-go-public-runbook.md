# Go public and cut v0.1.0

## What this branch adds

- **Tag-driven releases.** `CHANGELOG.md` (Keep a Changelog) and
  `scripts/changelog.sh` to check it. `make release RELEASE_VERSION=X.Y.Z`
  makes a signed, annotated tag after its guards pass and prints the push
  command; it never pushes. Pushing a `vX.Y.Z` tag runs
  `.github/workflows/release.yml`: CI first, then checks that the tag is on
  `main`, signed and matches the changelog, builds linux and darwin archives
  for amd64 and arm64, smoke tests them, attests their build provenance and
  publishes the release with notes taken from the changelog
  (`scripts/release-notes.sh`).
- **Public repository files.** Licence, `CONTRIBUTING.md`, `SECURITY.md`,
  `CODE_OF_CONDUCT.md`, issue forms, a pull request template, `CODEOWNERS`,
  Codecov and OpenSSF Scorecard, and README install, badge and release
  sections. `scripts/docs-check.sh` keeps them consistent.
- **Going public safely.** `scripts/history-scan.sh` scans every ref's
  history for agent attribution, secrets, home paths and unknown emails.
  `scripts/repo-settings.sh` applies the repository settings in phases
  (`pre-public`, `public`, `post-public`, `rulesets`, `show`).

Every script has a `scripts/*_test.sh` suite run by `make test-scripts`.

## How to use this runbook

Run the phases in order from a clean, up to date checkout of `main`, after
this pull request is merged. Each phase can be re-run on its own: every
settings call sends the full desired state, and rulesets are found by name
and replaced in place. Stop at the first phase that does not print what is
shown here.

Put `DRY_RUN=1` in front of any `scripts/repo-settings.sh` phase to print
each call with its body and send nothing. Each real call prints its method
and path (for example `PATCH repos/bawdo/caddy_shack`) before it is sent.
`REPO=owner/name` points the script at another repository.

The maintainer pushes. Agents never push.

## 1. Merge

Merge the kaizen pull request first, then this one (this branch contains the
kaizen head, so merging it alone also works). Wait for CI on `main`:

```bash
gh run list --workflow ci.yml --branch main --limit 1
```

Worked when: the newest run shows `completed` and `success`. The `rulesets`
phase reads its required checks from this run, so it must be a run of the
new `ci.yml`.

Then update your checkout:

```bash
git checkout main && git pull --ff-only
```

## 2. Scan the history

Fetch the pull request heads too, so they are in the scanned history:

```bash
git fetch origin '+refs/pull/*/head:refs/remotes/origin/pr/*'
git fetch --all --tags && scripts/history-scan.sh
```

Existing Actions run logs also become public with the repository; delete
any you don't want seen from the Actions tab before going on.

Worked when: it prints `history-scan: clean (N commits, M blobs)` and exits
0. Any `history-scan: ...` line on stderr stops the run; decide what to do
about each hit before going on (accepted hits go in `.history-scan-allow`).

## 3. Pre-public settings

```bash
scripts/repo-settings.sh pre-public && scripts/repo-settings.sh show
```

Worked when: `pre-public` prints

```text
PATCH repos/bawdo/caddy_shack
PUT repos/bawdo/caddy_shack/topics
```

and `show` prints these lines among others:

```text
visibility: private
description: A CLI that runs a local Caddy in Docker and gives your projects stable HTTPS URLs under .test
homepage: https://github.com/bawdo/caddy_shack
delete_branch_on_merge: true
has_wiki: false
has_projects: false
has_discussions: false
allow_update_branch: true
topics: caddy go golang cli docker https local-development reverse-proxy tls
```

(GitHub may list the topics in another order.)

If it also prints `repo-settings: warning: no CODECOV_TOKEN secret`, add
the secret (or set up Codecov's OIDC upload instead) and re-run CI on
`main`:

```bash
gh secret set CODECOV_TOKEN --repo bawdo/caddy_shack
gh workflow run ci.yml --ref main
```

## 4. Go-ahead

With the scan output and the `show` output in front of you, decide whether
to make the repository public. Once it is public, anyone can clone or fork
the history, so this is the last point to stop. Go on only on an explicit
yes.

## 5. Public

```bash
GO_PUBLIC=yes scripts/repo-settings.sh public
```

Worked when: it prints `PATCH repos/bawdo/caddy_shack` and
`scripts/repo-settings.sh show` prints `visibility: public`. Without
`GO_PUBLIC=yes` it refuses with
`repo-settings: set GO_PUBLIC=yes to make the repository public` and sends
nothing.

## 6. Post-public security settings

```bash
scripts/repo-settings.sh post-public && scripts/repo-settings.sh show
```

Worked when: `post-public` prints

```text
PUT repos/bawdo/caddy_shack/private-vulnerability-reporting
PATCH repos/bawdo/caddy_shack
PUT repos/bawdo/caddy_shack/vulnerability-alerts
PUT repos/bawdo/caddy_shack/automated-security-fixes
```

and `show` prints

```text
secret_scanning: enabled
secret_scanning_push_protection: enabled
private_vulnerability_reporting: enabled
vulnerability_alerts: enabled
automated_security_fixes: enabled
```

## 7. Rulesets

```bash
scripts/repo-settings.sh rulesets && scripts/repo-settings.sh show
```

Worked when: `rulesets` prints `POST repos/bawdo/caddy_shack/rulesets`
twice the first time (`PUT repos/bawdo/caddy_shack/rulesets/<id>` twice on
a re-run) and `show` prints `rulesets: main release-tags`.

- `main` protects `refs/heads/main`: no deletion, no force push, changes
  through a pull request (no approving review needed), and the five CI
  checks (`Static checks`, `Test (ubuntu-latest)`, `Test (macos-latest)`,
  `Integration tests`, `Build and smoke`) must pass on an up to date
  branch. Repository admins can bypass it.
- `release-tags` protects `refs/tags/v*`: no deletion, no force push, no
  update. Nobody bypasses it.

If it fails with `repo-settings: expected 5 status check contexts, found N`,
the newest successful CI run on `main` is not from the new `ci.yml`: re-run
CI on `main` (step 1) and try again. Nothing is written when it fails.

The Scorecard workflow starts on its next scheduled run or push to `main`.

## 8. Release v0.1.0

```bash
git checkout main && git pull --ff-only && make release RELEASE_VERSION=0.1.0
```

Worked when: the guards pass, the signed tag is made, and the last two
lines are

```text
tagged v0.1.0
git push origin v0.1.0
```

Run that push command yourself; it starts the Release workflow. Watch it:

```bash
gh run watch
```

Worked when: the run finishes green and
`gh release view v0.1.0` shows the published release with four archives
and `checksums.txt`. Check the notes match the changelog (empty output):

```bash
gh release view v0.1.0 --json body --jq .body | diff - <(GITHUB_REPOSITORY=bawdo/caddy_shack scripts/release-notes.sh 0.1.0)
```

If the publish failed part way, fix the cause and run the workflow again
for the existing tag (a leftover draft is replaced; a published release is
never changed):

```bash
gh workflow run release.yml --ref v0.1.0 -f tag=v0.1.0
```

Then check a download, for example on an Apple silicon Mac:

```bash
gh release download v0.1.0 --pattern 'caddy_shack_0.1.0_darwin_arm64.tar.gz' --pattern checksums.txt
gh attestation verify caddy_shack_0.1.0_darwin_arm64.tar.gz --repo bawdo/caddy_shack
shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf caddy_shack_0.1.0_darwin_arm64.tar.gz && ./caddy_shack_0.1.0_darwin_arm64/caddy_shack version
go install github.com/bawdo/caddy_shack@v0.1.0 && caddy_shack version
```

Worked when: the attestation verifies, the checksum line ends in `OK`, the
unpacked binary's first line is `caddy_shack v0.1.0`, and the `go install`
binary's `caddy_shack version` reports `v0.1.0`. The `go install` build
reports the module version from Go's build info, with no commit line.

Proven by tests, not live: once the `release-tags` ruleset exists, two of
the release workflow's refusals can't be exercised here.

- A tag whose version has no changelog section is refused: the tag loop in
  `scripts/changelog.sh check` and its cases in `scripts/changelog_test.sh`
  (`test_check_rejects_tag_without_section`).
- A tag not on `main` is refused: the `Check the tag is on main` step in
  `.github/workflows/release.yml`, which has no test.

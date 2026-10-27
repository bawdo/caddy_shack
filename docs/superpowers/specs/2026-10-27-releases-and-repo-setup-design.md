# Tag-driven releases and public repository setup

caddy_shack is about to become a public free software project. This design
covers three things that land together: a changelog that is the single
source of release notes, releases that are cut by automation from a signed
tag the maintainer pushes, and the documents and repository settings a
visitor expects from a well-run project. It is a one-person project, so
nothing here adds review gates or approval ceremony.

## Hypothesis

We believe that for a solo-maintained Go CLI the right release trigger is a
tag the maintainer pushes, not a merge, and that `CHANGELOG.md` in Keep a
Changelog form can be the only place release notes are written. Evidence:
the maintainer's own jellyfish and gosbee repositories already run this way
(gosbee on tag push, jellyfish on merge), both extract notes from the
changelog with a shell script, and neither has needed a release tool. If
true, a release costs one changelog edit and one `make release` invocation,
the GitHub Release page carries exactly the changelog section, and
`go install github.com/bawdo/caddy_shack@vX.Y.Z` resolves the same tag.

What would show we are wrong: a release that needs hand-written notes
because the changelog section does not read well on the Release page; a
tag that releases without CI having passed on that commit; or a maintainer
who stops updating `[Unreleased]` because the ritual is too heavy, which
would show up as empty release notes.

## Method

Build the changelog script, the release workflow, the `make release`
target and the project documents on one branch, and cut `v0.1.0` from it
as the proof. Measure: the Release page notes equal the changelog section
byte for byte; `gh attestation verify` passes on every archive;
`caddy_shack version` from each archive reports the tag; a tag pushed from
a commit not on `main` is refused; a tag whose version has no changelog
section is refused; CI (including the Docker integration suite) runs
before any binary is built.

Evidence against: any of those checks needing a manual step, or the
changelog script rejecting a section that a human reads as well formed.

## Desired outcome

- Pushing a signed `vX.Y.Z` tag on `main` produces, with no further
  action, a published GitHub Release named `vX.Y.Z` with that version's
  changelog section as its notes, four archives (darwin and linux, amd64
  and arm64), `checksums.txt`, and a build provenance attestation.
- The repository is public with MIT licence, changelog, contributing
  guide, security policy, code of conduct, issue forms, PR template,
  CODEOWNERS, Dependabot, Codecov config, Scorecard, description, topics,
  private vulnerability reporting, secret scanning with push protection,
  Dependabot alerts, delete-branch-on-merge, wiki off, and a ruleset on
  `main` that requires CI to pass and nothing else.
- `v0.1.0` exists and was produced by the workflow, not by hand.

## Goals, non-goals, anti-goals

Goals: one changelog edit and one command per release; releases
reproducible from the tag; everything a first-time visitor needs present
before the repository goes public.

Non-goals: Homebrew tap, Linux packages, container images, signing with
cosign (provenance attestation covers verification), release automation
that decides version numbers.

Anti-goals: any process step that assumes a second person (required
reviewers, approval environments); conventional-commit prefixes; release
notes written anywhere other than `CHANGELOG.md`.

## Constraints

- Commits and tags are GPG-signed. No agent attribution anywhere.
- Commit subjects past tense, 60 characters or fewer. Australian English.
  No em dashes or en dashes in any file.
- The reworked `ci.yml` on the kaizen branch (four jobs, Linux by default,
  macOS test leg) is the CI that must pass before a release. This branch
  is cut from that head.
- Repository is private on the free plan today; rulesets become available
  only after it is public, so the settings step is ordered last.
- `internal/version` already reads `Version`, `Commit`, `Tag` and `Dirty`
  from ldflags and falls back to `debug.ReadBuildInfo()`. The release build
  sets all four; `go install` users get the module version from build info.

## Approaches considered

1. **Changelog-driven release on tag push (chosen).** Same mechanism as
   gosbee's `release.yml` plus jellyfish's build, checksum, smoke test and
   attestation steps. No third-party release tool. The maintainer decides
   the version and when.
2. **Changelog-driven release on merge to `main`** (jellyfish). Rejected by
   the maintainer: they want to decide when a release happens, separately
   from merging.
3. **GoReleaser on tag push.** The most common Go idiom, with archives,
   Homebrew tap and generated notes. Rejected for now: generated notes from
   prose commits read as a flat list, it is one more tool to pin and
   configure, and the changelog-as-notes rule already gives better release
   pages. It can be slotted in later as the builder if a tap is wanted.
4. **release-please.** Rejected: requires conventional-commit prefixes,
   which contradicts the house commit style.

## Design

### CHANGELOG.md

Keep a Changelog 1.1.0 format, Semantic Versioning. Headings are exactly
`## [Unreleased]` and `## [X.Y.Z] - YYYY-MM-DD` (no `v` in the heading;
the tag carries it). Sections under each: Added, Changed, Deprecated,
Removed, Fixed, Security, used only when non-empty. Link references at the
bottom: `[Unreleased]: https://github.com/bawdo/caddy_shack/compare/vX.Y.Z...HEAD`
and `[X.Y.Z]: https://github.com/bawdo/caddy_shack/compare/vPREV...vX.Y.Z`
(the first release links to `releases/tag/v0.1.0`).

Seed content: `[0.1.0]` summarises the project as it ships (install,
register and deregister, list, status and sync, doctor with fixes,
container-mode upstreams, subdomains, native Linux relay, LAN exposure,
Let's Encrypt via manual DNS-01, upgrade), and `[Unreleased]` is empty.
The kaizen branch's changes are part of 0.1.0, not a separate entry,
because nothing was released before them.

### scripts/changelog.sh

One script, adapted from gosbee's, with three subcommands:

- `versions`: released versions newest first.
- `notes X.Y.Z [owner/repo]`: that section's body, with relative links
  rewritten to absolute `blob/vX.Y.Z/` links so they work on the Release
  page.
- `check`: every `## ` heading is `[Unreleased]` or a dated semver
  heading; no duplicate versions; every released version has a link
  reference and a non-empty body; the newest released version is not
  behind the highest `v*` tag.

CI runs `check` in the `static` job. `make release` and the workflow run
`notes`.

### Makefile

`make changelog` runs `scripts/changelog.sh check`. `make release
VERSION=X.Y.Z` refuses unless: the tree is clean, the current branch is
`main` and up to date with `origin/main`, `scripts/changelog.sh check`
passes, the newest changelog version equals `VERSION`, and no tag
`vVERSION` exists; then it runs `git tag -s vVERSION -m "vVERSION"` and
prints the push command (`git push origin vVERSION`). It never pushes.

### .github/workflows/release.yml

Trigger: `push` on `tags: ['v*']`, plus `workflow_dispatch` with a `tag`
input for re-running a failed publish. Top-level `permissions: contents:
read`; the publish job elevates to `contents: write`, `id-token: write`,
`attestations: write`. Concurrency group `release-${{ github.ref }}`,
never cancel.

Jobs:

1. `ci`: `uses: ./.github/workflows/ci.yml` with the Codecov secret passed
   through. `ci.yml` gains `workflow_call` (with the optional secret) and
   its concurrency group becomes the literal `ci-${{ github.ref }}` so the
   caller and callee cannot share a group.
2. `publish`, `needs: ci`, `ubuntu-latest`, `timeout-minutes: 20`:
   - checkout with `fetch-depth: 0` and `persist-credentials: false`;
   - refuse unless the tag's commit is an ancestor of `origin/main`;
   - refuse unless the tag is signed (`git tag -v`) by a key in the
     repository's allowed signers list, if GitHub exposes it; otherwise
     record in the step log that signature verification is done client
     side by `make release` and leave a comment in the workflow saying so
     (builder's call: implement the check only if it can be done without
     importing keys into the runner);
   - `scripts/changelog.sh check` and `notes` for the tag's version into
     `$RUNNER_TEMP/notes.md`, appending a "Verify a downloaded archive"
     block and a Full Changelog compare link to the previous tag;
   - setup-go `1.25.x`, `cache: true`;
   - build four archives: for each of darwin and linux times amd64 and
     arm64, `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X ...Version=vX.Y.Z -X ...Commit=$GITHUB_SHA -X ...Tag=vX.Y.Z -X ...Dirty=false"` into
     `dist/caddy_shack_X.Y.Z_<os>_<arch>/caddy_shack`, copy `LICENSE`,
     `README.md` and `CHANGELOG.md` alongside, tar.gz the directory,
     remove it; then `sha256sum *.tar.gz > checksums.txt`;
   - smoke test: extract the linux amd64 archive and assert
     `caddy_shack version` prints `caddy_shack vX.Y.Z` on its first line;
   - `actions/attest-build-provenance` with `subject-checksums:
     dist/checksums.txt`;
   - `gh release create vX.Y.Z --draft --title vX.Y.Z --notes-file ...
     dist/*.tar.gz dist/checksums.txt`, then `gh release edit vX.Y.Z
     --draft=false --latest`. A pre-existing draft of the same name is
     deleted first; a pre-existing published release stops the job with
     an error.

Every action is SHA-pinned with a `# vX.Y.Z` comment, matching the other
repositories. `ci.yml`'s actions are pinned the same way on this branch.

### Documents

All in the repository root unless noted, written in the voice of the
jellyfish and gosbee originals and adapted to caddy_shack:

- `LICENSE`: MIT, copyright 2026 Keith Bawden.
- `CONTRIBUTING.md`: one person, light process; open an issue for
  anything beyond a small fix; `make pre-ci`; add a line under
  `[Unreleased]`; signed past-tense commits; how releases happen (the
  ritual above).
- `SECURITY.md`: what counts as a security bug here (the admin API trust
  boundary, certificate and key handling, the LAN listener), supported
  versions (newest release only), private reporting via
  `security/advisories/new`, acknowledgement within seven days, how to
  verify a release archive with `gh attestation verify` and
  `sha256sum -c`.
- `CODE_OF_CONDUCT.md`: Contributor Covenant 2.1, enforcement contact the
  maintainer's GitHub profile.
- `.github/PULL_REQUEST_TEMPLATE.md`: what changed, checklist (`make
  pre-ci`, changelog line, release ritual if this is a release).
- `.github/ISSUE_TEMPLATE/bug_report.yml`, `feature_request.yml`,
  `config.yml` (blank issues off, security contact link). The bug form
  asks for the OS, the Docker engine (Desktop, OrbStack, Colima, native
  Linux), `caddy_shack version` and `caddy_shack doctor` output.
- `.github/CODEOWNERS`: `* @bawdo`.
- `.github/dependabot.yml`: aligned with the other repositories (Monday,
  Australia/Sydney, grouped, "Bumped" prefix, `dependencies` label).
- `.codecov.yml`: floor and ratchet as in jellyfish, with the floor set
  from the current measured coverage rounded down to the nearest 5
  percent (builder measures it).
- `.github/workflows/scorecard.yml`: as in go-blinkstick.
- `README.md`: an install section listing `go install` and the release
  archives with the verification commands; badges for CI, Codecov,
  Go Reference, licence and Scorecard; a Releases subsection pointing at
  `CONTRIBUTING.md`.

### Repository settings (last, by `gh`, each one idempotent)

Description and homepage; topics `caddy`, `go`, `golang`, `cli`, `docker`,
`https`, `local-development`, `reverse-proxy`, `tls`; delete branch on
merge; wiki off; projects off; discussions off; then make public; then
enable private vulnerability reporting, secret scanning, push protection,
Dependabot alerts and security updates; then a ruleset on `main`: require
a pull request with zero required approvals, require the CI status checks
(the four job names), block force pushes, block deletion, allow the
repository owner to bypass. Tag ruleset on `v*`: block deletion and
updates. These are run from the plan as `gh api` calls with their exact
payloads, and the plan records each one so it can be re-run.

## Left to the builder

- Exact wording of the documents, within the voice described.
- Whether the tag signature can be verified in the workflow without
  importing keys; the fallback is stated above.
- The coverage floor number.
- The badge markdown.

## Order of work

1. Changelog, script, Makefile target, CI changelog check, SHA pinning.
2. Release workflow and `workflow_call` in `ci.yml`.
3. Documents, Dependabot alignment, Codecov config, Scorecard.
4. README install and release sections.
5. Repository settings and going public.
6. Merge, then cut `v0.1.0` with `make release` as the acceptance test.

## Open decisions at the time of writing

Two questions from the builder check are the maintainer's to answer
before the plan is written:

1. The gate before the repository goes public: an agent-run history scan
   (attribution trailers, secrets, absolute home paths) that stops on any
   hit, followed by an explicit go-ahead for the public switch; or a
   runbook the maintainer runs by hand.
2. Required status checks on `main`: all five contexts (four jobs, with
   the test matrix contributing two), or the Linux leg only.

Builder's calls already folded in: reference files are fetched from the
maintainer's GitHub repositories (gosbee, jellyfish, go-blinkstick), not
local checkouts; `ci.yml` drops its own `v*` tag trigger; the `static`
job checks out with full history for the changelog tag check; a failed
publish is re-run with `workflow_dispatch` and a defect gets a new patch
version; the tag ruleset precedes `v0.1.0`; the `make release` variable
avoids the Makefile's existing `VERSION`; `CODECOV_TOKEN` presence is
checked during the settings step.

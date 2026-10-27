# Tag-driven releases and public repository setup, closeout

Statement of fit for the branch opened in
`2026-10-27-releases-and-repo-setup-design.md`. The opening statement is
left as written; where it was wrong, this says so. Written on 2026-10-27
before the branch was merged, so the live acceptance run (cutting
`v0.1.0`) is still owed and is listed under Divergence.

## Fit

The hypothesis held on everything the branch could test short of the live
run. A release is one changelog edit and `make release RELEASE_VERSION=X.Y.Z`
followed by the push command it prints; `CHANGELOG.md` is the only place
notes are written and the workflow builds the Release page from it. What
the hypothesis did not anticipate was how much of the release path had to
leave the YAML and become tested shell: six scripts and 80 script test
cases now carry the logic the design sketched as workflow steps, because
nothing in a workflow file can be exercised locally. The branch closes with
26 signed commits across eight tasks, two task-level fix rounds, and one
final fix wave from three reviewers (security, quality, compliance).

## Proven

- The changelog is parseable and checkable as designed. `scripts/changelog.sh`
  refuses a `v`-prefixed heading, a missing date, a duplicate, a missing link
  reference, an empty body, a tag with no section and a newest version behind
  the highest tag, each pinned by a test. `make pre-ci` and the CI static job
  run it.
- The local guard refuses every bad state the design listed (dirty tree,
  wrong branch, behind origin, version mismatch, existing tag) before
  tagging, and never pushes; a bare-origin fixture proves the tag stays
  local.
- A tag's signature can be verified in the workflow without importing any
  key: GitHub's tags API reports `verification.verified` and `reason` for
  annotated tags, and `scripts/verify-tag.sh` also binds the remote tag
  object to the locally checked-out one, so a tag moved after checkout is
  refused.
- The publish path is draft-then-publish, replaces a stale draft, and stops
  on a published release of the same name, all exercised through a stateful
  fake `gh`.
- `build-release.sh` produces the four archives with the designed layout and
  `checksums.txt`, and the host archive's binary reports the tag; the real
  cross-compile ran in the test suite on every `make pre-ci`.
- `go install github.com/bawdo/caddy_shack@vX.Y.Z` can report the tag: the
  design asserted this and the code did not do it, so `internal/version`
  gained the build-info fallback with a test.
- The history scan is clean on all 234 commits and 745 blobs across every
  ref, after the maintainer-facing false positives (GitHub's merge
  committer, placeholder PEM blocks in tests, the design documents quoting
  the scan's own patterns) were handled by rule or allow entry, and after
  two confirmed evasions (an invalid UTF-8 byte before a token under a UTF-8
  locale; a PEM body split across quoted string lines) were closed with
  tests.
- All eight action pins resolve to their named tags upstream. The release
  job has no expression injection, least-privilege permissions, no
  persisted credentials, no restored Go cache, and the token scoped to the
  two steps that call `gh`.

## Disproven

- The design's constraint that `internal/version` "falls back to
  `debug.ReadBuildInfo()`" so that "`go install` users get the module
  version" was false for the version string: `Resolve()` only read commit
  and dirty state from build info. The acceptance step depended on it. Fixed
  in Task 8's fix round.
- The design's smoke test "extract the linux amd64 archive" was replaced
  with the host platform's archive so the test runs on the maintainer's
  Mac; on `ubuntu-latest` it is the same archive.
- The spec's concurrency group `release-${{ github.ref }}` does not
  serialise a tag push and a dispatch re-run of the same tag; the group is
  now keyed on the tag name.
- "No em dashes or en dashes in any file" was already untrue of five
  historical design documents and two comments in `pre-ci-check.sh`. The
  comments were rewritten; the historical documents are excluded from the
  docs check rather than edited.
- The spec's Method lists two refusals (a tag off `main`; a tag with no
  changelog section) as live measurements. Once the `v*` tag ruleset exists
  they cannot be exercised live without leaving a permanent bad tag, so they
  are proven by tests and code reading, and the runbook says so.

## Divergence

- The live acceptance run has not happened. Going public, the rulesets, and
  cutting `v0.1.0` wait on the maintainer merging this branch, CI passing on
  `main`, a clean scan on the merged history, and an explicit go-ahead. The
  runbook `docs/superpowers/plans/2026-10-27-go-public-runbook.md` carries
  the ordered commands and what each prints when it worked. Three checks are
  live-only: the Release notes equalling the changelog section byte for
  byte, `gh attestation verify` on every archive, and the three archives the
  smoke test does not run.
- `actions/checkout` with `ref: refs/tags/vX.Y.Z` is relied on to leave the
  annotated tag object in place; if it does not, `verify-tag.sh` fails
  closed and `v0.1.0` is blocked until the checkout step is adjusted.
- The `rulesets` phase needs a successful CI run on `main` from the new
  `ci.yml`; the dry run today correctly reports six contexts from the old
  workflow and refuses.
- `CODECOV_TOKEN` is not set; the Codecov upload is a non-failing step and
  the settings script warns rather than fails.
- Left for the maintainer, recorded in the plan's ledger as deferred minors:
  the main ruleset's admin bypass mode (`always`, as the spec chose),
  immutable releases and default workflow permissions not set by the
  settings script, the scan's narrow token coverage (secret scanning is the
  backstop once public), the `[Unreleased]` compare link not being
  validated, `ca` missing from the 0.1.0 changelog entry, and the strict
  status-check policy that the plan added beyond the spec's "requires CI to
  pass and nothing else".

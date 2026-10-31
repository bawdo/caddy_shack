# Project Improvements Design

Date: 2026-10-31. Source: a side by side comparison with `bawdo/jellyfish`,
which shares this repository's skeleton but has moved further on CI gating,
release isolation and fuzzing.

## Scope

Items 1 to 7, 9 and 10 of the comparison. Item 8 (raising the Codecov floor)
is out of scope.

1. **One aggregate CI check.** A `CI result` job in `ci.yml` needs every other
   job and fails unless each one succeeded, or was skipped on an event that is
   allowed to skip it. The `main` ruleset requires only `CI result`, pinned to
   the GitHub Actions app (`integration_id: 15368`).
2. **CodeQL gates merges.** The `main` ruleset gains a `code_scanning` rule
   (CodeQL, security alerts `high_or_higher`, alerts `errors`). The default
   setup moves to the `extended` query suite.
3. **No admin bypass on `main`.** `bypass_actors` becomes `[]`.
4. **Ruleset parity.** `required_review_thread_resolution: true`,
   `allowed_merge_methods: ["merge"]`, target `~DEFAULT_BRANCH`.
5. **Release isolation.** `release.yml` splits into a read-only build job
   (no Go cache, uploads archives and notes as artifacts) and a publish job
   that holds the write, id-token and attestation permissions, checks out
   only `scripts/publish-release.sh`, verifies checksums, attests and
   publishes.
6. **Provenance bundle and tighter verification.** The attestation's
   Sigstore bundle is attached as
   `caddy_shack_<version>_provenance.intoto.jsonl`. Release notes and
   SECURITY.md add `--deny-self-hosted-runners`, a `--bundle` variant and an
   offline `--custom-trusted-root` variant. The source ref stays
   `refs/tags/vX.Y.Z`, because releases here are built from signed tags.
7. **Fuzzing and a weekly schedule.** Seven fuzz targets over the parsers and
   validators that guard paths, DNS names, `state.json` and Docker output. A
   `make fuzz` target, a `pre-ci` fuzz check, and a `Fuzz` CI job that runs
   on the weekly schedule and by hand only. The schedule runs `vuln` and
   `fuzz` and nothing else, in its own concurrency group.
9. **Docs and templates.** A privacy warning on the bug report form, the Code
   of Conduct linked from CONTRIBUTING.md, CONTRIBUTING.md describing the new
   merge gates and `make fuzz`, `CLAUDE.local.md` ignored.
10. **Scorecard tidy.** `timeout-minutes: 10`; the private repository guard
    and the extra permissions it needed are removed.

## Decisions

- **Keep the signed-tag release model.** Jellyfish releases on merge to
  `main`; this repository releases from a signed tag the maintainer pushes,
  with `verify-tag.sh` checking GitHub's verdict. That is the stronger gate
  and stays. Only the job split and the bundle are taken from jellyfish.
- **The gate logic lives in `scripts/ci-result.sh`, not inline YAML,** so it
  is covered by `make test-scripts` like every other script here.
- **The publish job checks out one file.** Jellyfish's publish job has no
  checkout at all and inlines its shell. Here `publish-release.sh` already
  has tests, so the publish job does a sparse checkout of that one script at
  the tag. No Go code and no dependency runs while the write token is
  present.
- **A release re-run fuzzes briefly.** `release.yml`'s manual re-run calls CI
  with event `workflow_dispatch`, which runs the 15 second fuzz job. Re-runs
  are rare and a crash found then is worth knowing before publishing, so
  this is accepted rather than adding an input to tell the callers apart.
- **Rulesets are applied after the merge, through the script.** Settings
  never change by hand. `repo-settings.sh rulesets` refuses until a
  successful CI run on `main` has a `CI result` job.

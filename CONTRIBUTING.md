# Contributing

Thanks for taking an interest. caddy_shack is small and maintained by one
person, so the process is light.

## Before you start

Open an issue for anything bigger than a typo or a small fix, so the
approach can be agreed before you spend time on it. Security problems go
through [SECURITY.md](SECURITY.md), not the issue tracker. Please follow the
[Code of Conduct](CODE_OF_CONDUCT.md).

## Making a change

1. Fork and branch from `main`.
2. Add tests for any new functionality or bug fix in the same pull request:
   unit tests alongside the code, and an integration test under
   `test/integration` when the change touches Docker or Caddy. Codecov fails
   a pull request that takes project coverage below 85% or more than 1%
   below its base.
3. Run `make pre-ci` before pushing. It mirrors the GitHub Actions checks
   (format, tests with the race detector, lint, coverage, build, a CLI smoke
   test, the changelog check, `govulncheck` and a short fuzz run of 5s per
   target). If you touch code that parses untrusted input or `state.json`,
   run `make fuzz` for longer (15s each by default, or set `FUZZTIME`).
4. Add a line under `[Unreleased]` in `CHANGELOG.md` for anything a user
   would notice. Follow the existing Keep a Changelog headings.
5. Open a pull request. The `CI result` check and CodeQL must pass before
   merging, and it lands as a merge commit so signed commits keep their
   signatures. There is no reviewer requirement, but expect questions.

Commits are signed and written in the past tense, with a subject of 60
characters or fewer. Use Australian English in prose (licence, behaviour,
organise).

## How releases happen

Releases are cut by the maintainer from a signed tag on `main`. To release:

1. In `CHANGELOG.md`, rename `[Unreleased]` to `[X.Y.Z] - YYYY-MM-DD` and
   add the `[X.Y.Z]: compare` link at the bottom.
2. Start a fresh, empty `[Unreleased]` section above it.
3. Merge to `main`.
4. On an up-to-date `main` with a clean tree, run
   `make release RELEASE_VERSION=X.Y.Z`. It refuses unless the newest
   changelog version matches and the tag does not already exist, then
   creates a signed tag. It never pushes.
5. Push the tag with the `git push origin vX.Y.Z` command it prints.

`scripts/history-scan.sh` is run before anything is pushed to a public remote.

Pushing the tag starts `.github/workflows/release.yml`. It re-runs CI, checks
the tag is on `main` and signed, builds the four archives (darwin and linux,
amd64 and arm64), attests the checksums and publishes a GitHub Release with
the changelog section as the notes. Preview those notes beforehand with
`scripts/changelog.sh notes X.Y.Z bawdo/caddy_shack`.

If the publish fails, re-run it with
`gh workflow run release.yml --ref vX.Y.Z -f tag=vX.Y.Z`. The `--ref` makes
the called CI run on the tag's commit. A defect in a published release gets a
new patch version, never a moved tag. Published releases are immutable, so
a release cannot be patched or have assets replaced.

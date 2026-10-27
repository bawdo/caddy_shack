# Kaizen: lean and secure

A code review of the whole module on 2026-10-27 found one security issue and
a set of duplications and dead code. This document is the record of what we
believed before acting on it, what we chose to do, and how we will know
whether it worked. The plan that implements it is
`docs/superpowers/plans/2026-10-27-kaizen-lean-and-secure.md`.

## Hypothesis

We believe the codebase has accreted three kinds of avoidable weight, and
that removing them changes behaviour for exactly one user-visible case.

1. The state lock and state load helpers in `reconcile.go` exist so corrupt
   state maps to exit 7 and the lock timeout has one home, but ten call
   sites bypass them with inline copies. Evidence: `cert.go`, `recreate.go`,
   `list.go` and `status.go` all re-spell the same two blocks with a
   hard-coded 5s, so the test-tunable `stateLockTimeout` is honoured by
   only half the callers.
2. `issueAndInstall` re-implements the Caddy-first, state-second,
   roll-back-on-failure protocol that `commit` already owns, plus a
   certificate material restore. Evidence: the 50-line save-failure branch
   is the hardest code in the module to read and the existing `commit`
   covers every step except the material restore.
3. The Caddy admin API is reachable from every container on the Docker host
   because it binds `0.0.0.0:2019` inside the container and the container
   sits on the shared `caddy_shack` network. Evidence: the README says
   "loopback-only", which is true from the host and false from any sibling
   container, including every app registered with `--container`.

If true, the helpers collapse to one implementation each with no test
changes beyond the ones that pin the new shape, the certificate path loses
most of its rollback branch while keeping every existing test green, and a
sibling container that sends `Host: caddy_shack:2019` to the admin API is
refused where today it is accepted.

What would tell us we were wrong: a helper consolidation that forces a test
to change its expected error text, a `commit`-based certificate path that
needs more special cases than the inline version it replaces, or an
`enforce_origin` setting that breaks the host-side client or the drift check.

## Method

Nine tasks on one integration branch, each in its own worktree, each
reviewed against its brief before it is merged. Existing tests are the
regression net: the golden config fixtures under `internal/caddyapi/testdata`
must stay byte-identical through the pure refactor and change only in the
task that deliberately alters the admin block.

Evidence for: `go test -race ./...` and `golangci-lint run` stay clean after
every merge; the line count of `internal/app` falls; the drift check still
reports in sync against a freshly booted Caddy.

Evidence against: any task that cannot keep an existing test green without
weakening its assertion, or a change to `caddyBootScript` that the drift
detector does not surface as "outdated container command" (it must, so
existing installs are told to recreate).

## Desired outcome

- One implementation each of lock acquisition and state loading in the app
  package, with no inline copies. Checkable by grepping for
  `state.AcquireLock` and `state.Load(` in `internal/app` and finding only
  the helpers in `reconcile.go`.
- `issueAndInstall` under 40 lines, delegating rollback to `commit`.
- `docker.Client` has fifteen methods, not sixteen, and `caddyapi.Build`
  does not exist.
- A request to the admin API with a Host header other than
  `127.0.0.1:2019` or `localhost:2019` is rejected, pinned by a test on the
  rendered config, and the README's security notes say what that does and
  does not protect against.
- Every item in the review's findings list has a commit or a recorded
  ruling explaining why it was not done.

## Review findings being acted on

The numbering below is used by the plan.

1. Admin API reachable from sibling containers (should fix).
2. Let's Encrypt private keys in Caddy's autosave (document).
3. PROXY protocol trusts any sender on the LAN listener (note).
4. Ten call sites bypass `acquireStateLock` and `loadState`.
5. `issueAndInstall` re-implements `commit`.
6. Dead and duplicated code in the docker layer: `caddyapi.Build`,
   `CLI.Remove`, `isPortAllocatedErr` versus `addrInUse`, `NetworkAttached`.
7. Trust-store checks duplicate their first 25 lines and bypass `ExportCA`.
8. `BuildWith` is 300 lines with 25 types declared inside it.
9. Seventeen command bodies repeat the `app.New()` preamble.
10. Nits: `classifyError` pass-through, "Task 16" comment, misplaced doc
    comment on `joinPorts`, `restarting` reported as `running`, lock held
    across network waits in `recreateCaddy`.

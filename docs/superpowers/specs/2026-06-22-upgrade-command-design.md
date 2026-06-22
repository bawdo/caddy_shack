# caddy_shack upgrade — design

Date: 2026-06-22

## Goal

Add a `caddy_shack upgrade` command that re-pulls the pinned `caddy:2`
image and, if the digest changed, recreates the Caddy container against it
(preserving both named volumes), then re-pushes the registered backends from
`state.json` so all `.test` URLs keep working. No-op when already current.

This fulfils the README's "Updating the image without losing configuration
is planned for a later phase".

## Why this is simpler than mailpitter's equivalent

The sibling project `mailpitter` shipped the same feature recently. Two
caddy_shack facts make ours leaner:

- We upgrade within the `caddy:2` tag (the same tag `install` pins), so
  there is **no new image constant**. `upgrade` re-pulls `CaddyImage` and
  compares image IDs to decide whether to recreate. `caddy:2` is a moving
  tag, so a re-pull can fetch a newer digest. Staying on `caddy:2` avoids a
  surprise major-version jump.
- caddy_shack already holds every backend in its own `state.json` (the
  source of truth, pushed to Caddy via `POST /load`). So route restoration
  after a recreate is a `caddyapi.Build(s)` + `Load`, the direct analogue of
  mailpitter's re-register step. The Caddy container boots from an
  admin-only bootstrap Caddyfile with no `--resume`, so its running route
  config is ephemeral and must be re-pushed; `state.json` is what makes that
  reliable.

## Behaviour

`caddy_shack upgrade`:

1. `DaemonReachable` → `ErrDockerUnreachable` (4) on failure.
2. `ContainerState(caddy_shack)`; `StateAbsent` → `ErrNotInstalled`
   (6, "run caddy_shack install").
3. `PullImage(CaddyImage)`.
4. Compare `ImageID(CaddyImage)` with `ContainerImageID(caddy_shack)`.
   - **Equal**: best-effort version probe; return
     `UpgradeReport{AlreadyCurrent: true, Version: …}`. The container is left
     exactly as it was. No state lock is taken on this path.
   - **Differs**: continue to the recreate flow.
5. Acquire the state lock (`ErrLockContention`, 8, on timeout) — mirrors
   `register`.
6. `Load` state (the backends). Corrupt → `ErrStateCorrupt` (7). Done
   *before* touching the container, so a corrupt-state upgrade bails without
   any container mutation.
7. `Remove(caddy_shack)`.
8. `EnsureVolume(CaddyDataVolume)`, `EnsureVolume(CaddyConfigVolume)`
   (idempotent; the volumes are preserved, so certs and ACME state survive).
9. `CreateAndStart(caddyContainerSpec(CaddyImage))` via a shared spec builder
   extracted from `install.go`.
10. `ensureSharedNetwork()` — reattach the fresh container, mirroring
    install (container-mode `--container` backends need the shared network).
11. Wait for the admin API to come up (bounded `caddy.Reachable` poll), then
    `caddy.Load(ctx, caddyapi.Build(s))` to restore every route. A reload
    failure returns a descriptive generic error (exit 1).
12. Best-effort version probe → `UpgradeReport{AlreadyCurrent: false,
    Version: …}`.

There is no confirmation prompt and no volume is ever removed, consistent
with how install/start/stop behave. Like install, a successful upgrade
leaves the container running even if it was stopped beforehand: a recreate
via `docker run -d` starts it and we do not stop it again.

### Two deliberate trade-offs

- `upgrade` re-pulls `caddy:2` even on the no-op path. This is cheap and is
  the only way to learn the latest digest before comparing image IDs.
- A reload failure after a successful recreate returns exit 1 even though the
  container is upgraded and running. Routes are not restored in that case, so
  the operation has not fully succeeded; the error message points the user at
  `caddy_shack status`.

## Version reporting

`docker exec caddy_shack caddy version` via the existing `docker.Exec`,
taking the first whitespace-separated field of the output (e.g. `v2.8.4`).
Best-effort, a single call with no retry: if it fails (container not ready
yet), the report carries an empty version and the cmd layer prints a
fallback pointing at `caddy_shack status`. Caddy's admin API has no clean
version endpoint, so exec is the pragmatic source.

## Preconditions and errors

All reuse existing sentinels and exit codes; **no new ones** are introduced.

| Condition                              | Sentinel / code              |
|----------------------------------------|------------------------------|
| docker daemon unreachable              | `ErrDockerUnreachable` (4)   |
| container absent (not installed)       | `ErrNotInstalled` (6)        |
| state file corrupt                     | `ErrStateCorrupt` (7)        |
| state lock held by another process     | `ErrLockContention` (8)      |
| config reload failed after recreate    | generic (1), descriptive     |

## Components

### 1. `internal/docker` (interface + cli.go + fake.go)

Add two read methods so images can be compared by ID:

- `ImageID(ref string) (string, error)` — shells out to
  `docker image inspect --format '{{.Id}}' <ref>`.
- `ContainerImageID(name string) (string, error)` — shells out to
  `docker inspect --format '{{.Image}}' <name>`.

Equal IDs mean the container already runs the pulled image. Both work
against stopped containers. The in-memory `Fake` gains configurable return
maps for these.

### 2. `internal/app/install.go` — shared container-spec builder

`install.go` builds the `docker.ContainerSpec` (ports, both volumes, the
bootstrap Caddyfile Command) inline. Extract it so `install` and `upgrade`
build identical specs and cannot drift:

```go
// caddyContainerSpec returns the ContainerSpec for the managed Caddy
// container running the given image.
func caddyContainerSpec(image string) docker.ContainerSpec
```

`install` is refactored to call it with `CaddyImage`; behaviour unchanged.

### 3. `internal/app/upgrade.go`

```go
// UpgradeReport is what `upgrade` returns; the cmd layer renders it.
type UpgradeReport struct {
    AlreadyCurrent bool
    Version        string // empty if the version probe was unavailable
}

func (a *App) Upgrade(ctx context.Context) (UpgradeReport, error)
```

Plus a small bounded `waitForAdmin` helper that polls `a.caddy.Reachable`.
Its timeout and interval are unexported package vars so tests can shrink
them; against an `httptest` server (reachable on the first poll) it sleeps
zero times.

### 4. `cmd/upgrade.go`

Thin Cobra command following the `status` pattern: calls `Upgrade`, then
renders the report via an extracted `renderUpgrade(w io.Writer, rep
app.UpgradeReport)`:

- already current, version known: `already up to date (v2.8.4)`
- already current, version unknown: `already up to date`
- upgraded, version known: `upgraded to v2.8.4`
- upgraded, version unknown:
  `upgrade complete; run caddy_shack status to confirm version`

Registered in `root.go` alongside the other commands; covered by the
`help` bareword pass.

### 5. `README.md`

Add `caddy_shack upgrade` to the container-lifecycle section with a one-line
description (re-pulls `caddy:2` and recreates the container, preserving certs
and registered backends; no-op when already current). Remove the "Updating
the image without losing configuration is planned for a later phase" caveat.

## Data flow

```
docker pull caddy:2
  -> ImageID(caddy:2) vs ContainerImageID(caddy_shack)
     -> equal:    caddy version  -> "already up to date (vX.Y.Z)"
     -> differs:  lock -> load state -> rm -> ensure volumes -> run
                  -> ensure network -> wait admin -> POST /load(Build(state))
                  -> caddy version -> "upgraded to vX.Y.Z"
```

The named volumes `caddy_shack_data` and `caddy_shack_config` are never
removed, so certs/ACME state survive the recreate; backends are restored from
`state.json`.

## Testing (TDD)

- `internal/docker/fake_test.go`: cover the two new methods.
- `internal/app/upgrade_test.go` (Fake docker + `httptest` Caddy, matching
  the existing app tests):
  - not installed (`StateAbsent`) → `ErrNotInstalled`.
  - daemon unreachable → `ErrDockerUnreachable`.
  - already current (equal image IDs) → no `Remove`/`CreateAndStart`, report
    `AlreadyCurrent: true`, state untouched.
  - successful upgrade → asserts `PullImage(CaddyImage)`, `Remove`,
    `CreateAndStart` with `CaddyImage`, both volumes ensured, network
    reattached, `Load` called with the rebuilt config, version parsed from a
    scripted `Exec` response.
  - corrupt state → `ErrStateCorrupt`, no container mutation.
  - reload failure after recreate → generic error.
- `cmd/upgrade_test.go`: command wiring, `help` bareword, and `renderUpgrade`
  output for each branch, matching the style of the other `cmd/*_test.go`.
- Integration test (`test/integration`): out of scope for this slice unless
  trivial to fold into the existing harness.
</content>
</invoke>

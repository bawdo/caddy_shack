# Container-upstream routing for caddy_shack

Date: 2026-06-07
Status: Design (approved for planning)

## Summary

Add an opt-in mode where caddy_shack reverse-proxies `<name>.test` to a
**container** over a shared Docker network, instead of to a host port via
`host.docker.internal`. This lets containerised apps be fronted by caddy_shack
without publishing any host ports, which in turn makes it possible to run many
Dockerised Rails apps at once without app/vite/DB host-port juggling.

The existing `register --port N` behaviour is preserved exactly. Container
routing activates only when the new `--container` flag is supplied.

## Motivation

caddy_shack today always routes through `host.docker.internal:<port>` (see
`internal/app/register.go`, `const UpstreamHost`). The backend service must
therefore listen on a host port. When two apps want the same port (Rails 3000,
Vite 3036) the host port collides, so each app probe-and-bumps to a free host
port.

A separate effort wants to run Rails apps fully containerised (app + Postgres
in a compose stack) so the database never touches a host port. caddy_shack's
host-only routing is the one thing forcing the app and vite ports to stay on
the host even in that model. If caddy_shack can route to the app container over
a shared Docker network, the whole app stack needs zero host ports, which is
the outcome that justifies containerising the app.

This change is the dependency that unblocks that work, so it is specced and
shipped first.

## Goals

- Reverse-proxy a registered name to a container reachable by DNS alias on a
  shared Docker network.
- Keep the existing `--port` / `host.docker.internal` path byte-for-byte
  unchanged. No state-file migration. No behaviour change for current consumers
  (e.g. cpzoner, kakei, and any host-mode bootstrapper apps).
- Let existing installs self-upgrade onto the shared network transparently the
  first time container mode is used, with no manual reinstall.

## Non-goals

- mailpitter changes. Its host-gateway path already serves container consumers
  (`mailpitter register` prints a compose snippet for that case).
- bootstrapper template / devcontainer work. The app-side wiring that consumes
  this feature is a separate spec; section "Consumer contract" only records the
  interface this change exposes.
- Removing the shared network on uninstall. Tracked as a minor follow-up.
- Dynamic per-app `docker network connect`/`disconnect` on register. The single
  shared network avoids the DNS-ambiguity and multi-network lifecycle cost of
  that alternative (see "Alternatives considered").

## CLI surface

```
# Existing behaviour - UNCHANGED
caddy_shack register myapp --port 3000
#   -> dial host.docker.internal:3000

# New additive mode
caddy_shack register myapp --container myapp --port 3000
caddy_shack register myapp-vite --container myapp --port 3036
#   -> dial myapp:3000 (and myapp:3036) over the shared caddy_shack network
```

- `--port <p>` alone: unchanged, dials `host.docker.internal:<p>`.
- `--container <alias>`: new optional flag. When present, the dial becomes
  `<alias>:<p>` and routing goes over the shared network. Requires `--port`.
- `--domain` continues to work in both modes; the wildcard-subdomain behaviour
  (serving `*.<name>` to the same upstream) is unchanged.

### Validation

- `<alias>` must be a single DNS label: `^[a-z0-9][a-z0-9-]{0,62}$`.
- `--container` supplied without `--port` is `ErrInvalidArgs` (exit code 2).
- `--port` range check is unchanged (1-65535).

## The shared network

- Fixed name: **`caddy_shack`** (matches the managed container name and state
  naming). Not overridable in this version; can be revisited if a consumer
  needs a different network name.
- caddy_shack ensures the network exists and the caddy container is attached to
  it, in addition to the existing host port publishes (80/443/2019 on
  127.0.0.1).
- The browser -> caddy hop is unchanged: TLS is still terminated at the host
  443 listener for `https://<name>.test`. Only the caddy -> upstream hop moves
  onto the Docker network when container mode is used.

## Lifecycle and migration

The network is ensured (created if missing) and caddy attached (connected if
not already) at the following points, so both fresh installs and
already-running older caddy containers converge onto the network without a
manual reinstall:

- `install` (container absent): ensure network -> create caddy -> connect caddy
  to the network.
- `start` (container stopped): start -> ensure connected (idempotent).
- `register --container ...`: ensure caddy is connected to the network before
  pushing the new Caddy config. This is what lets an already-running old caddy
  transparently gain the network on first container-mode use.
- `register --port ...`: does NOT touch the network (preserves the unchanged
  host-mode path).
- `doctor`: new check that the network exists and caddy is attached; `--fix`
  remediates by ensuring + connecting.

The network is never auto-removed; other apps may share it.

## Internal changes

### `internal/docker` (Client interface + CLI impl + fake)

Add two idempotent methods to `docker.Client`:

- `EnsureNetwork(name string) error` - `docker network create <name>`, treating
  an already-existing network as success.
- `ConnectNetwork(network, container string) error` - `docker network connect
  <network> <container>`, treating "already exists" / "endpoint already exists
  in network" as success (mirroring how `Remove` treats "no such object" as a
  no-op success).

Implement both in `internal/docker/cli.go` and add fakes in
`internal/docker/fake.go` that record calls and honour injected errors, so the
app-layer tests can assert lifecycle behaviour without a real daemon.

### `internal/app/register.go`

- `RegisterInput` gains `Container string`.
- Dial construction branches:
  - `Container == ""` -> `fmt.Sprintf("%s:%d", UpstreamHost, in.Port)` (today's
    behaviour).
  - `Container != ""` -> `fmt.Sprintf("%s:%d", in.Container, in.Port)`.
- The shared-network ensure (`EnsureNetwork` + `ConnectNetwork(network,
  CaddyContainerName)`) runs only in the container branch, before the Caddy
  `/load`. The host-mode branch is untouched.
- Alias validation as above.

### `internal/caddyapi` and `internal/state`

**Unchanged.** `state.Backend.Upstream` already stores the full dial string, and
`caddyapi.Build` already routes `reverse_proxy` to whatever `Dial` a backend
has. This is why the change is small: the config builder and state schema are
already upstream-agnostic.

### `cmd/register.go`

Add the `--container` string flag with help text describing the shared-network
container mode and that `--port` is still required.

### `internal/app` lifecycle (`install.go`, `lifecycle.go`, `doctor*.go`)

Wire the ensure/connect calls into install and start, and add the doctor check
and `--fix` remediation described under "Lifecycle and migration".

## Caddy resolution semantics

Caddy resolves the container alias via Docker's embedded DNS on the shared
network at request time. Implications:

- If the app container is not up yet, requests return 502 until it starts.
  Acceptable for local development.
- On app container restart (new IP), Caddy re-resolves; no caddy_shack action
  needed.
- HMR websockets proxy natively through Caddy's `reverse_proxy`, so a vite
  upstream registered in container mode gets working HMR over
  `https://<app>-vite.test` (an improvement over the host-mode Rack::Proxy
  limitation that could not tunnel the HMR upgrade).

## Consumer contract (interface only; implemented in bootstrapper later)

For an app to use container mode:

1. The app's compose declares the shared network as external:
   ```yaml
   networks:
     caddy_shack:
       external: true
   ```
2. The app service attaches to it with a stable alias:
   ```yaml
   services:
     rails-app:
       networks:
         default: {}
         caddy_shack:
           aliases: ["<app>"]
   ```
3. `caddy_shack install` has created the `caddy_shack` network (a prerequisite
   the host-side launcher can ensure by calling `caddy_shack start`/`install`).

The host-side launcher then registers:

```
caddy_shack register <app>      --container <app> --port 3000
caddy_shack register <app>-vite --container <app> --port 3036
```

This section records the interface only. The bootstrapper-side template work is
a separate spec.

## Testing

- Golden config test for a container-dial backend, alongside the existing
  host.docker.internal golden (which stays, as a regression guard).
- `register --container` sets `Dial=<alias>:<port>`.
- `--container` without `--port` returns `ErrInvalidArgs`.
- Port-only register output and state are byte-for-byte unchanged
  (regression guard).
- `EnsureNetwork` / `ConnectNetwork` idempotency on the fake (already-exists is
  success).
- Lifecycle: install and start ensure network + connect; `register --container`
  ensures connect; `register --port` does not.
- doctor reports a missing/detached network and `--fix` remediates.
- Integration test (docker-gated, as the suite already gates external-tool
  tests) registering a container upstream and reaching it over the shared
  network.

## Exit codes

No new exit codes. Reuse the existing stable set: invalid args (2), docker
daemon unreachable (4), caddy not running (5), generic failure (1, e.g. network
creation failing while the daemon is reachable).

## Alternatives considered

- **Dynamic per-app network connect/disconnect on register.** caddy stays on
  the default bridge and `docker network connect`s each app's own compose
  network on register, disconnecting on deregister. Rejected: caddy ends up on
  N networks, with DNS ambiguity when two apps share a service name, plus a
  connect/disconnect lifecycle to get right. The single shared network with
  per-app aliases avoids all of it.
- **`--upstream <host:port>` raw-dial flag.** More composable but less
  self-documenting and overloads the meaning of the dial. `--container` +
  `--port` keeps the host-port concept (`--port`) intact and adds an explicit,
  validated container alias.

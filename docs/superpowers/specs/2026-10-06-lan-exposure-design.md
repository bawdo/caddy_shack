# caddy_shack LAN exposure: design

Date: 2026-10-06
Branch: `feature/lan-exposure`

## Hypothesis

We believe LAN devices can reach opted-in backends over HTTPS, while `.test`
names stay unreachable from the LAN and unaffected by loss of the LAN IP, by
putting the LAN binding in a stateless haproxy forwarder container that feeds
an unpublished Caddy server holding LAN routes only.

Evidence, from probes run on 2026-10-06 with throwaway busybox containers:

| Probe | Docker Desktop (Mac) | Native Docker Engine (Omarchy) |
|---|---|---|
| Same host port on `127.0.0.1` and the LAN IP, mapped to two different container ports, gives two distinct listeners | yes | yes |
| `docker start` of a container whose published host IP is absent | whole container fails, loopback bindings included | whole container fails (`state=created`) |

If true, the main Caddy container never needs a LAN binding, its spec is
unchanged by LAN mode, and a vanished LAN IP can only take down LAN access.

The part most likely to be disproven: the real LAN client IP reaching Caddy
on Docker Desktop. Its published ports go through a userspace proxy that may
replace the source address before haproxy sees it, in which case PROXY
protocol faithfully carries the wrong address.

## Method

Build `caddy_shack_lan`, the `srv-lan` and `lan-http` Caddy servers, the
`lan enable` and `lan disable` commands, `register --lan` and
`--lan-domain`, and rebind on `restart`. Then check, on both Mac and Linux:

1. A second LAN device gets 200 for a LAN name, with a chain issued by the
   caddy_shack local CA.
2. A request to the LAN IP for a `.test` name gets no route: the TLS
   handshake fails for `.test` SNI, and a LAN SNI with a `.test` Host header
   is rejected.
3. `http://` on the LAN gets a 308 to `https://` for LAN names and a 404
   otherwise.
4. A backend sees the LAN client's IP, not a Docker address.
5. With the forwarder bound to an absent IP, `.test` still returns 200 and
   `status` reports LAN down.
6. In discovered mode, an IP change followed by `caddy_shack restart`
   rebinds the forwarder to the new IP.

Evidence against: any of these fails on either platform. In particular,
check 4 failing on Docker Desktop, or haproxy refusing to start while Caddy
is down.

If check 4 fails on Docker Desktop only, we accept and document it rather
than redesign: the backend still works, it just sees a Docker address. That
decision is made now so the result cannot bend it later.

## Desired outcome

- One command (`caddy_shack lan enable`) and one flag (`register --lan`)
  expose a backend to the LAN, on Mac and Linux.
- Every check in Method passes on both platforms, recorded per platform in a
  Testers Playbook run (or check 4 is recorded as failing on Docker Desktop,
  per the decision above).
- The main Caddy container spec is byte for byte unchanged by LAN mode, and
  the Caddy config with LAN mode disabled is byte for byte what `main`
  produces today (golden test).

## Goal

Let a registered backend optionally carry a second, LAN-facing name (for
example `gamma.arrix.home.lan`) next to its `.test` name (for example
`gamma.arrix.test`). Caddy routes both to the same upstream. `.test` names
stay reachable from this machine only.

## Decisions

| Topic | Decision |
|---|---|
| Isolation | Hard guarantee: the LAN can only reach opted-in LAN names. Enforced by caddy_shack, not by a firewall. |
| Port 80 on the LAN | Redirect to HTTPS only, never serves content |
| TLS | Existing caddy_shack internal CA. LAN devices trust it via `caddy_shack ca`. |
| Platforms | Mac (Docker Desktop) and Linux (native Docker Engine) in v1 |
| Base | `main`. The `feature/letsencrypt-certs` branch is parked. |
| Register UX | `--lan` (configured suffix) and `--lan-domain <fqdn>` (override). Wildcard subdomains as for `.test`. |
| LAN IP | One IPv4 address. Discovered at enable time, or pinned with `--ip`. |
| IP changes | No watcher in v1. `restart` rediscovers in discovered mode. `status` and `doctor` flag drift. |
| Pinned IP | Pinned wins: `restart` never changes a pinned IP. |
| DNS | Manual (wildcard) A record in v1. `dnsz` integration is a later release. |
| LAN binding location | A separate `caddy_shack_lan` container, not the main Caddy container and not the existing relay |
| Forwarder | haproxy, TCP mode, PROXY protocol v2 to Caddy |

### Rejected alternatives

- **Publish everything on `0.0.0.0`.** Simple, but every backend, `.test`
  included, becomes reachable from any network the machine joins, by anyone
  who sets a Host header or uses `curl --resolve`. On Linux, Docker's
  published ports bypass ufw, so the firewall is not a reliable guard.
- **LAN bindings on the main Caddy container.** Proven unsafe by the probes:
  if the LAN IP disappears, Docker refuses to start the container and the
  `.test` sites go down with it. Docker restarting the container at login or
  boot bypasses caddy_shack entirely, so caddy_shack cannot compensate.
- **Bundle the forwarder into `caddy_shack_relay`.** The relay exists only on
  native Linux, serves every host-mode `.test` backend through its unix
  socket, and is always present. Adding the LAN binding would recreate the
  failure above (missing IP takes host-mode `.test` backends down), and the
  relay's host networking means nothing on Docker Desktop. The relay's
  machinery (spec builder, `specDrift`, replace on mismatch) is reused; the
  container is not.
- **socat forwarder.** No PROXY protocol, so every LAN request appears to
  come from the forwarder. Still a new image, so it saves nothing over
  haproxy.
- **A second Caddy as the LAN edge.** Two Caddies sharing one cert store, and
  config pushed to two admin APIs that must stay consistent. Doubles the
  hardest part of caddy_shack.
- **Bind to several LAN IPs.** Ethernet plus Wi-Fi is possible, but each IP
  needs its own A record and drift detection gets harder. Not needed for v1.

## Topology

```
 this machine                                   LAN device
 ───────────                                    ──────────
 https://gamma.arrix.test                       https://gamma.arrix.home.lan
        │                                              │
 127.0.0.1:443 ─┐                           192.168.1.x:443 / :80
 127.0.0.1:80  ─┤ (published, as today)            │ (published)
 127.0.0.1:2019┘                                   ▼
        │                               ┌──────────────────────┐
        │                               │ caddy_shack_lan      │  haproxy, static config,
        │                               │ TCP only, no TLS     │  adds PROXY v2 header
        │                               └──────────┬───────────┘
        │                     caddy_shack network  │ :8443 / :8080 (NOT published)
        ▼                                          ▼
 ┌──────────────────────────────────────────────────────────────┐
 │ caddy_shack (main Caddy, unchanged container spec)           │
 │  srv0     :443  routes: .test names           (as today)     │
 │  srv-lan  :8443 routes: opted-in LAN names only, PROXY proto │
 │  lan-http :8080 308 redirect → https://{host}{uri}           │
 └───────────────────────┬──────────────────────────────────────┘
                         ▼
          same upstream (host relay / host.docker.internal / container alias)
```

- The main Caddy container's spec does not change. No new published ports.
- The only path in from the LAN is `srv-lan` (and `lan-http`, which only
  redirects), and they only hold LAN names.
- `caddy_shack_lan` holds no state. Its haproxy config is built from
  constants; the only variable is the IP in its port bindings.
- TLS terminates once, in the main Caddy.

## State

`state.json` moves from schema 1 to schema 2:

```json
{
  "schema_version": 2,
  "lan": {
    "enabled": true,
    "ip": "192.168.1.12",
    "mode": "discovered",
    "suffix": "arrix.home.lan"
  },
  "backends": {
    "gamma": {
      "domain": "gamma.arrix.test",
      "lan_domain": "gamma.arrix.home.lan",
      "upstream": "...",
      "registered_at": "..."
    }
  }
}
```

- No `lan` block: LAN mode has never been set up. `enabled: false`: switched
  off, settings remembered.
- `mode` is `discovered` or `pinned`.
- `lan_domain` stores the resolved FQDN. Changing the suffix later does not
  rename existing LAN names.
- `state.Load` currently rejects any unknown `schema_version`
  (`internal/state/state.go:71`). It must accept version 1, upgrade it in
  memory, and the next save writes version 2. Unknown versions are still
  rejected.
- Merge note: the parked `feature/letsencrypt-certs` branch also claims
  schema 2. Whichever branch lands second renumbers to schema 3 and migrates
  from both.

## CLI

| Command | Behaviour |
|---|---|
| `lan enable [--ip X] [--suffix S] [--yes]` | Discovers the IP (or pins `--ip`), shows it and asks for confirmation unless `--yes`. `--suffix` is required on first enable and remembered. Saves state, pushes config, ensures the forwarder. Re-running it changes IP, mode or suffix. |
| `lan disable` | Removes the forwarder, pushes config without the LAN servers, saves `enabled: false`. Keeps settings and every `lan_domain`. |
| `register <name> --port P --lan` | `lan_domain = <name>.<suffix>`. Exit 10 if LAN mode is not enabled. |
| `register <name> --port P --lan-domain <fqdn>` | Explicit LAN name. Exit 10 if LAN mode is not enabled. |
| `register` without either flag | Clears `lan_domain`. Register is a full upsert (`internal/app/register.go:47`), so the flags describe the whole backend. |
| `deregister` | Removes both names. |
| `list` | Gains a LAN column. |
| `status` | Gains a LAN section (below). |

- Domain conflict checks (`ErrDomainConflict`, exit 3) cover `.test` and LAN
  names together.
- No separate `lan status` command; `status` covers it.

## Lifecycle

Rebinding never recreates the main Caddy. The IP lives only in
`caddy_shack_lan`'s port bindings, so rebinding means replacing the
forwarder.

### `ensureLAN()`

Modelled on `ensureRelay` (`internal/app/relay.go:122`):

1. LAN mode absent or disabled: ensure the forwarder does not exist. Done.
2. Stored IP not present on any local interface: do not attempt to create
   the container. Return a LAN-down warning naming the IP and the remedy.
   Not an error.
3. Forwarder exists and matches its spec (`specDrift`): start it if stopped.
   Otherwise remove it and create it again.

### Per command

| Command | LAN behaviour |
|---|---|
| `install` | As now, then `ensureLAN()` |
| `start` | As now, then `ensureLAN()` with the stored IP. No rediscovery. |
| `stop` | Also stops the forwarder |
| `restart` | Stop. Discovered mode: rediscover; if the IP changed, save it and print "LAN IP changed A → B, update your A record". Pinned mode: keep the IP. Then `start`, which replaces the forwarder because its bindings drifted. |
| `upgrade` | As now, plus pull the pinned haproxy image and replace the forwarder |
| `sync` | Config push includes the LAN servers, then `ensureLAN()` |
| `register`, `deregister` | Config push only |
| `lan enable` | Validate, discover or pin, confirm. Under the state lock: save state, push config. Then `ensureLAN()`. A failed bind leaves state enabled, reports the error and exits 1. |
| `lan disable` | Remove the forwarder first, then push config without LAN servers, then save `enabled: false` |

- Order is always Caddy config, then forwarder, so LAN routes exist before
  LAN traffic can arrive.
- `srv-lan` and `lan-http` exist whenever LAN mode is enabled, even with no
  LAN names registered, so the forwarder always has a target.
- After a reboot, Docker's restart policy brings the forwarder back if the
  IP is present. If not, it stays down until the next `start` or `restart`;
  `.test` is unaffected.

### Forwarder container

- Name `caddy_shack_lan`. Image pinned to a major version (for example
  `haproxy:3.2-alpine`), alongside `CaddyImage`.
- Attached to the `caddy_shack` network.
- Publishes `<LAN IP>:<LANHTTPSPort>` → container `443` and
  `<LAN IP>:<LANHTTPPort>` → container `80`.
- haproxy in TCP mode: `443` → `caddy_shack:8443`, `80` → `caddy_shack:8080`,
  both with `send-proxy-v2`.
- `caddy_shack` is resolved at runtime (Docker's embedded DNS as a haproxy
  resolver, `init-addr none`), so haproxy starts while Caddy is down and
  recovers when it returns.
- Same restart policy as the main Caddy container.
- `Instance` gains `LANHTTPPort` and `LANHTTPSPort` (80 and 443 in
  production) and a forwarder container name, so integration tests run on
  high ports beside a live install.

## Caddy config

All changes are in `caddyapi.BuildWith` (`internal/caddyapi/config.go`).
With LAN mode disabled, output is byte for byte unchanged.

With LAN mode enabled, two servers are added.

### `srv-lan` (`:8443`)

- `listener_wrappers`: `proxy_protocol` then `tls`. The PROXY header precedes
  the TLS handshake, so the order matters. Policy: require the header.
- Routes: the same exact-then-wildcard route builder `srv0` uses, extracted
  into a shared function, fed with LAN names. Same upstreams.
- `automatic_https.disable_redirects: true`; `lan-http` handles redirects.

### `lan-http` (`:8080`)

- Same `proxy_protocol` wrapper.
- Host-matched route for LAN names (exact and wildcard): 308 to
  `https://{http.request.host}{http.request.uri}`. Everything else: 404.
- `automatic_https.disable: true`, otherwise Caddy would try to enable TLS on
  a host-matched server that is not on the HTTP port.

### Keeping `.test` off the LAN listener

Caddy's certificate cache is global, not per server. Without care, a LAN
client sending SNI `gamma.arrix.test` to `srv-lan` would complete the TLS
handshake with the `.test` cert and then receive Caddy's default response
for an unmatched host (an empty 200). Three layers prevent that:

1. `tls_connection_policies` on `srv-lan` matching SNI against LAN names
   only, with no catch-all policy, so other handshakes fail.
2. `strict_sni_host: true`, so a client cannot complete TLS as a LAN name
   and then send a `.test` Host header.
3. A terminal catch-all route that aborts the connection.

### TLS automation

LAN names and their wildcards join the existing internal-issuer subjects.

### Accepted risk: PROXY header trust

`srv-lan` accepts a PROXY header from any peer that can reach `:8443`, which
is any container on the `caddy_shack` network. One of the user's own app
containers could in principle spoof a client IP. Restricting `allow` to the
network subnet would tie config building to Docker inspection. Accepted for
a home development machine.

### Out of scope, noted

- `srv0` returns an empty 200 for unknown hosts on loopback today.
- The admin API listens on `0.0.0.0:2019` inside the container, reachable by
  containers on the `caddy_shack` network. The forwarder only passes 80 and
  443, so it is not exposed to the LAN by this design.

## IP discovery

- Candidates: IPv4 addresses on interfaces that are up, not loopback, and in
  RFC 1918 ranges.
- Excluded interface names: `utun*`, `tun*`, `wg*`, `tailscale*`, `docker*`,
  `br-*`, `veth*`, `bridge*`, `vmnet*`, `awdl*`, `llw*`.
- The default-route source address (found with a UDP dial to an external
  address; nothing is sent) wins if it passes the filters.
- Otherwise, if exactly one candidate remains, use it and say so. None, or
  several: error listing the candidates and suggesting `--ip`.
- Pure Go standard library (`net.Interfaces`, `net.Dial`), no cgo, same code
  on both platforms. Injected behind an interface for tests.

## Validation and errors

- `--ip`: IPv4, not loopback, present on an interface at enable time.
- `--suffix` and `--lan-domain`: valid DNS names, not under `.test`.
  `--lan-domain` need not be under the suffix.
- "Address already in use" on the LAN IP's 80 or 443 is reported as a plain
  message naming the IP and port.

### Exit codes

Adds to the stable contract:

- `10`: LAN mode not enabled (`register --lan` or `--lan-domain` while LAN
  mode is off).

LAN down during `start`, `restart`, `sync` or `install` is a warning with
exit 0, so scripts that bring up `.test` do not break away from home. A
failed bind during `lan enable` exits 1.

## Status

Informational; does not change the exit code.

```
LAN:        enabled (discovered) 192.168.1.12, suffix arrix.home.lan
forwarder:  running
drift:      none
backends:   3 with LAN names
```

Drift example: `interface now has 192.168.1.40, run caddy_shack restart`.

## Doctor

All checks are SKIP when LAN mode is off.

| Check | FAIL / WARN | `--fix` |
|---|---|---|
| `lan-ip-present` | FAIL if the stored IP is not on any interface | Rebind in discovered mode; none when pinned |
| `lan-forwarder` | FAIL if missing, stopped or drifted | `ensureLAN()` |
| `lan-ip-drift` | WARN in discovered mode when discovery gives a different IP | Rebind |
| `lan-path` | FAIL if a local TLS probe to the LAN IP with a LAN name's SNI does not return the CA-issued cert, or a probe with `.test` SNI completes | none |
| `lan-dns` | WARN for each LAN name not resolving to the stored IP | none (`dnsz` later) |
| `lan-firewall` | Linux: WARN that published ports bypass ufw when ufw is active. Mac: WARN if the application firewall blocks all incoming. | none |

`lan-path` runs on this machine, so it proves the listener behaves but not
that other devices get through the network. The manual acceptance run covers
that.

## Testing

### Unit

- Config golden tests: LAN disabled matches a golden file captured from
  `main` before any change. LAN enabled matches a snapshot covering the
  wrapper order, SNI policies, `strict_sni_host`, abort catch-all, `lan-http`
  308 and 404, and TLS subjects.
- Discovery with injected interfaces and route source: default route on
  `en0`, VPN holding the default route with one candidate left, several
  candidates, none, virtual interfaces filtered.
- State: v1 loads and saves as v2; unknown versions still rejected.
- Register: `--lan` and `--lan-domain` resolution, exit 10, `.test` names
  refused as LAN names, conflicts across both spaces, re-register clearing
  `lan_domain`.
- Lifecycle with fakes: `ensureLAN` paths (absent, drifted, stopped, IP
  missing), ordering in `lan enable` and `lan disable`, `restart`
  rediscovering in discovered mode only.
- Doctor: each new check's PASS, WARN, FAIL, SKIP and `--fix` paths.

### Integration (`//go:build integration`, real Docker)

- LAN name over HTTPS: 200, chain verified against the exported CA.
- `.test` SNI to the LAN listener: handshake fails. LAN SNI with `.test`
  Host: rejected.
- `http://` on the LAN port: 308 for LAN names, 404 otherwise.
- Forwarder pinned to `192.0.2.1`: `.test` still 200, `status` reports LAN
  down.
- Rebind on restart, Linux only: an injected discoverer moves the forwarder
  from `127.0.0.2` to `127.0.0.3` (all of `127.0.0.0/8` exists on Linux
  loopback); the old address stops answering and the new one starts.
- Echo backend records the client IP Caddy saw. Same-host only, so not proof
  of Method check 4 on its own.

### Manual acceptance

Method checks 1 to 6 from a second LAN device, on Mac and on Omarchy,
recorded in a Testers Playbook run: real cross-device requests, real client
IP, a real IP change on the Mac, and a reboot with the LAN IP missing.

## Later releases (not v1)

- `dnsz` integration: create and remove `home.lan` records on `lan enable`,
  `register --lan`, `deregister` and IP change.
- Watching for IP changes (SystemConfiguration on Mac, netlink or a
  dispatcher hook on Linux).
- Several LAN IPs.
- Let's Encrypt certificates for LAN names, once `feature/letsencrypt-certs`
  lands.

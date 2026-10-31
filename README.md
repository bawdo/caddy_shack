# caddy_shack

[![CI](https://github.com/bawdo/caddy_shack/actions/workflows/ci.yml/badge.svg)](https://github.com/bawdo/caddy_shack/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/bawdo/caddy_shack/graph/badge.svg)](https://codecov.io/gh/bawdo/caddy_shack)
[![Go Reference](https://pkg.go.dev/badge/github.com/bawdo/caddy_shack.svg)](https://pkg.go.dev/github.com/bawdo/caddy_shack)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/bawdo/caddy_shack/badge)](https://scorecard.dev/viewer/?uri=github.com/bawdo/caddy_shack)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/16115/badge)](https://www.bestpractices.dev/projects/16115)

CaddyShack is a CLI tool that helps to deploy and manage a locally running
caddy instance..

The purpose of this is so an engineer can easily spin up a local project and
register that project with caddy and have https and stable/distinct local URLs
for these projects.

This is a helpful wrapper around the Caddy API that helps do things like:

- Register a backend web service (`caddy_shack register myapp --port 3000`)
- Deregister it on shutdown (`caddy_shack deregister myapp`)
- List the currently-registered backends and whether Caddy is serving them
  (`caddy_shack list`)
- Report container health and whether Caddy's live config matches the
  registry (`caddy_shack status`), and put it right (`caddy_shack sync`)

CaddyShack also simplifies installing Caddy (via Docker), keeping it running,
upgrading it without losing configuration, and checking on its status.

CaddyShack only manages the locally running caddy server via the API.

[Caddy](https://caddyserver.com/)
[Caddy API](https://caddyserver.com/docs/api)

## Install

### From a release

Each [release](https://github.com/bawdo/caddy_shack/releases) ships four
archives:

- `caddy_shack_<version>_darwin_amd64.tar.gz`
- `caddy_shack_<version>_darwin_arm64.tar.gz`
- `caddy_shack_<version>_linux_amd64.tar.gz`
- `caddy_shack_<version>_linux_arm64.tar.gz`

Download the archive for your platform and `checksums.txt` from the Releases
page, then verify, unpack and install it. The full verification commands (tag
signature, attestation pinned to the release commit, checksums) are in
[SECURITY.md](SECURITY.md#verifying-a-release). At minimum, check the
checksum:

```
sha256sum -c checksums.txt --ignore-missing
tar -xzf caddy_shack_<version>_<os>_<arch>.tar.gz
install -m 0755 caddy_shack_<version>_<os>_<arch>/caddy_shack /usr/local/bin/caddy_shack
```

The attestation check in SECURITY.md needs the [GitHub CLI](https://cli.github.com/);
skip it if you don't have `gh`, but keep the checksum check. macOS has no `sha256sum`,
so use `shasum -a 256 -c checksums.txt --ignore-missing` there.

### From source

With Go 1.25 or newer:

```
go install github.com/bawdo/caddy_shack@latest
```

Or from a clone:

```
make install
```

## Prerequisites: local DNS for `.test`

CaddyShack issues HTTPS URLs under the `.test` TLD (reserved by RFC 2606, safe
to use locally). macOS does not resolve `.test` by default, so a one-time DNS
setup is required before registering backends.

The recommended approach uses `dnsmasq` and the macOS resolver mechanism:

```
brew install dnsmasq
mkdir -p $(brew --prefix)/etc/dnsmasq.d
echo 'address=/.test/127.0.0.1' > $(brew --prefix)/etc/dnsmasq.d/caddy_shack.conf
sudo brew services restart dnsmasq

sudo mkdir -p /etc/resolver
echo 'nameserver 127.0.0.1' | sudo tee /etc/resolver/test
```

Verify:

```
dscacheutil -q host -a name anything.test
# expect: 127.0.0.1
```

`caddy_shack doctor --fix` will install dnsmasq and write the resolver
entries for you.

## Omarchy (Linux)

The steps above are macOS-specific: `brew`, `dscacheutil`, and
`/etc/resolver/` don't exist on Linux. Omarchy has no NetworkManager either,
so the usual `nmcli`-based dnsmasq guides for Linux don't apply directly.
Omarchy manages networking with `systemd-networkd` and DNS with
`systemd-resolved`, configured via drop-ins - the same mechanism Omarchy
itself uses for its own DNS provider picker (`omarchy-setup-dns`) and for the
Docker DNS stub listener (`/etc/systemd/resolved.conf.d/20-docker-dns.conf`).

`dnsmasq` is still required - `.test` needs wildcard resolution (`foo.test`,
`*.foo.test`, any name under the TLD -> `127.0.0.1`), and neither
`/etc/hosts` nor `systemd-resolved` can synthesize a whole TLD themselves.
`systemd-resolved` can only *route* queries for a domain to a nameserver; you
still need a real nameserver answering them, which is what dnsmasq is for.

1. Point dnsmasq at `.test` and stop it from claiming the wildcard address on
   port 53 (`systemd-resolved`'s stub listener already owns
   `127.0.0.53:53`/`127.0.0.54:53`, and dnsmasq's default of binding all
   interfaces collides with that):

   ```
   sudo mkdir -p /etc/dnsmasq.d
   sudo tee /etc/dnsmasq.d/caddy_shack.conf <<'EOF'
   # caddy_shack: answer .test only; never forward (avoids a resolved<->dnsmasq loop)
   address=/.test/127.0.0.1
   no-resolv
   local=/#/
   EOF

   sudo sed -i \
     -e 's/^#conf-dir=\/etc\/dnsmasq.d\/,\*\.conf$/conf-dir=\/etc\/dnsmasq.d\/,*.conf/' \
     -e 's/^#bind-interfaces$/bind-interfaces/' \
     /etc/dnsmasq.conf

   sudo systemctl reset-failed dnsmasq   # clears any earlier port-53 failures
   sudo systemctl enable --now dnsmasq
   ```

   `no-resolv` and `local=/#/` matter. `systemd-resolved` sends *every*
   query to its global DNS server (step 2's `DNS=127.0.0.1`), not just
   `.test`, and dnsmasq by default forwards anything it can't answer to
   `/etc/resolv.conf` - which is resolved's own `127.0.0.53` stub. The two
   then bounce non-`.test` queries between each other. Real names still work
   (your router answers first), but every lookup for a name that doesn't
   exist hangs for up to ~100s instead of failing - on the host and inside
   every container. With these two lines dnsmasq answers `.test` and
   immediately returns NXDOMAIN for everything else, and resolved keeps the
   real answer from your normal DNS. This makes dnsmasq dedicated to `.test`;
   if you use it for anything else, give it an explicit `server=` upstream
   that isn't `127.0.0.53` instead. `caddy_shack doctor` detects the hang
   (`dns-negative-lookups`) and `doctor --fix` rewrites the file.

2. Route only `.test` queries to dnsmasq, leaving every other domain on your
   normal upstream DNS. This is the Omarchy-native equivalent of macOS's
   `/etc/resolver/test` - a drop-in under
   `/etc/systemd/resolved.conf.d/`, not a NetworkManager or per-link
   `systemd-networkd` setting (per-link config on `lo` does not work here;
   `systemd-resolved` never gives loopback an active query scope):

   ```
   sudo tee /etc/systemd/resolved.conf.d/30-caddy-shack-test-tld.conf <<'EOF'
   [Resolve]
   DNS=127.0.0.1
   Domains=~test
   EOF

   sudo systemctl restart systemd-resolved
   ```

3. Verify:

   ```
   resolvectl query anything.test
   # expect: anything.test: 127.0.0.1
   getent ahosts anything.test
   # expect: 127.0.0.1 ...
   time getent ahosts does-not-exist.example
   # expect: no output, exit 2, well under a second
   ```

   Use `getent ahosts`, not `getent hosts` - the latter has a known hang
   against the `resolve` NSS module on some systemd versions unrelated to
   this setup. `curl`, browsers, and everything else that goes through
   `getaddrinfo(3)` are unaffected.

4. Trust Caddy's internal root CA so HTTPS is warning-free. Omarchy/Arch use
   `p11-kit`'s `trust` command in place of macOS's `security`:

   ```
   caddy_shack ca > /tmp/caddy_shack_root.crt
   sudo trust anchor /tmp/caddy_shack_root.crt
   ```

   This updates the shared system trust store (`curl`, `wget`, and
   Chromium-based browsers via NSS/p11-kit all pick it up). Firefox keeps its
   own certificate store and needs the root imported separately
   (Settings -> Privacy & Security -> Certificates -> View Certificates ->
   Import), unless `security.enterprise_roots.enabled` is turned on so it
   defers to the system store.

`caddy_shack doctor` detects Linux and checks/fixes DNS and CA trust the
Linux way (`getent`/`resolvectl` and a system CA bundle scan instead of
`dscacheutil`/`security`), so on Omarchy `doctor` and `doctor --fix` do steps
1-4 for you rather than reporting a false `FAIL`. The automatic DNS fix only
supports `systemd-resolved` setups (Omarchy and most modern distros); on
other DNS stacks it declines and points back at this section.

### Host-mode backends on native Linux

On Docker Desktop (and OrbStack, Colima, Lima, Rancher Desktop) Caddy reaches
host-mode backends at `host.docker.internal:<port>`, which the engine
forwards to the host's loopback. Native Docker Engine on Linux does neither:
it has no `host.docker.internal` by default, its replacement (the docker0
gateway, e.g. `172.17.0.1`) can't reach services bound to `127.0.0.1`, and a
host firewall such as ufw drops container-to-host traffic anyway.

So on native Linux, `caddy_shack install` also runs a small relay:

- `caddy_shack_relay` - a second `caddy:2` container on the **host** network
  whose only listener is a unix socket in the `caddy_shack_relay_socket` volume
  (no TCP port, nothing on `0.0.0.0` or the LAN; its admin API is off). It
  forwards each request to `localhost:<port>` (both `127.0.0.1` and `::1`).
- `caddy_shack` itself is unchanged apart from mounting that volume and
  `--add-host host.docker.internal:host-gateway`. As on every engine it lives
  on the `caddy_shack` network (not the default bridge), so `--container`
  backends work exactly as on macOS.
- Host-mode routes dial the socket, naming the backend's port in a header
  Caddy sets (a client can't override it). `state.json` still records
  `host.docker.internal:<port>`, so it means the same thing on every engine.

Because the relay talks to the host's loopback from inside the host's own
network namespace, backends bound to `127.0.0.1` work, and **no firewall
rule is needed**: no traffic crosses from a container to a host IP.

The engine is classified from `docker info`: Docker Desktop / OrbStack /
Colima / Lima / Rancher Desktop keep the behaviour above; it is treated as
native Linux only when the default bridge's gateway is an address on one of
this machine's interfaces. If it can't tell, it keeps the Docker Desktop
behaviour and says so. `caddy_shack doctor` reports the classification
(`docker-engine`), whether the containers match it (`container-config`),
whether `host.docker.internal` resolves inside Caddy (`host-gateway`), and
probes each host-mode backend from the relay (`backends`).

Upgrading from an older caddy_shack: `docker run` fixes mounts and extra
hosts at creation, so the existing `caddy_shack` container has to be
recreated. Run `caddy_shack install` (or accept the `container-config` fix in
`doctor --fix`). It reuses the `caddy_shack_data` and `caddy_shack_config`
volumes (so the local root CA your browser and OS already trust, and the
issued certificates, are kept), reattaches every Docker network the old
container was on except the default bridge, and re-pushes every registered
backend. `start` and
`register` never recreate; they print this hint instead.

## Usage

Day-to-day flow. Once-off setup first, then register/deregister per project.

### One-time setup

```
caddy_shack install
```

Pulls `caddy:2`, creates two named volumes (`caddy_shack_data`,
`caddy_shack_config`), and starts a single container called `caddy_shack`
listening on `127.0.0.1:80`, `:443`, and `:2019` (admin API). Those host ports
are published on `127.0.0.1` only. The container lives on the shared
`caddy_shack` network only, not the default bridge, so a plain `docker run`
container cannot reach it. Inside Docker the admin API listens on
`0.0.0.0:2019`, so it is reachable from any container on the `caddy_shack`
network, including every app registered with `--container`, and on Docker
Desktop and OrbStack most likely from any container at all (see
[Security notes](#security-notes)).
Idempotent: re-running on an up-to-date install is a no-op. A container
created by an older caddy_shack (for example one still on the default bridge)
is recreated, keeping the volumes, the local CA, networks and backends;
`caddy_shack start` tells you when that is needed. On native Linux it also
starts the `caddy_shack_relay` container (see
[Host-mode backends on native Linux](#host-mode-backends-on-native-linux)).

To get HTTPS without browser warnings, trust Caddy's internal root CA once:

```
caddy_shack ca > /tmp/caddy_shack_root.crt
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain /tmp/caddy_shack_root.crt
```

(Omarchy/Linux: see [Omarchy (Linux)](#omarchy-linux) for the `trust anchor`
equivalent.)

### Container lifecycle

```
caddy_shack start      # idempotent; errors if not installed
caddy_shack stop       # no-op when already stopped
caddy_shack restart
caddy_shack upgrade    # re-pull caddy:2; recreate only if the image changed
caddy_shack status     # --json for scripts; exit 0 healthy, 9 drift, 1 unhealthy
caddy_shack sync       # re-push the registry to Caddy if they differ
```

### Restarts and drift

The registry (`state.json`) is the source of truth; Caddy's config is derived
from it. Caddy autosaves every config caddy_shack pushes to the
`caddy_shack_config` volume and resumes it on boot, so the routes survive a
restart caddy_shack doesn't drive (`docker restart`, a Docker daemon restart,
a host reboot) without any command being run. An autosave Caddy can't load is
skipped rather than left to crash-loop the container.

caddy_shack also checks the live config itself. `start`, `restart`,
`install`, `register` and `deregister` compare Caddy's live config with the
registry and re-push the registry if they differ, so an identical `register`
on project startup also restores lost routes. `status` only reads. It compares
the live config (`GET /config/`) with the registry and exits **9** when Caddy is
up but has drifted, listing the backends that are registered but not live,
any live routes the registry doesn't know about, and whether the http/tls apps
are missing. `list` shows a `LIVE` column (`yes`, `no`, `stale`, or
`unknown` when Caddy can't be reached). `sync` makes Caddy match the registry
and changes nothing else:

```
caddy_shack status || { [ $? -eq 9 ] && caddy_shack sync; }
```

Containers created before this behaviour existed boot without resuming
their config (and older ones still sit on the default bridge with an admin
API that does not check `Origin`). `caddy_shack doctor` reports them
(`container-config`) and
`caddy_shack install` recreates them once, keeping the volumes, the local CA,
networks and backends.

`upgrade` re-pulls the `caddy:2` image and, if the digest changed, recreates
the container against it. Both named volumes are preserved, so certificates
and ACME state survive, every Docker network the container was attached to
(except the default bridge) is reattached, and registered backends are restored from state so every
`.test` URL keeps working. It is a no-op when already current.

### Registering a backend

From a project's startup script:

```
caddy_shack register myapp --port 3000
# https://myapp.test is now live
```

You only supply the port your service listens on locally. Caddy runs in a
container and reaches it on the host's loopback - via `host.docker.internal`
on Docker Desktop / OrbStack / Colima, and via the `caddy_shack_relay`
container on native Linux - so binding `127.0.0.1`, `0.0.0.0`, or `localhost`
all work, with no firewall changes.

Optional `--domain` overrides the default `<name>.test`:

```
caddy_shack register api --port 4000 --domain api.internal.test
```

Register is idempotent: same name + same fields writes nothing, and only
pushes to Caddy if its live config has drifted from the registry; same
name + different fields updates the existing route.

### Subdomains

Registering a backend automatically serves all of its subdomains from the same
upstream. After `caddy_shack register foo --port 2000`, both `foo.test` and any
`*.foo.test` (e.g. `bar.foo.test`, `api.foo.test`) reverse-proxy to port 2000 -
no extra commands.

To point one subdomain at a different service, register it with a dotted name:

```
caddy_shack register bar.foo --port 3000
# bar.foo.test now serves port 3000; the rest of *.foo.test still serves 2000
```

The explicit subdomain always wins over the parent's wildcard.

### Listing and deregistering

```
caddy_shack list           # tabular with a LIVE column; --json for scripts
caddy_shack deregister myapp            # removes myapp and its *.myapp.test wildcard
caddy_shack deregister myapp --cascade  # also removes subdomain overrides (e.g. bar.myapp)
```

`deregister` is idempotent: unknown names exit 0 with a `no such backend`
note on stderr.

By default, deregistering a parent leaves any explicitly-registered subdomain
backends in place and notes how many remain on stderr. `--cascade` removes them
too.

### Trusting the CA (`.test` over HTTPS from other tools/containers)

Caddy signs `*.test` certificates with a local root CA that only caddy_shack
knows about. Anything that needs to verify `https://*.test` (a distroless
container that trusts only public CAs, a script, a language runtime) needs that
root CA in its trust store. `caddy_shack ca` prints it as PEM to stdout, so you
never have to know the container name or reach into Caddy's internal PKI paths:

```
caddy_shack ca > root.crt                 # write the root CA to a file
caddy_shack ca --intermediate             # append the intermediate after the root
caddy_shack ca | docker exec -i myapp \
  tee /usr/local/share/ca-certificates/caddy_shack.crt   # seed a container's trust store
```

Only the PEM goes to stdout (notes and errors go to stderr), so it is safe to
redirect or pipe. The command reads the CA from the running Caddy admin API, so
the export always matches what Caddy is actually serving. It returns exit 6 when
caddy isn't installed and exit 5 when the container isn't running.

### Let's Encrypt certificates for a public domain

`.test` domains are served by Caddy's internal CA. For a domain you actually
own, `caddy_shack cert` obtains a publicly-trusted Let's Encrypt certificate
and loads it into the same running Caddy.

This is useful for local development against a real domain whose public DNS
points at `127.0.0.1`: you get real HTTPS with no trust-store fiddling, and
anything that only trusts public CAs works without extra setup.

Because the domain resolves to loopback, Let's Encrypt cannot reach it to
validate over HTTP. DNS-01 is the only challenge that works, and caddy_shack
runs it manually: it prints the TXT records and waits while you add them at your
DNS provider.

```
caddy_shack cert request alpha.arrix.dev --email you@example.com
```

Requests go to the **staging** environment by default. Staging certificates are
not trusted by browsers, which is the point: prove the DNS flow works without
spending quota. Production allows only five duplicate certificates per week for
the same set of names.

```
caddy_shack cert request alpha.arrix.dev --email you@example.com --production
```

Each certificate covers the domain and its single-label subdomains, so one
request serves both `alpha.arrix.dev` and `vite.alpha.arrix.dev`. That matches
how `register` already routes: registering a backend serves its wildcard too.

```
caddy_shack register web --port 5173 --domain alpha.arrix.dev
# https://alpha.arrix.dev and https://vite.alpha.arrix.dev are both live
```

Note that Caddy wildcards match exactly one label. A backend registered directly
on a subdomain (for example `caddy_shack register vite --domain vite.alpha.arrix.dev`)
is covered by the parent domain's wildcard certificate and correctly uses it. However,
multi-level subdomains are not covered: `*.vite.alpha.arrix.dev` would require its own
certificate request, as would `a.b.alpha.arrix.dev`.

A wildcard order produces **two** TXT records on the same name, one per
identifier. Both must exist at the same time; do not replace the first when you
add the second. caddy_shack shows both at once and then polls your authoritative
nameservers, so pressing Enter early is harmless.

Managing what you have:

```
caddy_shack cert list           # domains, environment, expiry, days left
caddy_shack cert list --json    # for scripts
caddy_shack cert renew alpha.arrix.dev          # no-op unless within 30 days
caddy_shack cert renew alpha.arrix.dev --force  # renew regardless
caddy_shack cert remove alpha.arrix.dev         # stop serving it
```

Certificates last 90 days. Because the challenge is manual, renewal is manual
too: `cert renew` prompts for the TXT records the same way `cert request` does.
`cert list` shows the days remaining so you can see one coming. A failed
certificate request or renewal leaves the previously working certificate
untouched, so a failure is a no-op rather than an outage.

Certificate material is stored on this machine, not inside Caddy's volume, so it
survives `caddy_shack upgrade` and container recreates the same way registered
backends do.

Worth knowing before you point `--production` at a real domain: the private
key travels from that host-side store to Caddy over the loopback admin API,
and Caddy also writes its own copy into the config it autosaves inside the
`caddy_shack` container's Docker volume, in plaintext. That is an accepted
trade-off for a local development tool on a machine you already trust, not a
flaw to work around, but consider it before issuing a certificate for
something that matters.

### Exit codes

Stable contract, documented under `caddy_shack --help`:

```
0  success
1  generic command failure
2  invalid arguments
3  domain conflict (another backend owns this domain)
4  docker daemon unreachable
5  caddy container not running
6  caddy not installed
7  state file corrupt
8  state lock held by another caddy_shack process
9  status only: Caddy is up but its live config does not match the registry
10 LAN mode not enabled (register with --lan or --lan-domain)
11 certificate issuance failed (ACME order, DNS validation, or rate limit)
```

`state.json`'s format is upgraded in place the first time a newer caddy_shack
binary writes it. That upgrade is one-way: downgrading to an older
caddy_shack afterwards is not supported, and it exits 7 rather than reading a
state file from a newer schema version.

### Quick smoke test

```
python3 -m http.server 3000 &
caddy_shack register hello --port 3000
curl -k https://hello.test/                    # via dnsmasq DNS setup above
# or, without DNS setup:
curl -k --resolve hello.test:443:127.0.0.1 https://hello.test/

caddy_shack deregister hello
kill %1
caddy_shack stop
```

### Doctor

Run a self-diagnosis:

```
caddy_shack doctor
```

The output shows one line per check ([PASS], [WARN], [FAIL], [SKIP]). To
fix problems automatically:

```
caddy_shack doctor --fix          # interactive y/N per fix
caddy_shack doctor --fix --yes    # apply every fix non-interactively
caddy_shack doctor --json         # machine-readable output
caddy_shack doctor --no-backends  # skip per-backend reachability probes
```

Doctor exits 1 if any check FAILs (or, in `--fix` mode, if any fix fails).
DNS and trust-store fixes use `sudo`; you'll be prompted for your password
unless `sudo` is already cached.

## LAN exposure

A backend can optionally get a second, LAN-facing name (e.g.
`gamma.arrix.home.lan`) next to its `.test` name, so other devices on the
local network can reach it. `.test` names stay reachable from this machine
only.

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

- The LAN binding lives in its own container, `caddy_shack_lan`, not in the
  main Caddy container. Docker refuses to start a container whose published
  host IP no longer exists (verified on Docker Desktop for Mac and native
  Docker Engine on Linux), so keeping it separate means a vanished LAN IP
  only takes down LAN access, never the `.test` sites.
- `.test` names are unreachable from the LAN by construction: the only path
  in from the LAN is `srv-lan`, which holds routes for opted-in LAN names
  only.
- Port 80 on the LAN only ever redirects to HTTPS.
- TLS terminates once, in the main Caddy, using the same local CA as `.test`.
  LAN devices must trust that CA (see `caddy_shack ca`).
- PROXY protocol v2 carries the client address the forwarder sees through to
  Caddy. Whether backends then see the real LAN client IP depends on the
  Docker engine (see the client IP caveat below).
- On native Linux, Docker's published ports bypass ufw, so binding to the
  LAN IP exposes the ports whatever ufw says.

### Setting it up

One-time setup, on the machine running Caddy:

```
caddy_shack lan enable --suffix arrix.home.lan
```

This discovers the machine's LAN IPv4 address, starts the `caddy_shack_lan`
forwarder bound to it on ports 443 and 80, and asks for confirmation first
(`--yes` skips the prompt).

`--suffix` is the DNS domain appended to a backend's name to form its LAN
name: with `--suffix arrix.home.lan`, `register gamma --lan` serves
`gamma.arrix.home.lan`. It is required the first time and is kept afterwards.
It must be a lowercase DNS name (letters, digits, hyphens and dots), and
neither the suffix nor any LAN name may be `test` or end in `.test`. Choosing
one:

- Prefer a name under `home.arpa` (for example `mac.home.arpa`), which is
  reserved for home networks (RFC 8375). `.lan` names such as `arrix.home.lan`
  are common and work, but are not formally reserved.
- Avoid `.local`, which clashes with mDNS (Bonjour/Avahi).
- Use a domain your local DNS can serve, since you will add a wildcard record
  for it (see below).
- Give each machine its own suffix (see "Several machines on one network"). Pass `--ip 192.168.1.20` to pin the address instead of discovering
it. A failed bind exits 1.

Then, for each backend you want on the LAN:

```
caddy_shack register gamma --port 3000 --lan                         # gamma.arrix.home.lan
caddy_shack register gamma --port 3000 --lan-domain gamma.example.lan  # explicit name
```

`--lan` and `--lan-domain` are mutually exclusive. Registering again without
either flag removes the LAN name. Both flags exit 10 if LAN mode is not enabled.

Two things are needed on the other devices:

- DNS: point a wildcard A record for `*.arrix.home.lan` (your suffix) at the
  LAN IP, on your router or local DNS server. Alternatively, add an A record
  for each LAN name.
- Trust: LAN devices must trust the local CA, since TLS terminates in the main
  Caddy using the same CA as `.test`. Export it with `caddy_shack ca` (see
  above).

To stop exposing, run `caddy_shack lan disable`. Your settings are kept, and
`.test` names are unaffected. It still completes while Caddy is stopped: the
state is saved and the Caddy config is updated on the next start.

### Several machines on one network

Each machine runs its own caddy_shack bound to its own LAN IP, so give each
one its own suffix. LAN names then never clash, and DNS needs one wildcard
record per machine rather than one record per service.

On a Mac at `10.0.0.10`:

```
caddy_shack lan enable --suffix mac.home.lan
caddy_shack register gamma --port 3000 --lan     # gamma.mac.home.lan
```

On a Raspberry Pi at `10.0.0.20`:

```
caddy_shack lan enable --suffix pi.home.lan
caddy_shack register gamma --port 3000 --lan     # gamma.pi.home.lan
```

Then add one wildcard A record per machine to your local DNS:

```
*.mac.home.lan   A   10.0.0.10
*.pi.home.lan    A   10.0.0.20
```

A wildcard covers exactly one label, so `gamma.mac.home.lan` matches
`*.mac.home.lan` but `gamma.arrix.mac.home.lan` would need its own
`*.arrix.mac.home.lan` record. A plain `mac.home.lan` record for the machine
itself can sit alongside the wildcard.

`--lan` builds the LAN name from the backend's name, so a backend called
`arrix-gamma-ecs` becomes `arrix-gamma-ecs.mac.home.lan`. To get a shorter
name, pass it explicitly (re-registering is a full upsert, so repeat the
backend's other settings):

```
caddy_shack register arrix-gamma-ecs --container arrix-gamma-alb-proxy --port 80 \
  --domain gamma.lan.arrix.dev --lan-domain gamma.mac.home.lan
```

Points to keep in mind:

- Changing the suffix later (`lan enable --suffix ...` again) does not rename
  existing LAN names. Re-register each backend to move it to the new suffix.
- Each machine has its own local CA. A device that trusts the Mac's CA will
  still warn on `*.pi.home.lan` until it also trusts the Pi's, so run
  `caddy_shack ca` on each machine and install both roots on your devices.
- The backend receives the LAN name as the `Host` header. Apps that check it
  (Rails `config.hosts`, a proxy matching on hostname) must allow the new
  name, or they reply with an error such as 403.

### Restarts and IP changes

How `restart` treats the LAN address depends on how you enabled it:

- Discovered (no `--ip`): `restart` re-discovers the LAN IP and rebinds the
  forwarder to it, so a change of network (say Ethernet to Wi-Fi) is picked up.
- Pinned (`--ip`): `restart` never changes the IP. If it has gone away, LAN is
  reported as down until it returns.

LAN being down is a notice, not an error: whether the IP is missing or the
forwarder cannot be started (say another process already holds the IP and
port), `start`, `restart`, `sync`, `install` and `upgrade` print a notice
naming the problem, still exit 0, and `.test` keeps working. Run
`caddy_shack doctor` for the details. `lan enable` is the exception: there a
failed bind exits 1.

### Status and doctor

`caddy_shack status` gains a LAN section showing the mode, address and
forwarder state. `caddy_shack doctor` adds `lan-ip-present`, `lan-forwarder`,
`lan-ip-drift`, `lan-path`, `lan-dns` and `lan-firewall`.

### Caveats

- Client IP on Docker Desktop: backends see Docker Desktop's VM address
  (observed as `192.168.65.1`) in `X-Forwarded-For`, not the LAN client's IP.
  On native Linux the real client IP is expected to come through, but this is
  not yet confirmed.
- ufw on native Linux: Docker's published ports bypass ufw, so a firewall rule
  will not stop LAN access to the forwarder's ports.
- State schema: LAN support and Let's Encrypt certificates share schema 2 of
  `state.json`. Older `caddy_shack` binaries cannot read it and report it as
  corrupt (exit 7), so upgrade every copy you use. A pre-release build that
  has only one of the two features will read a schema 2 file but drop the
  other feature's settings the next time it writes, so don't mix them.

## Security notes

caddy_shack is a local development tool and trusts the containers you run next
to it. Know where that trust boundary sits:

- **The admin API is open to sibling containers, and through it the local
  root CA.** Caddy's admin API listens on `0.0.0.0:2019` inside its container,
  with no authentication, so any container on the `caddy_shack` network (every
  app registered with `--container` is on it) can call it. The worst it can do:
  `POST /load` a config with a `file_server` route rooted at
  `/data/caddy/pki/authorities/local` and download `root.key`, the private key
  of the local root CA this README tells you to trust system-wide. As far as we
  know that root carries no name constraints, so whoever holds the key can
  mint a certificate for any name that every browser on the host accepts. The
  same access lets a hostile sibling replace the whole config, proxy traffic to
  ports on your host, and read the private keys of loaded certificates. To
  limit the damage:
  - do not trust the root in the system store on a machine that runs
    containers you do not trust;
  - remove it from the store when you stop using caddy_shack. Print its subject
    with `caddy_shack ca | openssl x509 -noout -subject`, then on macOS run
    `sudo security delete-certificate -c "Caddy Local Authority - 2024 ECC Root" /Library/Keychains/System.keychain`
    with the CN it printed in place of the one shown, or on p11-kit systems
    (Omarchy and most Linux distributions) `sudo trust anchor --remove <file>`
    with the file you added.

  Only attach containers you trust to the `caddy_shack` network. The proper
  fix, a unix socket or an mTLS remote admin endpoint, is noted for future
  work.
- **The Caddy container is on the `caddy_shack` network only.** It is not
  attached to the default bridge, so plain `docker run` containers there
  cannot reach it. On an existing install created by an older caddy_shack,
  `caddy_shack start` tells you to run `caddy_shack install`, which recreates
  the container keeping the volumes, the local CA and backends.
- **On Docker Desktop and OrbStack the admin API is likely reachable from any
  container.** The published `127.0.0.1:2019` is a listener on the host's
  loopback, and `host.docker.internal` forwards to it, so the admin API is
  likely reachable from a container on any network, not only the
  `caddy_shack` network. To check:

  ```
  docker run --rm alpine wget -qO- -S http://host.docker.internal:2019/config/ 2>&1 | head -1
  ```

  A `200` or `403` in that line means it is reachable.
- **`enforce_origin` is on.** The admin API only accepts requests whose
  `Origin` header names `127.0.0.1:2019` or `localhost:2019`, and caddy_shack's
  own client sends `Origin: http://127.0.0.1:2019` on every request. That value
  is fixed: Caddy checks it against this list, not against the port the request
  arrived on, so it is the same whatever host port the admin API is published
  on. This stops
  a browser DNS-rebinding attack on the host: a web page that rebinds its name
  to `127.0.0.1` cannot forge `Origin` (and a rebound `GET` carries none), so
  Caddy refuses it. It does not stop a sibling container, which can set any
  header it likes, and on a wildcard listener such as `0.0.0.0` Caddy skips
  the `Host` check, so `Origin` is the only check there is. Requests you run by
  hand need the header too:
  `curl -H 'Origin: http://127.0.0.1:2019' http://127.0.0.1:2019/config/`.
  When the client meets a Caddy still running the pre-origins admin block (an
  older install resuming its old autosave), which refuses any request that
  carries `Origin`, it retries once without the header, so upgrades do not
  lock themselves out; the next config push installs the new block.
- **Let's Encrypt private keys live in three places.** The key is embedded in
  the config caddy_shack pushes to Caddy, so it sits in the certificate store
  on the host, in Caddy's autosave in the `caddy_shack_config` volume, and is
  readable by anyone who can reach the admin API via `GET /config/`.
- **The LAN listener trusts any PROXY protocol header.** Its PROXY protocol
  wrapper accepts a header from any sender on the shared network. An `allow`
  list naming only the forwarder's own /32 would exclude siblings, but it
  needs the forwarder pinned to a fixed address and the config re-rendered on
  every forwarder start. The only impact is a spoofable client address in
  Caddy's logs and in the `X-Forwarded-For` header backends see during
  development, so it is not worth it.

## Development

Run the local pre-CI gauntlet before pushing:

```
make pre-ci
```

Auto-fix formatting along the way:

```
make pre-ci-fix
```

The script runs `govulncheck` and a short fuzz pass (5 seconds per target)
along with the other checks. It does not run the Docker-backed integration
tests; `make integration` runs those, matching the CI job.

Install a local build into `$GOBIN`:

```
make install
```

### Releases

Releases are cut from a signed tag by `make release RELEASE_VERSION=X.Y.Z`. The
release notes come from `CHANGELOG.md`, and `make changelog` checks that it is
well formed and matches the tags. A published release is immutable and cannot
be patched: a bad release means cutting a new version. See
[CONTRIBUTING.md](CONTRIBUTING.md) for the full ritual.

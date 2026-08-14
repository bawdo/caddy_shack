# caddy_shack

CaddyShack is a CLI tool that helps to deploy and manage a locally running
caddy instance.

The purpose of this is so an engineer can easily spin up a local project and
register that project with caddy and have https and stable/distinct local URLs
for these projects.

This is a helpful wrapper around the Caddy API that helps do things like:

- Register a backend web service (`caddy_shack register myapp --port 3000`)
- Deregister it on shutdown (`caddy_shack deregister myapp`)
- List the currently-registered backends (`caddy_shack list`)
- Report container and admin-API health (`caddy_shack status`)

CaddyShack also simplifies installing Caddy (via Docker), keeping it running,
upgrading it without losing configuration, and checking on its status.

CaddyShack only manages the locally running caddy server via the API.

[Caddy](https://caddyserver.com/)
[Caddy API](https://caddyserver.com/docs/api)

## Install

```
go install github.com/bawdo/caddy_shack@latest
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
   echo 'address=/.test/127.0.0.1' | sudo tee /etc/dnsmasq.d/caddy_shack.conf

   sudo sed -i \
     -e 's/^#conf-dir=\/etc\/dnsmasq.d\/,\*\.conf$/conf-dir=\/etc\/dnsmasq.d\/,*.conf/' \
     -e 's/^#bind-interfaces$/bind-interfaces/' \
     /etc/dnsmasq.conf

   sudo systemctl reset-failed dnsmasq   # clears any earlier port-53 failures
   sudo systemctl enable --now dnsmasq
   ```

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
   ```

   Use `getent ahosts`, not `getent hosts` - the latter has a known hang
   against the `resolve` NSS module on some systemd versions unrelated to
   this setup. `curl`, browsers, and everything else that goes through
   `getaddrinfo(3)` are unaffected.

4. Trust Caddy's internal root CA so HTTPS is warning-free. Omarchy/Arch use
   `p11-kit`'s `trust` command in place of macOS's `security`:

   ```
   docker exec caddy_shack cat /data/caddy/pki/authorities/local/root.crt > /tmp/caddy_shack_root.crt
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

Known gap on Linux, not fixed by the above:

- `host.docker.internal` (what `caddy_shack register` uses by default to
  reach services on the host) is a Docker Desktop feature and isn't provided
  by native Docker Engine on Linux, so host-mode backends won't be reachable
  from the caddy container out of the box. Use `caddy_shack register <name>
  --port <port> --container <alias>` and attach your service to the shared
  `caddy_shack` Docker network instead.

## Usage

Day-to-day flow. Once-off setup first, then register/deregister per project.

### One-time setup

```
caddy_shack install
```

Pulls `caddy:2`, creates two named volumes (`caddy_shack_data`,
`caddy_shack_config`), and starts a single container called `caddy_shack`
listening on `127.0.0.1:80`, `:443`, and `:2019` (admin API, loopback-only).
Idempotent: re-running on an existing install is a no-op.

To get HTTPS without browser warnings, trust Caddy's internal root CA once:

```
docker exec caddy_shack cat /data/caddy/pki/authorities/local/root.crt > /tmp/caddy_shack_root.crt
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
caddy_shack status     # --json for scripts; exit 0 healthy, exit 1 unhealthy
```

`upgrade` re-pulls the `caddy:2` image and, if the digest changed, recreates
the container against it. Both named volumes are preserved, so certificates
and ACME state survive, and registered backends are restored from state so
every `.test` URL keeps working. It is a no-op when already current.

### Registering a backend

From a project's startup script:

```
caddy_shack register myapp --port 3000
# https://myapp.test is now live
```

You only supply the port your service listens on locally. Caddy runs in a
container and always reaches the host via `host.docker.internal`, so it
doesn't matter whether your service binds `127.0.0.1`, `0.0.0.0`, or
`localhost` on the host - the port is the only thing we need.

Optional `--domain` overrides the default `<name>.test`:

```
caddy_shack register api --port 4000 --domain api.internal.test
```

Register is idempotent: same name + same fields is a no-op (no Caddy hit);
same name + different fields updates the existing route.

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
caddy_shack list           # tabular; --json for scripts
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
```

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

## Development

Run the local pre-CI gauntlet before pushing:

```
make pre-ci
```

Auto-fix formatting along the way:

```
make pre-ci-fix
```

Install a local build into `$GOBIN`:

```
make install
```

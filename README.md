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
and checking on its status. Updating the image without losing configuration
is planned for a later phase.

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

### Container lifecycle

```
caddy_shack start      # idempotent; errors if not installed
caddy_shack stop       # no-op when already stopped
caddy_shack restart
caddy_shack status     # --json for scripts; exit 0 healthy, exit 1 unhealthy
```

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

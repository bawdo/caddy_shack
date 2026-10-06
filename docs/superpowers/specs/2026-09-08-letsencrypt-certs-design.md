# Let's Encrypt certificates for public domains

Optional, per-domain Let's Encrypt certificates alongside the existing internal
CA, driven from the CLI. The internal `.test` issuance is unchanged and remains
the default for everything.

## Hypothesis

We believe a locally-running Caddy can serve a publicly-trusted Let's Encrypt
certificate for one domain while continuing to mint internal-CA certificates for
every `.test` domain, from a single generated config, because Caddy's automatic
HTTPS skips any hostname that already has a manually loaded certificate.

Evidence: `caddyapi.Build` already derives TLS subjects from the registered
names rather than a static `*.test`, so the automation policy is ours to
restrict. Caddy documents `apps.tls.certificates.load_pem` as taking precedence
over automation for the names it covers. We have not verified that interaction
in the specific config shape we generate, which pairs an explicit automation
policy pinned to the `internal` issuer with an explicit subjects list.

If true, `caddy_shack cert request alpha.arrix.dev` yields a browser-trusted
`https://alpha.arrix.dev` and `https://vite.alpha.arrix.dev` from the same
container that is still serving `https://anything.test` off the internal CA, with
no change to `register`, `deregister` or `list`.

This can be false in three ways we would see quickly:

1. Caddy adds the loaded-cert subjects to automation anyway and either errors on
   `/load` or issues an internal certificate that shadows the loaded one.
2. A wildcard certificate does not line up with the existing exact-plus-`*.d`
   route pair, so subdomain coverage needs routing changes after all.
3. Excluding subjects from the `internal` policy disables automatic HTTPS for
   those hostnames entirely, leaving them unserved rather than served from the
   loaded certificate.

A secondary belief, weaker and cheaper to test: manual DNS-01 with a human
editing GoDaddy records is workable at a 90-day cadence. This is false if a
single issuance needs more than one round trip to the DNS provider.

## Method

Build the `cert` command group against a real ACME server and check the three
failure modes above directly.

**What gets built.** `internal/acme` wrapping `go-acme/lego/v4` with a custom
two-method `challenge.Provider`; `internal/certstore` owning cert material on
the host; a `certs` map in `state.json` at `schema_version: 2`; `caddyapi.Build`
emitting `load_pem` and excluding those subjects from the `internal` policy;
`cert request`, `cert list`, `cert renew` and `cert remove`.

**What gets measured.**

- Evidence for: an integration test against Pebble and `pebble-challtestsrv`
  shows a TLS handshake on the LE-covered domain presenting the loaded
  certificate, by issuer and serial, while a concurrently registered `.test`
  domain presents the internal CA's certificate. Both from one container, one
  `/load`.
- Evidence for: the same handshake still presents the loaded certificate after
  `caddy_shack upgrade` recreates the container.
- Evidence against: Caddy rejects the generated config, or the handshake on the
  covered domain presents an internal-CA certificate, or presents nothing.
- Evidence against, on the wildcard: `vite.alpha.arrix.dev` fails to verify
  against a certificate that covers `*.alpha.arrix.dev`, which would mean the
  route and subject models do not line up.
- Evidence against, on the manual flow: issuing against Let's Encrypt staging
  for `alpha.arrix.dev` needs more than one visit to GoDaddy.

The dependency cost is already measured rather than hypothesised: linking lego's
core plus `challenge/dns01` adds five modules and takes the binary from 5.31MB
to 5.65MB, about 6 per cent. The 232-module `go list -m all` graph is provider
subpackages that module pruning keeps out of the build.

## Desired outcome

1. `caddy_shack cert request alpha.arrix.dev --production` completes with one
   visit to the DNS provider, and `curl https://alpha.arrix.dev` plus
   `curl https://vite.alpha.arrix.dev` both succeed from the host with no `-k`
   and no added trust anchor.
2. `curl -k https://hello.test` still works from the same container in the same
   session, off the internal CA.
3. The existing unit and integration suites pass unchanged. Any edit to a
   current test is a signal that this feature changed behaviour it should not
   have, and gets called out rather than absorbed.
4. A `state.json` written by the current release loads without tripping exit
   code 7.
5. Certificates survive `caddy_shack upgrade` and a container recreate.

Checkable afterwards by re-running the integration suite and by the manual
acceptance step against `alpha.arrix.dev`.

## Constraint that shapes the whole design

`alpha.arrix.dev` has a public A record pointing at `127.0.0.1`. Let's Encrypt
validates from its own servers, so HTTP-01 and TLS-ALPN-01 cannot work: the
validator would connect to its own loopback. **DNS-01 is the only viable
challenge.**

`arrix.dev` is served by GoDaddy nameservers (`ns17`/`ns18.domaincontrol.com`).
GoDaddy's DNS API is gated behind account-size tiers and its Caddy plugin is
unmaintained, so automated DNS-01 there is unreliable. For now the challenge is
solved manually: caddy_shack prints the TXT records and waits for the operator to
add them. Moving to an automated provider later is a change of one
`challenge.Provider` implementation and nothing else.

## Architecture

### Why caddy_shack does the ACME order, not Caddy

Caddy's ACME automation is all or nothing; it has no mode that pauses and asks a
human for a DNS record. So caddy_shack runs the order itself and hands Caddy the
finished certificate to load.

### `internal/acme`

Wraps `go-acme/lego/v4` behind a small interface so `app` can be tested against a
fake. Responsibilities: account key creation and reuse, opening an order for
`[d, *.d]`, driving the manual challenge, finalising, returning the PEM chain and
key.

lego was chosen over `golang.org/x/crypto/acme` because it already contains the
parts most likely to be got wrong by hand: propagation checking against the
authoritative nameservers (`FindPrimaryNsByFqdn`,
`RecursiveNSsPropagationRequirement`, `PropagationWait`), correct wildcard DNS-01
handling with both identifiers on one `_acme-challenge` name, ARI renewal windows
(`GetRenewalInfo`, RFC 9773), and account registration. It is MIT licensed.

#### The custom provider

We do not use lego's `dns01.DNSProviderManual`. Three reasons, in order of
weight:

1. **One prompt instead of two.** `challenge/resolver/prober.go` splits solvers
   into `parallelSolve` (PreSolve all, Solve all, CleanUp all) and
   `sequentialSolve`, and a provider lands in the sequential bucket only if it
   implements `Sequential()`. lego's manual provider does, so a wildcard order
   prompts twice, once per identifier, with a validation between. Ours does not
   implement `Sequential()`, so both `Present` calls land before any validation
   and the operator makes one trip to GoDaddy.
2. **Testability.** lego's provider does `bufio.NewReader(os.Stdin)` and
   `fmt.Printf` against process globals, bypassing cobra. `doctor --fix` tests
   drive prompts with `cmd.SetIn(strings.NewReader("y\n"))`, which would have no
   effect. lego's own test for it monkey-patches the global `os.Stdin`, which
   also makes it unparallelisable.
3. **It is deprecated,** and its replacement `providers/dns/manual` is a type
   alias to the identical struct. There is nothing to move to.

The implementation is two methods. `Present(domain, token, keyAuth)` calls
`dns01.GetChallengeInfo` and records the FQDN and value. `CleanUp` is a no-op
beyond noting the records can be removed. The prompt fires once from a
`dns01.WrapPreCheck` hook guarded by `sync.Once`, printing every collected record
before delegating to lego's real propagation check, so confirming early is
harmless: lego still polls the authoritative nameservers until both records
resolve.

### `internal/certstore`

Owns certificate material on the host, under the existing config directory:

```
~/Library/Application Support/caddy_shack/
  state.json
  acme/account-staging.key             0600
  acme/account-production.key          0600
  certs/alpha.arrix.dev/fullchain.pem  0644
  certs/alpha.arrix.dev/key.pem        0600
```

Keeping material on the host rather than in Caddy's `/data` volume means
`upgrade` and container recreate restore certificates by the mechanism that
already restores backends: re-push from local state. Writes are atomic, matching
`state.Save`.

### State

`state.json` gains a `certs` map keyed by domain, each record carrying subjects,
`staging` or `production`, issued-at and not-after, and moves to
`schema_version: 2`. `state.Load` migrates a v1 file in memory rather than
returning `ErrCorrupt`, so installs in the field do not hit exit code 7.

Private keys stay in their own files. Only metadata goes in `state.json`.

### Config generation

`caddyapi.Build` takes the loaded certificates as a second argument. For each
domain holding one it emits `apps.tls.certificates.load_pem` with the PEM inline,
and removes that domain's subjects (`d` and `*.d`) from the `internal` issuer
automation policy.

Routing needs no work. `Build` already emits an exact route and a `*.d` route per
backend, so `register web --port 5173 --domain alpha.arrix.dev` serves both
`alpha.arrix.dev` and `vite.alpha.arrix.dev`, which is exactly the set a wildcard
certificate covers. A future per-domain subjects list slots into the cert record
without touching routing.

Known trade-off: the private key travels over the loopback admin API and lands in
Caddy's autosaved config in the config volume. Acceptable for a local development
tool, and recorded here so it is a decision rather than an accident.

## CLI

```
caddy_shack cert request <domain> [--production] [--email <addr>]
caddy_shack cert list [--json]
caddy_shack cert renew <domain> [--force]
caddy_shack cert remove <domain>
```

`cert request` front-loads every cheap failure so a rate-limited issuance is
never spent on a problem catchable beforehand:

1. Validate the domain. Reject `.test`, which belongs to the internal issuer.
2. Check docker reachable and the container running, before any ACME traffic.
3. Resolve the account email from `--email`, else the stored one, else error.
4. Print a staging banner to stderr unless `--production`.
5. Order, prompt once, poll for propagation, finalise.
6. Write to the cert store, update state, push config to Caddy.

Staging is the default because Let's Encrypt production allows only five
duplicate certificates per week for the same name set, and a human-driven DNS-01
flow has an obvious way to go wrong. `--production` is the deliberate step.

`cert list` renders domain, environment, subjects, not-after and days remaining,
with `--json` following `list` and `status`. `cert remove` follows `deregister`'s
precedent: an unknown domain exits 0 with a note on stderr. `cert renew` uses ARI
to decide whether renewal is due and no-ops when it is not; `--force` overrides.

## Errors and exit codes

Existing sentinels are reused: invalid args 2, docker unreachable 4, caddy not
running 5, not installed 6, state corrupt 7, lock contention 8. One addition,
because the exit codes are a documented contract and a failed ACME order is worth
distinguishing from a generic failure:

```
9  certificate issuance failed (ACME order, DNS validation, or rate limit)
```

That means a sentinel in `errors.go`, a case in `ExitCode`, and a line in both
the root command's help text and the README.

## Testing

**Unit**, no network and no docker:

- `certstore`: atomic writes, key mode 0600, round trip.
- `state`: a v1 file migrates to v2 without tripping the corrupt path. This one
  matters; v1 files exist in the field.
- `caddyapi.Build`: golden config proving `load_pem` is emitted and that those
  subjects are absent from the `internal` policy.
- The manual provider: prompt text into a buffer, confirmation from a reader,
  driven through `cmd.SetIn` the way the `doctor --fix` tests do.
- `app`: a fake issuer behind the `internal/acme` interface, proving the
  container check precedes issuance, that staging is the default endpoint, and
  that `renew` no-ops when not due.

**Integration**, behind the existing `//go:build integration` tag so CI's
`go test -tags integration ./test/integration/...` picks it up: end to end
against Pebble with `pebble-challtestsrv` as the DNS, alongside the current
container fixtures. This is where the hypothesis is actually tested, so the
assertions are on issuer and serial of the presented certificate, not merely on
a successful request. It also covers survival across `upgrade`.

Anything involving Let's Encrypt proper stays out of automated tests.
`alpha.arrix.dev` against real staging, then production, is a manual acceptance
step.

## Documentation

The README gains a Let's Encrypt section: the DNS-01 constraint and why, the
manual TXT workflow, staging-first, the 90-day manual renewal reality, and exit
code 9.

## Out of scope

- Automated DNS providers. The seam exists; no provider beyond manual is built.
- Per-domain narrower certificates. The cert record carries a subjects list, so
  this is additive later.
- Any change to `.test` handling, the internal CA, or the `ca` command.
- Opening ports 80 or 443 beyond loopback. DNS-01 needs no inbound reachability.

# Let's Encrypt certificates: statement of fit

Closes the record opened in
`2026-09-08-letsencrypt-certs-design.md`.

## Fit

The hypothesis held, and it held for the reason given. We believed Caddy would
serve a manually loaded certificate for a hostname while continuing to mint
internal-CA certificates for every other hostname, from a single generated
config, because automatic HTTPS skips a name that already has a loaded
certificate. It does.

What did not hold was a set of supporting beliefs the method rested on: about
how lego behaves, about our own config generation, and about whether the
planned tests were safe to run on a developer machine. The central claim
survived contact with the implementation. Most of the scaffolding around it did
not.

## Proven

**The hypothesis, by direct observation.** From one container and one config:

```
cert-e2e.invalid      issuer="Pebble Intermediate CA 426d5b"
sub.cert-e2e.invalid  issuer="Pebble Intermediate CA 426d5b"
hello.test            issuer="Caddy Local Authority - ECC Intermediate"
```

The covered domain and its subdomain present the externally issued certificate;
the `.test` domain still presents the internal CA. Caddy did not mint an
internal certificate over the loaded one.

The evidence is stronger than the assertion. `caddyapi.Build` never emits an
ACME automation policy pointing at the test ACME server, so the only two TLS
mechanisms Caddy is ever handed are internal-CA automation and `load_pem`.
Caddy has no code path by which it could have obtained that certificate itself.
A Pebble issuer can therefore only mean the loaded PEM was served.

**The wildcard covers a subdomain with no route of its own**, which was the
second thing in doubt. A backend registered directly on a subdomain is also
recognised as covered, and correctly excluded from the internal issuer.

**Certificates survive a container recreate**, restored from host storage rather
than re-issued. The same intermediate serial appears before and after, and the
post-recreate path never touches the issuer layer, so re-issuance could not have
produced the result.

**A version 1 state file migrates without data loss.** Verified against the real
state file on this machine: nine backends, migrated to version 2, none lost, no
exit code 7.

**Existing behaviour is unchanged for anyone who never runs a `cert` command.**
The only edits to pre-existing tests across the whole branch are eight
mechanical `Build(s)` to `Build(s, nil)` call-site updates and one hardcoded
schema version replaced by the constant. No assertion or golden value was
altered.

**One visit to the DNS provider per issuance.** The custom provider stays out of
lego's sequential bucket, so both challenges of a wildcard order are presented
before any validation and the operator is prompted once.

## Disproven

The hypothesis itself was not disproven. Several beliefs the method depended on
were, and they are worth recording because they clustered in one place:
**assumptions about third-party behaviour and about our own conventions, not
about Caddy.**

- **That exact-string subject matching was sufficient.** It was not. A backend
  registered directly on a subdomain of a covered domain was not recognised as
  covered, and was handed to the internal issuer despite the wildcard
  certificate already covering it. This is precisely the configuration in use on
  this machine, so the belief was not only wrong, it was wrong about the
  operator's actual setup.
- **That lego's `parallelSolve` calls `Present` from multiple goroutines.** It
  does not. It is two sequential loops, and lego's own documentation says
  "solves the challenges in series". "Parallel" there means present-all-then-
  validate-all.
- **That emitting the automation policy only when subjects exist preserved
  behaviour.** It did not. An empty state previously produced a policy with an
  empty subjects list, and the planned condition would have silently changed
  that.
- **That renewal could reuse the first-issuance install path.** It could not.
  Doing so meant a failed renewal deleted the working certificate it was
  replacing, and because missing material is skipped rather than reported, the
  next unrelated command rebuilt a config with no certificate for that hostname.
  A transient failure during a routine renewal would have become an outage at an
  arbitrary later moment.
- **That the planned tests were safe to run.** Two would have acted on the
  developer's live installation: one asserted TLS against `127.0.0.1:443`, and
  one called `app.Upgrade`, which operates on the hardcoded production container
  name. The repository's existing integration tests already avoided both, using
  a separate container name and ephemeral ports. The plan simply did not follow
  a convention that was already there.

If the design had been implemented exactly as written, the feature would have
worked in the common case and failed silently in three specific ones: a
subdomain backend, a failed renewal, and any config rebuild after a partial
write.

## Divergence

- **The desired outcome is not fully verified.** Outcome 1, that
  `alpha.arrix.dev` and `vite.alpha.arrix.dev` verify from the host with no `-k`
  and no added trust anchor, has NOT been demonstrated. It requires a real
  Let's Encrypt order with a human editing DNS at GoDaddy, which no automated
  test can stand in for. Everything the Pebble test proves is about the
  mechanism, not about the real certificate authority. This outcome remains
  owed.
- **Renewal does not use ARI.** The design says `cert renew` uses the ACME
  Renewal Information extension to decide whether renewal is due. It uses a
  fixed thirty day window instead. The README documents the actual behaviour, so
  this is a divergence between two documents rather than a claim made to users,
  but the design overstated what was built.
- **`LegoIssuer` gained two seams the design did not describe**,
  `InsecureSkipTLSVerify` and `DNSResolvers`, both inert unless set. The first
  exists because a test ACME server serves its directory with a self-signed
  certificate. The second exists because lego's propagation check consults the
  zone's real authoritative nameservers, which for a resolvable test domain
  meant querying the public internet and timing out.
- **Certificate survival across `upgrade` is tested by proxy.** The design said
  to test `caddy_shack upgrade` directly. `Upgrade` operates on the production
  container name, so the test recreates the test container and re-pushes from
  state, which exercises the same restoration path without touching the
  developer's installation.
- **Indirect dependencies moved.** Seven `golang.org/x` modules were bumped to
  clear `govulncheck`, which the pre-CI gate runs. `go-acme/lego` stayed pinned.

## Deferred

Triaged during the final review and deliberately not fixed on this branch.

- `register --domain` is never validated or lower-cased, unlike `cert` domains.
  Pre-existing, but this feature gives it teeth: an uppercase or trailing-dot
  domain in state now misses the coverage lookup and is handed to the internal
  issuer while the external certificate is loaded, producing a wrong-certificate
  warning rather than a routing oddity. Worth its own change.
- `DaysRemaining` truncates toward zero, so a certificate that expired within the
  last day reads `0`, the same as one expiring today.
- `CertList` silently zeroes `DaysRemaining` on an unparseable expiry where
  `CertRenew` treats the same condition as corrupt state.
- No test executes a `cert` subcommand's `RunE`, because that path constructs an
  App pointing at the live admin API. Closing this properly needs an injectable
  app constructor in `cmd`.
- `deregister` does not clear a certificate for the removed domain, so a
  certificate can outlive the route it was obtained for.
- Two certificates whose coverage overlaps are accepted without warning.
- `LegoIssuer.Issue` does not observe its context, so an interrupt during the
  propagation poll does not abort the order.

## A note on method

Nine defects were found across ten tasks, three of them Critical, and the
majority originated in the plan rather than in its execution. Every Critical was
silent-failure shaped: a store wipe from a bare `.` as a domain, a world-readable
private key from a leftover temporary file, and a failed renewal quietly becoming
an outage. None would have produced an error at the moment it went wrong.

The design reasoning about Caddy, the thing actually under test, held up exactly
as written. The reasoning about everything adjacent did not. That is the useful
finding here: the hypothesis was well chosen, and the confidence around it was
not calibrated to the same standard.

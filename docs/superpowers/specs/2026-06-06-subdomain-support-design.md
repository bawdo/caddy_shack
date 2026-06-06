# Subdomain support design

Date: 2026-06-06
Status: Draft, awaiting user review

## Goal

Let a registered backend serve its subdomains automatically, and let any
single subdomain be overridden to a different backend on demand.

By default, registering a backend serves every subdomain of its domain from
the same upstream: `caddy_shack register foo --port 2000` makes `foo.test`
*and* `bar.foo.test`, `anything.foo.test`, etc. all reverse-proxy to
`host.docker.internal:2000`, with no extra commands. A specific subdomain can
then be pointed somewhere else by registering it explicitly:
`caddy_shack register bar.foo --port 3000` sends `bar.foo.test` to port 3000
while the rest of `*.foo.test` still goes to port 2000.

## In scope

- Automatic wildcard routing: each registered backend serves both its exact
  domain and `*.<domain>`.
- Subdomain overrides via the existing `register` command using a dotted name
  (`register bar.foo` -> `bar.foo.test`).
- An exact override always wins over the parent's wildcard.
- `deregister` gains a `--cascade` flag to also remove subdomain backends
  under the named parent.
- TLS coverage for deep names (`bar.foo.test`) and wildcard certs
  (`*.foo.test`).

## Non-goals

- Aliasing a subdomain onto another *named* backend's upstream. "A different
  backend service" is expressed purely by `--port`. Aliasing can be added
  later without breaking anything here.
- A dedicated `subdomain` subcommand. Everything flows through the existing
  `register` / `deregister` commands.
- Storing wildcards in state. Wildcards are derived at config-build time, not
  persisted.
- A state schema change. The on-disk shape is unchanged.
- Cross-platform support. macOS only, in line with the rest of the tool.

## Chosen approach

Derived wildcards over a flat state model (Approach A of the brainstorm).
State stays the flat `map[name]Backend` it is today; nothing about subdomains
is persisted. The wildcard is generated when the Caddy config is built. An
override is just an ordinary backend whose name (and therefore domain)
contains dots. The parent/child relationship is implicit, inferred from
domain strings, not stored.

Rejected alternatives:

- **Explicit wildcard rows in state.** Mirrors Caddy 1:1 but needs a schema
  bump and pairs every backend with a second row to keep in sync. Buys little.
- **Parent/child nested model.** Makes cascade trivial but cuts against the
  flat map and bundles objects we would otherwise normalise. Overkill.

## Design

### State and validation

No schema change. `state.Backend` and the flat `map[name]Backend` are
unchanged. The only change is the name validator.

Today:

```
^[a-z0-9][a-z0-9-]{0,30}$
```

Becomes a dotted sequence of that same label pattern:

```
^[a-z0-9][a-z0-9-]{0,30}(\.[a-z0-9][a-z0-9-]{0,30})*$
```

`foo` and `bar.foo` both validate; each label keeps the existing rules. The
`name -> <name>.test` derivation is unchanged, so `bar.foo` -> `bar.foo.test`.
`--domain` still works as the escape hatch for non-`.test` domains.

### Routing (`caddyapi.Build`)

For each backend with domain `D` and upstream `U`, emit two routes instead of
one:

- exact: `match host=[D]` -> reverse_proxy `U`
- wildcard: `match host=[*.D]` -> reverse_proxy `U`

Ordering is the new invariant: **all exact routes first (sorted by domain),
then all wildcard routes (sorted by domain).**

Because a Caddy `*` matches exactly one label, a request such as
`bar.foo.test` matches both the exact `bar.foo.test` route and the
`*.foo.test` wildcard. Putting every exact route ahead of every wildcard
route guarantees the explicit override wins (reverse_proxy is a terminal
handler). Wildcards never overlap each other (single label), so their
relative order matters only for deterministic golden-file output.

Worked routing examples (backends: `foo` -> 2000, `bar.foo` -> 3000):

- `foo.test` -> matches exact `foo.test` only -> port 2000
- `bar.foo.test` -> matches exact `bar.foo.test` (first) -> port 3000
- `other.foo.test` -> matches `*.foo.test` only -> port 2000
- `x.bar.foo.test` -> matches `bar.foo`'s own `*.bar.foo.test` -> port 3000

### TLS

The current static `*.test` policy subject cannot cover `bar.foo.test` or a
`*.foo.test` certificate (wildcards are single-label). Replace it with a
derived subject list: the sorted union over all backends of `{D, *.D}`, still
using the internal issuer. Certificates are then minted only for names
actually registered, and deep names are covered.

**To verify during implementation, not assume:** that Caddy's internal CA
issues the `*.foo.test` wildcard certificate cleanly. If it does not, the
fallback is on-demand TLS, which is acceptable here because the listeners and
admin API are loopback-only. This will be confirmed against a running
container before the approach is locked.

### CLI UX

**`register`** needs no new flags. The dotted name flows through the existing
path:

```
caddy_shack register bar.foo --port 3000      # bar.foo.test -> port 3000
```

Existing idempotency and domain-conflict checks apply unchanged; both key off
name and domain, which are well-defined for dotted names. Help text and the
README gain a short note: registering `foo` automatically serves
`*.foo.test`; register a dotted name to override one subdomain.

**`deregister`** gains one flag:

```
caddy_shack deregister foo            # removes foo + its *.foo.test wildcard;
                                      # bar.foo.test (if registered) survives
caddy_shack deregister foo --cascade  # also removes every backend whose
                                      # domain ends in ".foo.test"
```

Default behaviour leaves children in place. `--cascade` finds children by
domain suffix (`.` + the parent's domain) and removes them in the same
locked, single-`Build`-and-`Load` transaction. When children exist and
`--cascade` is not given, deregister still succeeds for `foo` but prints a
one-line stderr note: "N subdomain backend(s) still registered under foo.test
(use --cascade to remove)."

**`list`** stays one row per registered backend. The implicit `*.<domain>` is
not a separate row; a short footer notes that each backend also serves
`*.<domain>` unless a more specific override exists. The `--json` shape is
unchanged.

### Edge and conflict rules

- **Override without parent:** `register bar.foo` when `foo` is not registered
  is allowed. It is a normal independent backend (exact `bar.foo.test` plus
  its own `*.bar.foo.test` wildcard). No error.
- **Domain conflict:** unchanged. Two backends cannot own the same exact
  domain. Distinct names mapping to the same domain still conflict.
- **Wildcard shadowing:** a registered override that shadows a parent's
  wildcard is the intended behaviour, not a conflict, so no error.
- **Depth:** `*` is single-label, so `x.bar.foo.test` is served by `bar.foo`'s
  own `*.bar.foo.test` wildcard, not `foo`'s. No special handling.

## Testing

- **`caddyapi` golden files:** update `testdata` for the doubled routes,
  exact-before-wildcard ordering, and derived TLS subjects. Add cases: a
  single backend (exact + wildcard), an override (ordering proves exact
  wins), and a deep override.
- **`app` unit tests:** dotted-name validation (accept and reject), register
  idempotency with dotted names, cascade vs non-cascade deregister (suffix
  matching plus the stderr note), and override-without-parent.
- **Integration (`test/integration`):** register a parent, `curl` a random
  `*.parent.test` and confirm it hits the parent's upstream; register an
  override and confirm it hits a different upstream; deregister the parent and
  confirm the override survives, then `--cascade` and confirm it is gone. Add
  a TLS check that the wildcard certificate is actually served; this is where
  the Caddy internal-CA caveat is verified for real.

## Open question (resolved)

- Whether the internal CA issues `*.<domain>` wildcard certs cleanly, or
  whether on-demand TLS is needed instead (see TLS section).
- **Resolved:** the internal CA serves `*.foo.test` wildcard certs cleanly. The
  `TestSubdomainE2EWildcardAndOverride` integration test hits `random.foo.test`,
  `foo.test`, and the `bar.foo.test` override over HTTPS against a real Caddy
  container and all succeed with no TLS handshake errors. On-demand TLS was not
  needed; the derived `{D, *.D}` subject list with the internal issuer is
  sufficient.

# Kaizen: lean and secure, closeout

Statement of fit for the branch opened in
`2026-10-27-kaizen-lean-and-secure-design.md`. The opening statement is left
as written; where it was wrong, this says so.

## Fit

Two of the three hypotheses held and were acted on as planned. The third
(that Caddy's `enforce_origin` was a cheap mitigation for sibling-container
access to the admin API) was wrong on method, was first withdrawn, and was
then reinstated against a different attacker after the final security
review. The branch delivers every review finding as a commit or a recorded
ruling, with 29 signed commits across nine tasks and one fix wave, and the
race suite and lint clean at every merge.

## Proven

- The ten inline lock and load sites did collapse onto the two helpers with
  no test weakened. `state.AcquireLock` now appears once in `internal/app`
  (`reconcile.go`), and `state.Load(` only there and in the doctor checks
  that discard the error into a SKIP. The new contention test fails on the
  old code in 5s and passes in 50ms.
- `issueAndInstall` dropped to 39 lines by delegating to `commitWith`, and
  all 47 certificate tests stayed green without an edit. The rollback
  ordering (restore material, then rebuild the previous config) is pinned
  by an existing behavioural test, and the fix wave added a pre-order check
  so an unreadable leftover directory never spends a Let's Encrypt
  issuance.
- `caddyapi.Build` is the only builder, the config builder split into
  `types.go` and `lan.go` with every golden fixture byte-identical until the
  admin block was deliberately changed, and `docker.Client` lost
  `NetworkAttached`.
- A container in Docker's `restarting` state is now reported, refused,
  stopped and recreated honestly across every switch site, and the doctor
  recreate fix for a crash-looping forwarder or relay actually recreates
  it. Both were gaps the parallel per-task reviews caught before merge.

## Disproven

- **Hypothesis 3, on method.** The opening statement claimed a sibling
  sending `Host: caddy_shack:2019` would be refused by `enforce_origin`.
  Caddy's `admin.go` skips the Host check entirely on a wildcard listener,
  and `enforce_origin` is an Origin-header check that any non-browser
  client can satisfy and that caddy_shack's own client did not send. The
  change as specified would have 403'd every admin call and stopped no
  sibling. The security reviewer then showed the setting does stop a
  browser DNS-rebinding attack, which is the attacker it exists for, so it
  was applied with a fixed `Origin` header in the client. Sibling access is
  documented, not mitigated, and the proper fix (unix-socket or mTLS remote
  admin) is recorded for later.
- **"The line count of `internal/app` falls."** It rose from 4803 to 4822
  non-test lines. The helper consolidation and command-layer trims removed
  about 140 lines, and the restarting-state handling, the cert pre-check
  and the trust-store rework added them back. Across the whole module the
  production Go diff is +656 / -597. Lean was achieved as fewer
  implementations of each idea, not as fewer lines.
- **The docker.Client count in the desired outcome.** The interface had 17
  methods, not 16, so it now has 16, not 15. The one-method reduction
  landed as intended; the number in the spec was a miscount at writing time.

## Divergence

- Task 6 ran twice: once as README-only after the implementer disproved the
  brief, then its code steps were reinstated in the fix wave for the browser
  vector, together with moving the Caddy container off the default bridge
  onto the `caddy_shack` network. Existing installs are told by `start` to
  run `install`, which recreates the container keeping volumes, the local
  CA and backends.
- The fix wave implementer could not run the port-binding tests or sign
  commits inside its sandbox; the controller ran the full verification on
  the staged tree and again at HEAD, and the implementer signed with the
  sandbox disabled, as every other task did.
- The first integration run against Docker found that a resumed
  pre-origins autosave 403s a client that sends `Origin`, which would have
  locked out every existing install. Fixed with a one-retry fallback in the
  client (a request without `Origin` when Caddy refuses the origin) and
  pinned by an integration test that keeps the old admin block
  (`restart_drift_e2e_test.go`).
- Deferred, with reasons, for the owner:
  - Doctor's trust-store fix now trusts the CA served by the admin API
    rather than the volume file. Same attacker already holds the root key,
    so the exposure is unchanged, but `--fix --yes` could print the
    SHA-256 it is about to trust and refuse to auto-apply.
  - The trust-store WARN ("no root CA yet") may be unreachable against a
    fresh `caddy:2` if `/pki/ca/local` answers non-2xx before the PKI app
    exists. Needs one check against a real container.
  - Whether `host.docker.internal:2019` reaches the admin API from any
    container on Docker Desktop and OrbStack is stated as likely in the
    README with a one-line check; it was not verified here.
  - The integration suite (real Caddy with origin enforcement, real Docker
    networking) was not run in this session; CI runs it.
  - Cosmetic: `noteStaleCaddy` and two README sentences still say
    "networks are kept" where the default bridge is now dropped on purpose.

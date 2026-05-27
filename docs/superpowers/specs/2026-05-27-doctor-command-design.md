# Doctor command design

Date: 2026-05-27
Status: Draft, awaiting user review

## Goal

Give the user a single command that diagnoses every prerequisite CaddyShack
relies on - DNS, the trust store, Docker, the managed container, state file
integrity, config drift, and backend reachability - and, on request, repairs
what can be repaired automatically.

The README already promises this for DNS: "A future `caddy_shack doctor`
command will detect and fix missing DNS configuration automatically." This
design extends that promise to every prerequisite the MVP currently expects
the user to set up by hand.

## In scope

- `caddy_shack doctor` - run all checks, report a per-check verdict, exit 0
  if healthy or exit 1 if anything failed.
- `caddy_shack doctor --json` - same checks, machine-readable output.
- `caddy_shack doctor --no-backends` - skip the per-backend probes.
- `caddy_shack doctor --fix` - report, then interactively prompt y/N for each
  available fix.
- `caddy_shack doctor --fix --yes` - apply every available fix without
  prompting.
- Mutual exclusion: `--json` cannot be combined with `--fix`; `--yes` without
  `--fix` is an error.

## Non-goals

- Cross-platform support. macOS only, in line with the rest of the MVP.
- Performance tuning of probes (parallelism, caching). All probes run
  sequentially; total runtime is in the order of seconds.
- Custom per-check filtering beyond `--no-backends`. If `--only` / `--skip`
  become useful later, they can be added without breaking the JSON schema.
- Repair of broken backends. Doctor reports unreachable upstreams but does
  not try to start the user's apps.
- Caching of `sudo` credentials or scripted password entry. The user types
  their password into the standard macOS `sudo` prompt.

## CLI surface

```
caddy_shack doctor                  # report only; exit 1 on any FAIL
caddy_shack doctor --json           # machine-readable; same exit semantics
caddy_shack doctor --no-backends    # skip backend reachability probes
caddy_shack doctor --fix            # report + interactive fixes
caddy_shack doctor --fix --yes      # report + non-interactive fixes
caddy_shack doctor --yes            # ERROR: "--yes requires --fix" (exit 2)
caddy_shack doctor --json --fix     # ERROR: "--json cannot be combined with --fix" (exit 2)
```

Exit codes (extend the existing contract, no new sentinels needed):

| Code | Meaning in `doctor` |
|------|----------------------|
| 0 | All checks PASS, or all attempted fixes succeeded |
| 1 | Report mode: at least one FAIL. Fix mode: at least one fix failed |
| 2 | Flag misuse (`--yes` without `--fix`, or `--json` + `--fix`) |
| 4 | Docker daemon unreachable AND the doctor entrypoint itself errored before any check could run (rare) |
| 7 | State file corrupt AND the doctor entrypoint itself errored before any check could run (rare) |
| 8 | State lock held when drift fix tries to acquire it |

Codes 4, 7, 8 are preserved for cases where the `Doctor` entrypoint cannot
even start. Within the report, the same underlying conditions surface as
FAIL/SKIP check results rather than aborting; doctor's value comes from
producing a full report whenever it can.

## Check catalogue

Eight checks, run in order so each can short-circuit downstream noise.

| # | Name | Verifies | Fix |
|---|------|----------|-----|
| 1 | `docker-daemon` | `docker info` succeeds | none (start Docker Desktop manually) |
| 2 | `port-availability` | 127.0.0.1 ports 80, 443, 2019 are free or already held by `caddy_shack` | none (cannot kill foreign processes) |
| 3 | `container-health` | Container exists, is running, admin API reachable | install / start (reuses `App.Install`, `App.Start`) |
| 4 | `dns-test-tld` | `dscacheutil -q host -a name <random>.test` resolves to 127.0.0.1 | install dnsmasq, write `caddy_shack.conf`, write `/etc/resolver/test`, restart dnsmasq (requires sudo) |
| 5 | `trust-store` | Caddy root CA (SHA-256, read from container) is in `/Library/Keychains/System.keychain` | `security add-trusted-cert` (requires sudo) |
| 6 | `state-integrity` | `state.json` parses; lock is not currently held | none for held lock (transient by definition); corrupt state is manual |
| 7 | `config-drift` | Live Caddy `apps.http.servers.srv0.routes` deep-equals routes regenerated from `state.json` | re-`POST /load` from state (takes the state lock) |
| 8 | `backends` (`--no-backends` to skip) | Per backend, `docker exec caddy_shack nc -z -w 3 <upstream-host> <upstream-port>`; fallback to `wget --spider` if `nc` is absent | none |

Each check produces a `CheckResult` with severity PASS, WARN, FAIL, or SKIP.

- **PASS**: check passed.
- **WARN**: informational issue that does not block normal use (e.g. drift
  detected but Caddy is still routing). Counted separately; does not affect
  exit code on its own.
- **FAIL**: user is hitting this now. Drives exit 1 in report mode.
- **SKIP**: check could not run because a prerequisite check failed (e.g.
  `backends` SKIPs when `container-health` FAILs) or because the user passed
  `--no-backends`.

### Check details

**`dns-test-tld`.** Functional probe is `dscacheutil -q host -a name
<12-random-chars>.test`, parsed for `ip_address: 127.0.0.1`. A random
subdomain avoids false positives from stale `/etc/hosts` entries on a
specific name. Diagnostic sub-probes (dnsmasq installed, conf file present,
resolver entry correct, dnsmasq service running) populate `detail` so the
user can see what is broken even when the fix is unavailable.

**`trust-store`.** Read the root cert via `docker exec caddy_shack cat
/data/caddy/pki/authorities/local/root.crt`, compute its SHA-256
fingerprint, then run `security find-certificate -a -Z
/Library/Keychains/System.keychain` and look for that fingerprint. Name-based
matching is unreliable because Caddy rotates the CA every year.

**`state-integrity`.** Parse `state.json`; if it fails to parse, FAIL with
the parser error in detail. Then attempt a non-blocking flock on the lock
file; if it is currently held, the result is WARN `register/deregister in
progress` (not FAIL, since flock is transient and self-cleaning). PASS
otherwise. No state file at all is PASS - that just means nothing has been
registered yet.

**`config-drift`.** Compare only the `apps.http.servers.srv0.routes`
subtree of the live config against routes regenerated from `state.json`.
Caddy normalises and defaults the full config on `POST /load`, so a full
deep-equal would false-positive constantly. The routes subtree is the part
CaddyShack actually owns. The fix is `caddyapi.Load(ctx, regenerated)`,
guarded by the state lock to avoid racing a concurrent `register`.

**`backends`.** Inside the container, try `nc -z -w 3 <host> <port>` first.
If `nc` is missing from the `caddy:2` image, fall back to `wget -q --spider
--tries 1 --timeout 3 http://<host>:<port>/`. Any TCP connect or any HTTP
response (2xx through 5xx) is PASS; refused or timed out is FAIL. We treat
5xx as PASS because reachability is the question - the upstream is alive,
the app is just broken.

## Output formats

### Human (default)

```
caddy_shack doctor

[PASS] docker-daemon       Docker reachable
[PASS] port-availability   80, 443, 2019 free or held by caddy_shack
[PASS] container-health    running; admin API responding
[FAIL] dns-test-tld        .test does not resolve to 127.0.0.1
       dnsmasq: not installed
       /etc/resolver/test: missing
       fix: install dnsmasq + write resolver entry (requires sudo)
[FAIL] trust-store         Caddy root CA not trusted in System keychain
       expected SHA-256: 7f3a...
       fix: security add-trusted-cert (requires sudo)
[PASS] state-integrity     state.json parses; lock free
[WARN] config-drift        live Caddy routes differ from state.json
       fix: re-POST /load from state
[SKIP] backends            skipped (--no-backends)

3 issues found (2 FAIL, 1 WARN). Run with --fix to remediate.
```

`--fix` mode replaces the trailing line with per-fix prompts:

```
[FAIL] dns-test-tld ...
Apply fix? (install dnsmasq + write /etc/resolver/test, requires sudo) [y/N]: y
  -> brew install dnsmasq ... ok
  -> sudo tee /etc/resolver/test ... ok
[FIXED] dns-test-tld
...
2 fixed, 0 skipped, 0 failed.
```

`--fix --yes` skips prompts and prints `[FIXED]` / `[FIX-FAILED]` lines as
each fix runs.

### JSON (`--json`)

```json
{
  "schema_version": 1,
  "overall": "fail",
  "checks": [
    {
      "name": "dns-test-tld",
      "status": "fail",
      "summary": ".test does not resolve to 127.0.0.1",
      "detail": ["dnsmasq: not installed", "/etc/resolver/test: missing"],
      "fix_available": true,
      "fix_requires_sudo": true
    }
  ],
  "summary": { "pass": 4, "warn": 1, "fail": 2, "skip": 1 }
}
```

- `overall` is `pass` if no FAIL, `warn` if any WARN but no FAIL, `fail` if
  any FAIL.
- `--json` and `--fix` are mutually exclusive, so the JSON shape never needs
  to carry fix results.
- No timestamps, no durations; keep the surface small until someone needs
  more.

## Internal architecture

Approach A from brainstorming: a flat orchestrator in `internal/app/`,
mirroring how `Status` is already structured.

```
internal/app/doctor.go              <- new
  type Severity      // pass | warn | fail | skip
  type CheckResult   // name, severity, summary, detail, fix
  type FixAction     // label, requiresSudo, apply func(ctx) error
  type DoctorReport  // overall, []CheckResult, summary counts
  type DoctorOptions // SkipBackends bool

  func (a *App) Doctor(ctx, opts) (DoctorReport, error)
    runs each check; honours ctx cancellation between checks

  func (a *App) ApplyFix(ctx, *CheckResult, prompt PromptFunc) (FixOutcome, error)
    cmd layer calls this once per FAIL/WARN result that carries a fix

internal/app/doctor_checks.go       <- new
  checkDockerDaemon, checkPortAvailability, checkContainerHealth,
  checkDNS, checkTrustStore, checkStateIntegrity, checkConfigDrift,
  checkBackends
  each is func(ctx, *App) CheckResult

cmd/doctor.go                       <- new
  newDoctorCmd: flags --json, --fix, --yes, --no-backends
  renderDoctorHuman / renderDoctorJSON
  promptYesNo (skipped when --yes)
```

### Existing collaborators reused

- `internal/docker` for `DaemonReachable`, `ContainerState`. **New method**
  `Exec(ctx, name string, argv []string) ([]byte, error)` backs the backend
  probe and the cert read; matches the existing thin-wrapper style. Fake in
  `internal/docker/fake.go` grows a matching method.
- `internal/caddyapi` for `Reachable` and `Load`. **New method**
  `GetConfig(ctx) ([]byte, error)` for the drift comparison.
- `internal/state` for `Load`. **New helper** `LockHeld(lockPath string)
  (bool, error)` attempts a non-blocking `flock(LOCK_EX|LOCK_NB)`; returns
  true if the lock could not be acquired. flock(2) auto-releases when the
  holding process exits, so there is no "stale lock" condition to detect;
  a held lock is always a live concurrent register/deregister. Used by
  `state-integrity`.
- `internal/caddyapi/config.go` builder for drift regeneration. The
  routes-only subtree extraction lives in `doctor_checks.go` (it is doctor's
  comparison choice, not a caddyapi concern).

### No new package

Everything sits in `internal/app/` next to `status.go` and `install.go`. If
`doctor_checks.go` grows past one screen the next refactor promotes to
`internal/app/doctor/`. Premature factoring is not warranted for v1.

### Fix shape

```go
type FixAction struct {
    Label         string                       // human-readable description shown in the prompt
    RequiresSudo  bool                         // surfaced in JSON and in the prompt
    Apply         func(ctx context.Context) error
}
```

Sudo-requiring fixes invoke `sudo` themselves via `os/exec`. The framework
does not escalate, so the prompt label can honestly describe what will run.
macOS handles the TTY password prompt; we inherit stdin/stdout/stderr for
the duration. In `--yes` mode if `sudo -n true` fails we mark the fix
`[FIX-FAILED]` with `sudo password required, re-run interactively`. We do
not cache or read passwords.

### Cancellation

Every check accepts `ctx`. `Doctor` checks `ctx.Err()` between checks and
returns a partial report on cancellation. `--fix` mode does the same between
fixes - no half-applied fix is left unreported.

## Error handling

A check never returns an error to its caller. Errors are encoded into the
`CheckResult` (status=FAIL, summary describes the failure, detail carries
the underlying message). This keeps `Doctor` simple and means one broken
probe never aborts the whole report. The only path that returns an error
from `Doctor` itself is `ctx.Err()` after cancellation.

`ApplyFix` returns an error only when the fix itself errored. The cmd layer
collects these into a failed-fix tally; doctor's process exit is 1 if any
fix failed.

## Testing strategy

- **Unit (table-driven, fast):** each `check*` function gets tests with a
  fake `docker.Client`, a fake `caddyapi` client (interface promoted from
  the existing struct), and a temp-dir state path. Fakes return scripted
  responses; assertions are on the returned `CheckResult` and (for fixes)
  the calls the `Apply` func makes.
- **Cmd layer:** `cmd/doctor_test.go` covers `renderDoctorHuman` and
  `renderDoctorJSON` against fixed `DoctorReport` values, matching the
  pattern already used by `cmd/status_test.go`. JSON comparisons parse and
  compare maps rather than byte-for-byte.
- **Integration (docker-backed, build-tagged like the existing E2E suite):**
  - happy path: run `doctor` after `install`, assert everything PASSes.
  - stopped container: stop, run `doctor`, assert correct FAILs.
  - dead backend: register a backend pointing at a closed port, assert
    `backends` FAILs.
- **DNS and trust-store checks are not covered by integration tests** -
  they touch system state the test cannot safely mutate. Unit tests cover
  parsing of `dscacheutil` and `security find-certificate -Z` output via
  captured fixture strings; the e2e behaviour is verified by hand.
- **Fix paths are unit-tested via fake side-effect recorders.** No fix's
  `Apply` runs against a real system in tests.

## Edge cases

- **`doctor` before `install`:** `docker-daemon` PASS,
  `port-availability` PASS (ports are free), `container-health` FAIL (`not
  installed`), `dns-test-tld` and `trust-store` still run because they don't
  need the container (trust-store SKIPs with `container not running` because
  it needs to read the cert from the container), `state-integrity` PASS (no
  state file is fine), `config-drift` SKIP (`container not running`),
  `backends` SKIP (`container not running`).
- **Docker Desktop quit:** `docker-daemon` FAIL; every later check that
  needs Docker SKIPs with `docker daemon unreachable`. Doctor still reports;
  it does not return `ErrDockerUnreachable` from the entrypoint.
- **Caddy admin reachable but `GET /config/` returns 5xx:** `container-health`
  reports admin reachable (existing behaviour); `config-drift` FAILs with the
  5xx body in detail.
- **No backends registered:** `backends` PASS with summary `no backends
  registered`.
- **Trust-store check sees no cert at `/data/caddy/pki/...`:** Caddy
  provisions the root CA lazily on first TLS handshake. If the file is
  absent we report WARN `no root CA yet (register a backend to trigger
  provisioning)` rather than FAIL.
- **dnsmasq installed via something other than brew:** the conf-file
  sub-probe looks under `$(brew --prefix)/etc/dnsmasq.d/`. If `brew --prefix`
  fails we fall back to checking `/opt/homebrew/etc/dnsmasq.d/` and
  `/usr/local/etc/dnsmasq.d/`. If neither path exists we WARN `dnsmasq
  appears installed but not via brew - the auto-fix only manages brew
  installs`.

## Open decisions deferred

- `--only <check>` and `--skip <check>` for arbitrary filtering. The
  `--no-backends` flag covers the only filter we currently need.
- Parallel probe execution. Sequential keeps the output deterministic and
  total runtime is already small.
- Caching the last doctor run. Probably belongs in a `status --verbose` if
  we ever want it.

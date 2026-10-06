# LAN Exposure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a registered backend carry an optional LAN-facing name, served to other LAN devices over HTTPS through a separate haproxy forwarder container, while `.test` names stay loopback-only and unaffected by loss of the LAN IP.

**Architecture:** A stateless `caddy_shack_lan` haproxy container publishes `<LAN IP>:80/443` and forwards raw TCP with PROXY protocol v2 over the `caddy_shack` Docker network to two new, unpublished Caddy servers in the main container (`srv-lan` on `:8443`, `lan-http` on `:8080`) that hold LAN names only. The main Caddy container spec never changes. LAN settings live in `state.json` (schema 2); the IP is discovered with the Go standard library or pinned by the user.

**Tech Stack:** Go 1.25, cobra, Docker CLI (via `internal/docker`), Caddy 2 admin API JSON, `haproxy:3.2-alpine`.

**Spec:** `docs/superpowers/specs/2026-10-06-lan-exposure-design.md`. Read it before starting any task; this plan argues from it and does not repeat its rationale.

## Global Constraints

- With LAN mode absent or disabled, `caddyapi.BuildWith` output is byte for byte unchanged: every existing golden file in `internal/caddyapi/testdata/` passes untouched.
- `caddyContainerSpec` (`internal/app/install.go:17`) is not modified by any task.
- State schema: `SchemaVersion = 2`; version 1 files load and upgrade in memory; any other version is `*ErrCorrupt`.
- Forwarder: container name `caddy_shack_lan`, image `haproxy:3.2-alpine`, listens on container ports `8443` and `8080` (the image runs as a non-root user and cannot bind below 1024).
- Caddy LAN servers: `srv-lan` listens `:8443`, `lan-http` listens `:8080`. Neither is published by the main container.
- Exit code `10` = LAN mode not enabled (`ErrLANDisabled`). LAN down during `start`, `restart`, `sync`, `install`, `upgrade` is a notice with exit 0. A failed bind during `lan enable` exits 1.
- LAN names and the suffix must not be `test` or end in `.test`.
- Australian English in all user-facing text, comments and docs. No em dashes or en dashes anywhere.
- Commits: GPG signed (`git commit -S`), past-tense subject of 60 characters or fewer, no attribution trailers of any kind. GPG signing needs the sandbox lifted (`~/.gnupg` is outside it); if signing still fails, stop and ask for the key to be refreshed.
- Unit tests: `go test -race ./...`. Integration tests are tag-gated and run locally only: `go test -tags integration ./test/integration/...`. CI runs unit tests on `macos-latest` only.
- Run `make pre-ci` before each commit (gofmt, vet, lint, test).

## Review Focus

1. **A backend registered with `--domain` set to a LAN-looking name** (e.g. `--domain gamma.arrix.home.lan` with no `--lan`): the name must still be loopback-only and must conflict with any other backend's `lan_domain`. Test owned by Task 7.
2. **`lan enable` run again with a different `--suffix`** while backends already carry LAN names: existing `lan_domain` values must not change; only new `--lan` registrations use the new suffix. Test owned by Task 6.
3. **`lan disable` then `register --lan`**: must exit 10, and must not drop the `lan_domain` of other backends. Test owned by Task 7.
4. **State written by this branch read back by an older binary**: an older binary sees schema 2 and reports corrupt (exit 7). Expected and accepted; covered by the README upgrade note in Task 12, not a test.
5. **Wildcard LAN subdomain**: `bar.gamma.arrix.home.lan` must reach gamma's upstream via `srv-lan`, and `bar.gamma.arrix.test` must not be reachable via the LAN listener. Test owned by Task 11.

---

### Task 1: State schema 2

**Files:**
- Modify: `internal/state/state.go`
- Test: `internal/state/state_test.go`

**Interfaces:**
- Produces:
  ```go
  const SchemaVersion = 2
  type LANMode string
  const (LANModeDiscovered LANMode = "discovered"; LANModePinned LANMode = "pinned")
  type LAN struct {
      Enabled bool    `json:"enabled"`
      IP      string  `json:"ip"`
      Mode    LANMode `json:"mode"`
      Suffix  string  `json:"suffix"`
  }
  // Backend gains:
  LANDomain string `json:"lan_domain,omitempty"`
  // State gains:
  LAN *LAN `json:"lan,omitempty"`
  func (s *State) LANEnabled() bool // s.LAN != nil && s.LAN.Enabled
  ```
  `Clone` deep-copies `LAN`.

- [ ] **Step 1: Write the failing tests**

`TestLoadUpgradesSchemaV1`: write `{"schema_version":1,"backends":{"a":{"domain":"a.test","upstream":"host.docker.internal:3000","registered_at":"x"}}}`; `Load` returns `SchemaVersion == 2`, backend `a` intact, `LAN == nil`. `Save` then writes `"schema_version": 2`.
`TestLoadRejectsUnknownSchema`: version `3` gives `IsCorrupt(err) == true`.
`TestRoundTripLAN`: save a state with `LAN{Enabled:true, IP:"192.168.1.12", Mode:LANModeDiscovered, Suffix:"arrix.home.lan"}` and a backend with `LANDomain:"gamma.arrix.home.lan"`; load gives an equal value.
`TestCloneDeepCopiesLAN`: mutating `clone.LAN.IP` leaves the original unchanged.
`TestSaveOmitsLANWhenNil`: saved JSON contains neither `"lan"` nor `"lan_domain"`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/state/ -run 'SchemaV1|UnknownSchema|RoundTripLAN|DeepCopiesLAN|OmitsLAN' -v`
Expected: FAIL (compile errors on `LAN`, `LANDomain`).

- [ ] **Step 3: Implement**

`Load` accepts `1` or `SchemaVersion`, sets `s.SchemaVersion = SchemaVersion` after a successful v1 parse (the v1 shape is a subset of v2), rejects anything else. Update existing tests that assert `schema_version` 1 on save.

- [ ] **Step 4: Run all state tests**

Run: `go test -race ./internal/state/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/state
git commit -S -m "Added LAN settings to state schema version 2"
```

---

### Task 2: Port bindings in container drift

**Files:**
- Modify: `internal/docker/docker.go` (`ContainerInfo`), `internal/docker/cli.go` (`parseContainerInfo`), `internal/docker/fake.go`, `internal/app/relay.go` (`specDrift`)
- Test: `internal/docker/cli_test.go`, `internal/app/relay_test.go` or the existing `specDrift` test file

**Interfaces:**
- Produces: `ContainerInfo.Ports []PortMapping`, sorted by `HostAddr`, `HostPort`, `ContainerPort`, parsed from `HostConfig.PortBindings` (`{"8443/tcp":[{"HostIp":"192.168.1.12","HostPort":"443"}]}`). `docker.Fake` records the spec's `PortMappings` into `Ports` on `CreateAndStart`. `specDrift` reports `"port bindings differ"` (detail lists got and want) when `len(spec.PortMappings) > 0` and the sorted sets differ.

- [ ] **Step 1: Write the failing tests**

`TestParseContainerInfoPorts`: inspect JSON with two bindings gives both `PortMapping`s, sorted.
`TestSpecDriftPorts`: spec `192.168.1.12:443->8443` against info `192.168.1.40:443->8443` gives one drift entry containing `"port bindings"`; identical sets give none; a spec with no `PortMappings` never reports port drift.
`TestCaddySpecHasNoPortDriftAgainstItself`: `specDrift(caddyContainerSpec(...), infoFromSpec)` is empty (guards existing installs from a spurious recreate).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/docker/ ./internal/app/ -run 'Ports|PortDrift' -v`
Expected: FAIL.

- [ ] **Step 3: Implement as described in Interfaces**

- [ ] **Step 4: Run the full suite**

Run: `go test -race ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/docker internal/app/relay.go internal/app/*_test.go
git commit -S -m "Compared port bindings when checking container drift"
```

---

### Task 3: LAN IP discovery package

**Files:**
- Create: `internal/lanip/lanip.go`, `internal/lanip/system.go`
- Test: `internal/lanip/lanip_test.go`

**Interfaces:**
- Produces:
  ```go
  type Iface struct { Name string; Up, Loopback bool; Addrs []netip.Prefix }
  type Source interface {
      Interfaces() ([]Iface, error)
      RouteSource() (netip.Addr, error) // default-route source address
  }
  func System() Source                      // net.Interfaces + UDP dial to 192.0.2.1:9 (nothing sent)
  type Result struct { IP netip.Addr; Note string } // Note non-empty when the default route was not used
  func Discover(src Source) (Result, error)
  func Present(src Source, ip netip.Addr) (bool, error)
  func Candidates(src Source) ([]Candidate, error)
  type Candidate struct { Iface string; IP netip.Addr }
  var ErrNoCandidate = errors.New("no private IPv4 address on a physical interface")
  type AmbiguousError struct { Candidates []Candidate } // Error() lists them and suggests --ip
  ```
  Candidate rule: interface up, not loopback, name not prefixed by any of `utun tun wg tailscale docker br- veth bridge vmnet awdl llw`, address IPv4 in `10.0.0.0/8`, `172.16.0.0/12` or `192.168.0.0/16`. `Discover`: route source wins if it is a candidate; else exactly one candidate wins with `Note` naming it; else `ErrNoCandidate` or `*AmbiguousError`. `Present` checks every interface address (not only candidates), so a pinned IP on any interface counts.

- [ ] **Step 1: Write the failing tests** (table-driven with a fake `Source`)

- default route `192.168.1.12` on `en0`: IP `192.168.1.12`, empty `Note`
- default route `10.8.0.2` on `utun4`, `en0` has `192.168.1.12`: IP `192.168.1.12`, `Note` contains `en0`
- two candidates (`en0` 192.168.1.12, `en7` 192.168.1.50), route source on `utun4`: `*AmbiguousError` with both
- only `lo0` and `docker0`: `ErrNoCandidate`
- `bridge100` with `192.168.64.1` is never a candidate
- public address `203.0.113.5` on `en0` is never a candidate
- `Present(192.168.1.12)` true; `Present(192.0.2.1)` false

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/lanip/ -v`
Expected: FAIL (package does not exist).

- [ ] **Step 3: Implement as described in Interfaces**

- [ ] **Step 4: Run them to verify they pass**

Run: `go test -race ./internal/lanip/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lanip
git commit -S -m "Added LAN IP discovery package"
```

---

### Task 4: Caddy config for the LAN servers

**Files:**
- Modify: `internal/caddyapi/config.go`
- Create: `internal/caddyapi/testdata/lan_enabled.json`, `internal/caddyapi/testdata/lan_enabled_no_lan_names.json`
- Test: `internal/caddyapi/config_test.go`

**Interfaces:**
- Consumes: `state.State.LANEnabled()`, `state.Backend.LANDomain` (Task 1).
- Produces: exported constants `LANHTTPSListenPort = 8443`, `LANHTTPListenPort = 8080`, server names `"srv-lan"` and `"lan-http"`. `BuildWith` signature unchanged; LAN output is driven by state alone.

Config rendered when `s.LANEnabled()`; LAN names are the sorted non-empty `LANDomain` values:

- `srv-lan`: `listen [":8443"]`; `listener_wrappers` `[{"wrapper":"proxy_protocol","fallback_policy":"require"},{"wrapper":"tls"}]`; routes from the same exact-then-wildcard builder as `srv0` (extract it into one function used by both) followed by a terminal catch-all route `{"handle":[{"handler":"static_response","abort":true}]}`; `tls_connection_policies` `[{"match":{"sni":[<each LAN name and *.<name>>]}}]`; `strict_sni_host: true`; `automatic_https: {"disable_redirects": true}`.
- `lan-http`: `listen [":8080"]`; `listener_wrappers` `[{"wrapper":"proxy_protocol","fallback_policy":"require"}]`; a route matching host on every LAN name and wildcard with `static_response` status `308` and header `Location: https://{http.request.host}{http.request.uri}`; a final route `static_response` status `404`; `automatic_https: {"disable": true}`.
- TLS automation subjects: existing `.test` subjects plus each LAN name and `*.<name>`, one sorted list.
- When enabled with zero LAN names: both servers exist; `srv-lan` has only the abort route and an SNI policy that matches nothing (`"sni": []` is "match all" in Caddy, so use a single impossible name `"lan.invalid"`); `lan-http` has only the 404 route.

The JSON value of `fallback_policy` must be confirmed against a running `caddy:2` (Task 11 does `/load` for real; if Caddy rejects `"require"`, use the spelling it accepts and update the golden).

- [ ] **Step 1: Capture the regression guard**

Run: `go test ./internal/caddyapi/ -v` and confirm every existing golden passes before editing.

- [ ] **Step 2: Write the failing tests**

`TestBuildLANEnabledGolden`: two backends, one with `LANDomain:"gamma.arrix.home.lan"`, LAN enabled, matches `lan_enabled.json`.
`TestBuildLANEnabledNoNamesGolden`: matches `lan_enabled_no_lan_names.json`.
`TestBuildLANDisabledIgnoresLANDomain`: `LAN{Enabled:false}` plus a backend with `LANDomain` gives output identical to the same state with `LANDomain` cleared and `LAN` nil.
`TestSrvLanNeverRoutesTestNames`: decode the enabled config; no host in any `srv-lan` or `lan-http` match ends in `.test`.
`TestListenerWrapperOrder`: `srv-lan` wrappers are `proxy_protocol` then `tls`.

Generate the two golden files from a reviewed first run, then read them line by line against the bullet list above before committing.

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/caddyapi/ -v`
Expected: new tests FAIL, existing goldens PASS.

- [ ] **Step 4: Implement**

- [ ] **Step 5: Run all caddyapi tests**

Run: `go test -race ./internal/caddyapi/...`
Expected: PASS, with no existing golden file modified (`git status internal/caddyapi/testdata` shows only the two new files).

- [ ] **Step 6: Commit**

```bash
git add internal/caddyapi
git commit -S -m "Rendered srv-lan and lan-http Caddy servers"
```

---

### Task 5: Forwarder container and ensureLAN

**Files:**
- Create: `internal/app/lan.go`
- Modify: `internal/app/app.go` (`Instance`, `DefaultInstance`, `App`, constructors), `internal/app/errors.go`
- Test: `internal/app/lan_test.go`, `internal/app/errors_test.go`

**Interfaces:**
- Consumes: `lanip.Source`, `lanip.Present` (Task 3); `state.LAN` (Task 1); `specDrift` with ports (Task 2); `caddyapi.LANHTTPSListenPort`, `caddyapi.LANHTTPListenPort` (Task 4).
- Produces:
  ```go
  const LANContainerName = "caddy_shack_lan"
  const LANImage = "haproxy:3.2-alpine"
  // Instance gains:
  LANContainerName string; LANHTTPPort int; LANHTTPSPort int // defaults "caddy_shack_lan", 80, 443
  // App gains: lan lanip.Source (default lanip.System())
  func (a *App) WithLANSource(src lanip.Source) *App // for tests; returns a
  func lanContainerSpec(inst Instance, ip string) docker.ContainerSpec
  func haproxyConfig(caddyHost string) string
  func (a *App) ensureLAN(s *state.State) error   // nil, ErrLANDown (wrapped, naming the IP), or another error
  func (a *App) stopLAN() error                   // stop if running; safe when absent
  func (a *App) removeLAN() error
  func (a *App) noteLANDown(err error) error      // if errors.Is(err, ErrLANDown): notef it and return nil; else return err
  var ErrLANDown = errors.New("LAN IP is not present on any interface")   // exit 1 when returned
  var ErrLANDisabled = errors.New("LAN mode is not enabled; run 'caddy_shack lan enable' first") // exit 10
  ```
  `lanContainerSpec`: `NetworkMode: inst.NetworkName`; ports `{ip, LANHTTPSPort, 8443}`, `{ip, LANHTTPPort, 8080}`; command `["sh","-c","printf '%s' \"$0\" > /tmp/haproxy.cfg && exec haproxy -W -db -f /tmp/haproxy.cfg", haproxyConfig(inst.ContainerName)]`.

  `haproxyConfig` (exact text, `<caddy>` replaced by the argument):
  ```
  global
    log stdout format raw local0 warning
  defaults
    mode tcp
    timeout connect 5s
    timeout client 1h
    timeout server 1h
  resolvers docker
    nameserver dns 127.0.0.11:53
    hold valid 10s
  frontend https
    bind :8443
    default_backend caddy_https
  frontend http
    bind :8080
    default_backend caddy_http
  backend caddy_https
    server caddy <caddy>:8443 send-proxy-v2 resolvers docker init-addr none
  backend caddy_http
    server caddy <caddy>:8080 send-proxy-v2 resolvers docker init-addr none
  ```

  `ensureLAN` order: not enabled means `removeLAN` and return nil; IP not `Present` means do not touch Docker, return `ErrLANDown`; `EnsureNetwork(inst.NetworkName)`; existing and no drift means start if stopped; otherwise remove and `CreateAndStart`, removing again on failure. An "address already in use" error from Docker is rewrapped as `"<ip>:<port> is already in use by another process"`.

- [ ] **Step 1: Write the failing tests** (`docker.Fake`, fake `lanip.Source`)

`TestEnsureLANDisabledRemovesForwarder`, `TestEnsureLANCreatesWhenAbsent` (asserts ports and network on the created spec), `TestEnsureLANStartsStopped`, `TestEnsureLANReplacesOnIPDrift`, `TestEnsureLANIPMissingTouchesNothing` (`errors.Is(err, ErrLANDown)`, no Docker calls recorded), `TestHaproxyConfigPointsAtCaddy` (contains `caddy_shack:8443 send-proxy-v2` and `init-addr none`), `TestExitCodeLANDisabled` (`ExitCode(ErrLANDisabled) == 10`), `TestNoteLANDownSwallowsOnlyLANDown`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/app/ -run 'EnsureLAN|Haproxy|LANDisabled|NoteLANDown' -v`
Expected: FAIL.

- [ ] **Step 3: Implement as described in Interfaces**; add `case errors.Is(err, ErrLANDisabled): return 10` to `ExitCode` and update its doc comment to `2-10`.

- [ ] **Step 4: Run the full suite**

Run: `go test -race ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app
git commit -S -m "Added the caddy_shack_lan forwarder container"
```

---

### Task 6: `lan enable` and `lan disable`

**Files:**
- Modify: `internal/app/lan.go`
- Create: `cmd/lan.go`, `cmd/lan_test.go`
- Modify: `cmd/root.go` (register the command group; add exit code 10 to the help text)
- Test: `internal/app/lan_test.go`

**Interfaces:**
- Consumes: Tasks 1, 3, 5; `a.commit(ctx, prev, updated)` (`internal/app/register.go:133`); `acquireStateLock`, `loadState`; `PromptFunc` (`internal/app/doctor.go:119`).
- Produces:
  ```go
  type LANEnableInput struct {
      IP      string     // empty means discover
      Suffix  string     // empty means keep the stored suffix; required if none stored
      Confirm PromptFunc // nil means proceed without asking (--yes)
  }
  type LANEnableResult struct { IP string; Mode state.LANMode; Suffix string; Note string }
  func (a *App) LANEnable(ctx context.Context, in LANEnableInput) (LANEnableResult, error)
  func (a *App) LANDisable(ctx context.Context) error
  func validLANName(name string) error // DNS name, not "test", not ending ".test"; wraps ErrInvalidArgs
  ```
  `LANEnable`: requires Caddy running (`ErrCaddyNotRunning`); validate suffix; resolve IP (pinned: parse IPv4, not loopback, `Present` or `ErrInvalidArgs` naming the IP; discovered: `lanip.Discover`, errors wrapped in `ErrInvalidArgs`); `Confirm("Bind LAN access to <ip> (<mode>)? ")` (false gives an error `"cancelled"`, exit 1); under the state lock set `LAN{Enabled:true, ...}` and `commit`; `ensureSharedNetwork`; then `ensureLAN` (errors returned as is, so `ErrLANDown` exits 1 here). `LANDisable`: `removeLAN` first, then under the lock set `Enabled=false` and `commit`. Neither touches any backend's `LANDomain`.

  CLI: `caddy_shack lan enable [--ip X] [--suffix S] [--yes]`, `caddy_shack lan disable`. Print the result, the `Note` if any, and a reminder line: `Point a DNS A record for *.<suffix> (or each LAN name) at <ip>.`

- [ ] **Step 1: Write the failing tests**

`TestLANEnableDiscovered` (state saved with mode discovered, forwarder created), `TestLANEnablePinnedAbsentIP` (`ErrInvalidArgs`, no state write), `TestLANEnableRequiresSuffixFirstTime`, `TestLANEnableRejectsTestSuffix` (`arrix.test` and `test`), `TestLANEnableKeepsExistingLANDomains` (Review Focus 2: re-enable with a new suffix leaves `gamma.arrix.home.lan` unchanged), `TestLANEnableConfirmDeclined` (no state write), `TestLANEnableIPMissingAfterSave` exits with `ErrLANDown`, `TestLANDisableRemovesForwarderBeforeConfig` (the fake records `Remove` before the Caddy `/load`), `TestLANDisableKeepsSettings`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/app/ ./cmd/ -run 'LANEnable|LANDisable' -v`
Expected: FAIL.

- [ ] **Step 3: Implement as described in Interfaces**

- [ ] **Step 4: Run the full suite**

Run: `go test -race ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app cmd
git commit -S -m "Added lan enable and lan disable commands"
```

---

### Task 7: `register --lan`, `--lan-domain` and `list`

**Files:**
- Modify: `internal/app/register.go`, `internal/app/list.go`, `cmd/register.go`, `cmd/list.go`
- Test: `internal/app/register_test.go`, `internal/app/list_test.go`, `cmd/register_test.go`, `cmd/list_test.go`

**Interfaces:**
- Consumes: `validLANName`, `ErrLANDisabled` (Tasks 5, 6); `state.LANEnabled()`.
- Produces: `RegisterInput` gains `LAN bool` and `LANDomain string` (mutually exclusive: both set is `ErrInvalidArgs`). `Listing` gains `LANDomain string \`json:"lan_domain,omitempty"\``.

  Resolution happens after loading state under the lock: `--lan` gives `<Name>.<LAN.Suffix>`; `--lan-domain` is validated with `validLANName`; either flag when `!s.LANEnabled()` gives `ErrLANDisabled`. Neither flag gives an empty `LANDomain` (full upsert). The conflict check compares the new backend's `Domain` and `LANDomain` against every other backend's `Domain` and `LANDomain`. The identical-re-registration shortcut also compares `LANDomain`.

  `list` prints a `LAN` column (blank when empty) after the domain column.

- [ ] **Step 1: Write the failing tests**

`TestRegisterLANUsesSuffix` (`gamma` gives `gamma.arrix.home.lan`), `TestRegisterLANDottedName` (`bar.gamma` gives `bar.gamma.arrix.home.lan`), `TestRegisterLANDomainOverride`, `TestRegisterLANWhenDisabled` (exit 10 via `ExitCode`, other backends' `LANDomain` untouched; Review Focus 3), `TestRegisterLANDomainRejectsTest`, `TestRegisterLANConflictAcrossSpaces` (backend A `--domain gamma.arrix.home.lan` and backend B `--lan-domain gamma.arrix.home.lan` conflict both ways; Review Focus 1), `TestRegisterWithoutLANClearsIt`, `TestRegisterBothFlagsInvalid`, `TestListShowsLANDomain`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/app/ ./cmd/ -run 'RegisterLAN|RegisterWithoutLAN|RegisterBothFlags|ListShowsLAN' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**; update `cmd/register.go` `Long` help with a LAN paragraph and the two flags.

- [ ] **Step 4: Run the full suite**

Run: `go test -race ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app cmd
git commit -S -m "Added LAN names to register and list"
```

---

### Task 8: Lifecycle wiring

**Files:**
- Modify: `internal/app/lifecycle.go`, `internal/app/install.go`, `internal/app/reconcile.go`, `internal/app/upgrade.go`
- Test: `internal/app/lifecycle_test.go`, `internal/app/install_test.go`, `internal/app/upgrade_test.go`, `internal/app/reconcile_test.go`

**Interfaces:**
- Consumes: `ensureLAN`, `stopLAN`, `noteLANDown` (Task 5); `lanip.Discover` (Task 3).
- Produces: `func (a *App) rediscoverLAN() error`: under the state lock, when LAN is enabled and mode is discovered, run `Discover`; if the IP differs, save state and `notef("LAN IP changed %s -> %s; update your DNS A record\n", old, new)`; discovery failure is a notice, not an error; pinned mode does nothing.

  Wiring (always after Caddy config is restored, so routes exist before LAN traffic):
  - `Start`: after `restoreThenRelay`, load state and `noteLANDown(a.ensureLAN(s))`; join with existing errors.
  - `Stop`: also `stopLAN()`.
  - `Restart`: `Stop`, then `rediscoverLAN`, then `Start`.
  - `Install`: after its existing steps, the same as `Start`.
  - `Sync`: after the config push, the same as `Start`.
  - `Upgrade`: `PullImage(LANImage)`; if LAN is enabled and the forwarder's `ContainerImageID` differs from `ImageID(LANImage)`, `removeLAN` then `ensureLAN` (through `noteLANDown`). This runs on the `AlreadyCurrent` path too.

- [ ] **Step 1: Write the failing tests**

`TestStartEnsuresLANAfterRoutes` (call order: Caddy `/load` before forwarder `CreateAndStart`), `TestStartLANDownIsNotice` (returns nil, notice written), `TestStopStopsLAN`, `TestRestartRediscoversWhenDiscovered` (state IP updated, forwarder replaced with new port binding, notice contains `->`), `TestRestartKeepsPinnedIP`, `TestRestartDiscoveryFailureIsNotice`, `TestUpgradeReplacesStaleForwarder`, `TestUpgradeLeavesCurrentForwarder`, `TestSyncEnsuresLAN`, `TestLifecycleNoLANUnchanged` (with `LAN == nil`, the recorded Docker calls for start, stop and restart match the pre-change sequence).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/app/ -run 'LAN|Rediscover|Forwarder|NoLANUnchanged' -v`
Expected: FAIL.

- [ ] **Step 3: Implement as described in Interfaces**

- [ ] **Step 4: Run the full suite**

Run: `go test -race ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app
git commit -S -m "Wired the LAN forwarder into the container lifecycle"
```

---

### Task 9: LAN section in `status`

**Files:**
- Modify: `internal/app/status.go`, `cmd/status.go`
- Test: `internal/app/status_test.go`, `cmd/status_test.go`

**Interfaces:**
- Produces:
  ```go
  type LANStatus struct {
      Enabled        bool   `json:"enabled"`
      IP             string `json:"ip"`
      Mode           string `json:"mode"`
      Suffix         string `json:"suffix"`
      IPPresent      bool   `json:"ip_present"`
      ForwarderState string `json:"forwarder_state"`
      DiscoveredIP   string `json:"discovered_ip,omitempty"` // set only in discovered mode when it differs from IP
      LANBackends    int    `json:"lan_backends"`
  }
  // StatusReport gains: LAN *LANStatus `json:"lan,omitempty"` (nil when no LAN block in state)
  ```
  LAN never sets `priorityErr` and never changes `Healthy`. Text output follows the spec's Status block; drift line `interface now has <ip>, run caddy_shack restart`.

- [ ] **Step 1: Write the failing tests**

`TestStatusNoLANBlock` (`rep.LAN == nil`), `TestStatusLANHealthy`, `TestStatusLANDownStillHealthy` (IP missing, forwarder absent, `Healthy == true`, error nil), `TestStatusLANDrift`, `TestStatusLANTextOutput` (cmd layer, golden strings).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/app/ ./cmd/ -run 'StatusLAN|StatusNoLAN' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run the full suite**

Run: `go test -race ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app cmd
git commit -S -m "Reported LAN mode in status"
```

---

### Task 10: Doctor checks

**Files:**
- Create: `internal/app/doctor_lan_checks.go`
- Modify: `internal/app/doctor.go` (`buildCheckList`)
- Test: `internal/app/doctor_lan_checks_test.go`

**Interfaces:**
- Consumes: Tasks 3, 5, 8 (`rediscoverLAN` for fixes); `ExportCA` (`internal/app/ca.go:25`); the existing `shell` runner used by other checks.
- Produces six checks, appended after `checkConfigDrift`, each returning `SeveritySkip` with summary `"LAN mode off"` when `!s.LANEnabled()`:

  | Function | Name | Result | Fix |
  |---|---|---|---|
  | `checkLANIPPresent` | `lan-ip-present` | FAIL when `!Present` | discovered mode: `rediscoverLAN` then `ensureLAN`; pinned: none |
  | `checkLANForwarder` | `lan-forwarder` | FAIL when absent, stopped or drifted (detail: drift list) | `ensureLAN` |
  | `checkLANIPDrift` | `lan-ip-drift` | WARN in discovered mode when `Discover` differs | `rediscoverLAN` then `ensureLAN` |
  | `checkLANPath` | `lan-path` | FAIL when a TLS dial to `<ip>:<LANHTTPSPort>` with the first LAN name as SNI does not verify against the exported CA, or a dial with the first `.test` name as SNI completes a handshake. Positive half SKIPs with no LAN names; negative half SKIPs with no backends. | none |
  | `checkLANDNS` | `lan-dns` | WARN listing each LAN name whose `net.DefaultResolver.LookupHost` does not include the stored IP | none |
  | `checkLANFirewall` | `lan-firewall` | Linux: WARN when `systemctl is-active ufw` prints `active` (detail: published ports bypass ufw; filter in `DOCKER-USER`). macOS: WARN when `/usr/libexec/ApplicationFirewall/socketfilterfw --getblockall` output contains `enabled`. | none |

  TLS dials and DNS lookups go through package-level function variables so tests can stub them, matching how existing checks stub `shell`.

- [ ] **Step 1: Write the failing tests**: PASS, WARN or FAIL, and SKIP for each check, plus `--fix` applying for `lan-ip-present` (discovered), `lan-forwarder` and `lan-ip-drift`, and no fix offered for `lan-ip-present` when pinned.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/app/ -run 'CheckLAN' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run the full suite**

Run: `go test -race ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app
git commit -S -m "Added LAN checks to doctor"
```

---

### Task 11: Integration tests

**Files:**
- Create: `test/integration/lan_e2e_test.go`
- Modify: `test/integration/helpers.go` (LAN instance helper and cleanup)

**Interfaces:**
- Consumes: everything above; `app.NewForInstance`, `(*App).WithLANSource`, `SkipUnlessDocker`.
- Produces: `func lanTestInstance() app.Instance` (unique container, network, volume and forwarder names suffixed `_it_lan`; high ports for HTTP, HTTPS, admin, LAN HTTP and LAN HTTPS), and cleanup of all of them before and after each test.

  Tests bind the forwarder to a real host IP from `lanip.Discover(lanip.System())`, skipping if none, except where noted. An echo backend (Go `httptest` server on the host, registered host-mode) returns the request's `Host`, `X-Forwarded-For` and `RemoteAddr` the backend saw.

- [ ] **Step 1: Write the tests**

`TestLANServesLANName`: `lan enable` then `register gamma --lan`; HTTPS to `<ip>:<lanHTTPS>` with SNI `gamma.<suffix>` returns 200 and the chain verifies against `ExportCA`. This is also the real `/load` that confirms the `fallback_policy` spelling from Task 4.
`TestLANWildcardSubdomain`: SNI and Host `bar.gamma.<suffix>` returns 200 from gamma's upstream (Review Focus 5).
`TestLANRefusesTestSNI`: SNI `gamma.test` and `bar.gamma.test` to the LAN port fails the handshake.
`TestLANRefusesTestHost`: SNI `gamma.<suffix>` with `Host: gamma.test` is rejected (not a 200).
`TestLANHTTPRedirect`: `http://<ip>:<lanHTTP>/x?y=1` with Host `gamma.<suffix>` returns 308 with `Location: https://gamma.<suffix>/x?y=1`; Host `gamma.test` returns 404.
`TestLANAbsentIPKeepsTestServing`: `lan enable --ip <real ip>`, then rewrite `lan.ip` in the test's state file to `192.0.2.1` (simulating the address vanishing) and `restart`; `restart` returns nil with a LAN-down notice, `.test` returns 200, `Status().LAN.IPPresent == false`, and the forwarder is not running (`Stop` stopped it and `ensureLAN` left it alone).
`TestLANRebindOnRestart` (`runtime.GOOS == "linux"` only): fake source discovers `127.0.0.2`, enable, request succeeds on `127.0.0.2`; switch the source to `127.0.0.3`, `restart`; `127.0.0.3` answers and `127.0.0.2` refuses.
`TestLANClientIPSameHost`: the echo backend's `X-Forwarded-For` is a host address, not an address in the Docker network's subnet. Log the observed value either way; it is evidence for the closing statement, not proof of Method check 4.
`TestLANDisableStopsLAN`: after `lan disable`, the LAN port refuses connections and `.test` still returns 200.

- [ ] **Step 2: Run on the Mac**

Run: `go test -tags integration ./test/integration/ -run LAN -v -count=1`
Expected: PASS (the rebind test reports SKIP on darwin).

- [ ] **Step 3: Run the full integration suite three times** (timing-sensitive)

Run: `for i in 1 2 3; do go test -tags integration ./test/integration/... -count=1 || break; done`
Expected: three PASS runs.

- [ ] **Step 4: Hand the same commands to the user for Omarchy**

They run Steps 2 and 3 on the Linux box and paste the output. Expected: PASS, including `TestLANRebindOnRestart`.

- [ ] **Step 5: Commit**

```bash
git add test/integration
git commit -S -m "Added LAN exposure integration tests"
```

---

### Task 12: Documentation and manual acceptance

**Files:**
- Modify: `README.md` (replace the "planned, not yet implemented" section with real usage; add exit code 10; add a schema 2 upgrade note), `cmd/root.go` help text if not done in Task 6

**Interfaces:**
- Consumes: the finished feature.

- [ ] **Step 1: Rewrite the README LAN section**

Retitle it `## LAN exposure`, keep the diagram and properties, and add: one-time setup (`caddy_shack lan enable --suffix arrix.home.lan`), the DNS A record step (wildcard recommended), trusting the CA on other devices (`caddy_shack ca`), `register ... --lan` and `--lan-domain`, `lan disable`, how `restart` rebinds in discovered mode and not in pinned mode, the ufw bypass warning, and the Docker Desktop client IP caveat if Task 11 or the manual run shows it. Add `10  LAN mode not enabled` to the exit code block. Add a note that state written by this version (schema 2) cannot be read by older caddy_shack binaries.

- [ ] **Step 2: Check the docs against the binary**

Run: `go build -o bin/caddy_shack . && bin/caddy_shack lan --help && bin/caddy_shack register --help && bin/caddy_shack --help | grep -n '10'`
Expected: every flag and exit code in the README appears in the help output.

- [ ] **Step 3: Commit**

```bash
git add README.md cmd
git commit -S -m "Documented LAN exposure"
```

- [ ] **Step 4: Manual acceptance run**

Create a Testers Playbook project for this feature (via the `testers-playbook` MCP) with the spec's six Method checks as items, run once on the Mac and once on Omarchy, from a second LAN device. Check 6 on the Mac means changing the Mac's IP (for example switching between Ethernet and Wi-Fi) and running `caddy_shack restart`. Add a seventh item: reboot with the LAN interface down, confirm `.test` works and `status` shows LAN down, bring the interface up, run `caddy_shack restart`, confirm LAN works. The user records results in the Testers Playbook UI; failures go through the `testers-playbook` skill.

- [ ] **Step 5: Close the R&D record**

Run the `wombat-rnd` CLOSING workflow, using the Testers Playbook results and `TestLANClientIPSameHost`'s logged value as the evidence for PROVEN, DISPROVEN and DIVERGENCE.

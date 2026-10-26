# Kaizen: Lean and Secure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Act on every finding of the 2026-10-27 code review: close the admin API to sibling containers, collapse duplicated helpers, and remove dead code, with existing tests as the regression net.

**Architecture:** Pure refactors first (helpers, docker layer, config builder), then the behaviour change to the admin block, then the certificate path rework on top of the consolidated helpers. Each task is independently mergeable into `kaizen/lean-and-secure`.

**Tech Stack:** Go 1.25, cobra, lego, golangci-lint v2, `go test -race`.

**Spec:** `docs/superpowers/specs/2026-10-27-kaizen-lean-and-secure-design.md`

## Global Constraints

- `go test -race ./...` and `golangci-lint run` must pass after every task. Run both before committing.
- No behaviour change except where the task says so. Golden fixtures under `internal/caddyapi/testdata/` change only in Task 6.
- Never weaken an existing assertion to make a test pass. If an error message must change, update the assertion to the new exact text and say so in the report.
- Commit messages: past tense, subject 60 characters or fewer, signed with GPG (`git commit -S`), no agent attribution of any kind in message or trailers.
- Australian English in comments and docs. No em dashes or en dashes.
- Follow the file's existing comment style: every exported identifier keeps a doc comment.

## Review Focus

1. A user on an existing install after Task 6: `caddy_shack start` must print the "created without settings this version needs" notice (the boot script changed), and `caddy_shack install` must recreate the container. Pinned in Task 6 by a `specDrift` test on the old versus new command.
2. The host-side client after Task 6: `caddyapi.Client` dials `http://127.0.0.1:2019`, so its Host header is `127.0.0.1:2019` and must be in the origins list. Pinned in Task 6 by the rendered-config test.
3. A drift check against a Caddy freshly booted from the bare Caddyfile after Task 6: `compareConfig` must still report in sync with nothing registered. The bootstrap Caddyfile and the rendered admin block must agree on `origins`. Pinned in Task 6.
4. A certificate renewal whose state save fails after Task 8: the prior material must be back on disk before the rollback config is built, or the rollback re-pushes the new key. Pinned by the existing `issueAndInstall` rollback tests, which must stay green unchanged.
5. A container in Docker's `restarting` state after Task 5: `register` must refuse with exit 5 and `status` must print `restarting`, not `running`. Pinned in Task 5.

---

### Task 1: Consolidate state lock and load helpers

**Files:**
- Modify: `internal/app/cert.go` (lines 114-130, 269-276, 309-325, 368-375)
- Modify: `internal/app/recreate.go` (lines 62-78)
- Modify: `internal/app/list.go` (lines 44-50)
- Modify: `internal/app/status.go` (lines 130-134)
- Test: `internal/app/cert_test.go`

**Interfaces:**
- Consumes: `acquireStateLock(path string) (*state.Lock, error)` and `loadState(path string) (*state.State, error)` from `internal/app/reconcile.go`.
- Produces: nothing new. After this task `grep -n 'state.AcquireLock\|state.Load(' internal/app/*.go | grep -v _test` lists only `reconcile.go`.

- [ ] **Step 1: Write the failing test**

In `internal/app/cert_test.go`, add a test proving `CertRemove` honours `stateLockTimeout`:

```go
func TestCertRemoveHonoursStateLockTimeout(t *testing.T) {
	a, paths := newTestApp(t) // use whatever helper the file already uses to build an App with temp paths
	held, err := state.AcquireLock(paths.LockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Release() }()

	old := stateLockTimeout
	stateLockTimeout = 50 * time.Millisecond
	t.Cleanup(func() { stateLockTimeout = old })

	start := time.Now()
	_, err = a.CertRemove(t.Context(), "example.com")
	if !errors.Is(err, ErrLockContention) {
		t.Fatalf("err = %v, want ErrLockContention", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("CertRemove waited %s; stateLockTimeout was not honoured", time.Since(start))
	}
}
```

Adapt the App construction to the helper the file already uses; do not add a new one.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app -run TestCertRemoveHonoursStateLockTimeout -v`
Expected: FAIL, the call waits the hard-coded 5s.

- [ ] **Step 3: Replace every inline lock and load with the helpers**

In each file listed, replace `state.AcquireLock(a.paths.LockPath, 5*time.Second)` plus its `ErrLockTimeout` mapping with `acquireStateLock(a.paths.LockPath)`, and `state.Load(a.paths.StatePath)` plus its `IsCorrupt` mapping with `loadState(a.paths.StatePath)`. Drop imports that become unused.

- [ ] **Step 4: Run the package tests and lint**

Run: `go test -race ./internal/app && golangci-lint run`
Expected: PASS, 0 issues.

- [ ] **Step 5: Commit**

```bash
git add internal/app
git commit -S -m "Routed every state lock and load through the helpers"
```

---

### Task 2: Remove dead and duplicated code in the docker layer

**Files:**
- Modify: `internal/caddyapi/config.go:82-84` (delete `Build`; rename `BuildWith` to `Build`)
- Modify: `internal/app/relay.go` (call site of `BuildWith`)
- Modify: `internal/caddyapi/*_test.go` (callers of `Build`/`BuildWith`)
- Modify: `internal/docker/cli.go:132-144` (`Remove` uses `isNoSuchContainer`)
- Modify: `internal/docker/docker.go:72-75`, `internal/docker/cli.go`, `internal/docker/fake.go`, `test/integration/helpers.go:100` (delete `NetworkAttached`)
- Modify: `internal/app/doctor_checks.go:583` (`checkSharedNetwork` derives attachment from `InspectContainer(...).Networks`)
- Modify: `internal/app/install.go:145-148` and `internal/app/lan.go:143` (one port-in-use detector)
- Test: `internal/app/doctor_checks_test.go`, `internal/docker/cli_test.go`

**Interfaces:**
- Produces: `caddyapi.Build(s *state.State, opts Options) []byte` (the old `BuildWith` signature under the old `Build` name). `docker.Client` without `NetworkAttached`. A single package-level `var addrInUse = regexp.MustCompile(...)` in `internal/app/lan.go` used by both `install.go` and `lan.go`; `isPortAllocatedErr` is deleted.

- [ ] **Step 1: Rename `BuildWith` to `Build` and delete the zero-options wrapper**

Update every caller. The two-argument form is the only form.

- [ ] **Step 2: Make `CLI.Remove` use `isNoSuchContainer`**

Replace the two inline `strings.Contains` checks with `isNoSuchContainer(string(out))`.

- [ ] **Step 3: Delete `NetworkAttached`**

Remove it from the interface, the CLI, the fake, and the integration helper. In `checkSharedNetwork`, call `a.docker.InspectContainer(a.inst.ContainerName)` and test `slices.Contains(info.Networks, a.inst.NetworkName)`. Update the fake-driven tests in `doctor_checks_test.go` so the fake reports networks through `InspectContainer` instead.

- [ ] **Step 4: One port-in-use detector**

Delete `isPortAllocatedErr`. In `Install`, use `addrInUse.MatchString(err.Error())`. Keep the existing error message text so the install test still passes.

- [ ] **Step 5: Run the tests and lint**

Run: `go test -race ./... && golangci-lint run && go vet -tags integration ./test/integration/`
Expected: PASS, 0 issues, integration package compiles.

- [ ] **Step 6: Commit**

```bash
git add internal test
git commit -S -m "Removed dead and duplicated code in the docker layer"
```

---

### Task 3: One local CA fingerprint helper for the trust-store checks

**Files:**
- Modify: `internal/app/doctor_checks.go:231-260` and `431-460`
- Test: `internal/app/doctor_checks_test.go`

**Interfaces:**
- Consumes: `(a *App) ExportCA(ctx context.Context, includeIntermediate bool) (string, error)` from `internal/app/ca.go`.
- Produces: `func (a *App) localRootCAFingerprint(ctx context.Context) (pemCert []byte, sha256Hex string, err error)` in `internal/app/doctor_checks.go`. `sha256Hex` is uppercase hex of SHA-256 over the DER bytes of the root only. Returns `ExportCA`'s error unchanged so callers can map `ErrCaddyNotRunning` and the "not provisioned yet" case.

- [ ] **Step 1: Write the failing test**

```go
func TestLocalRootCAFingerprintHashesDER(t *testing.T) {
	// Build an App whose caddy client answers /pki/ca/local with a known PEM
	// (reuse the fixture pattern the ExportCA tests already use), then assert
	// the returned hex equals sha256 of the decoded DER, uppercase, and the
	// returned PEM is the root only.
}
```

Fill the body using the same httptest fixture the existing `ExportCA` tests use.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app -run TestLocalRootCAFingerprintHashesDER -v`
Expected: FAIL, undefined `localRootCAFingerprint`.

- [ ] **Step 3: Implement the helper and use it from both trust-store checks**

Both checks keep their container-state SKIP branch. Replace the `docker.Exec cat` and the inline hashing with the helper. On error, keep the existing WARN result ("no root CA yet") when the error is the not-provisioned case, and FAIL otherwise. The Darwin and Linux fix closures still receive the PEM bytes.

- [ ] **Step 4: Run the tests and lint**

Run: `go test -race ./internal/app && golangci-lint run`
Expected: PASS, 0 issues. Existing trust-store tests that stubbed `docker.Exec` for `cat root.crt` must be updated to stub the admin API instead; do not delete them.

- [ ] **Step 5: Commit**

```bash
git add internal/app
git commit -S -m "Shared one local CA fingerprint across trust-store checks"
```

---

### Task 4: Command layer trims

**Files:**
- Modify: `cmd/root.go:17-25` (inline `classifyError`)
- Create: `cmd/app.go` (the `withApp` adapter)
- Modify: every `cmd/*.go` whose `RunE` opens with `a, err := app.New()`
- Modify: `cmd/doctor.go:70` (delete the "implemented in Task 16" comment, keep a one-line doc comment)
- Modify: `internal/app/doctor_checks.go:751-760` (move the `checkPortAvailabilityOn` doc comment onto `checkPortAvailabilityOn`; give `joinPorts` its own one-liner)
- Test: `cmd/root_test.go` or the existing cmd test file for `Execute`

**Interfaces:**
- Produces: `func withApp(run func(cmd *cobra.Command, args []string, a *app.App) error) func(*cobra.Command, []string) error` in `cmd/app.go`.

- [ ] **Step 1: Write the failing test**

```go
func TestWithAppReturnsConstructorError(t *testing.T) {
	// Point os.UserConfigDir at an impossible location via t.Setenv("HOME", "") or
	// XDG_CONFIG_HOME on Linux, as the existing cmd tests do for app.New failures,
	// and assert withApp's RunE returns that error without calling run.
}
```

If no existing test exercises an `app.New()` failure, test the positive path instead: `withApp` passes through to `run` with a non-nil `*app.App`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd -run TestWithApp -v`
Expected: FAIL, undefined `withApp`.

- [ ] **Step 3: Implement `withApp` and apply it to every command**

Each `RunE` becomes `RunE: withApp(func(cmd *cobra.Command, args []string, a *app.App) error { ... })`. Inline `classifyError` into `Execute` as `return app.ExitCode(err)`. Make the two comment fixes.

- [ ] **Step 4: Run the tests and lint**

Run: `go test -race ./cmd ./internal/app && golangci-lint run`
Expected: PASS, 0 issues.

- [ ] **Step 5: Commit**

```bash
git add cmd internal/app/doctor_checks.go
git commit -S -m "Trimmed the command layer preamble and stale comments"
```

---

### Task 5: Report a restarting container honestly

**Files:**
- Modify: `internal/docker/docker.go:13-36` (add `StateRestarting`)
- Modify: `internal/docker/cli.go:209-235` (`ContainerState` reads `{{.State.Status}}`)
- Modify: `internal/docker/fake.go` (if it maps a status string)
- Test: `internal/docker/cli_test.go`, `internal/app/status_test.go`

**Interfaces:**
- Produces: `docker.StateRestarting` with `String()` returning `"restarting"`. `ContainerState` maps Docker status `running` to `StateRunning`, `restarting` to `StateRestarting`, and every other status (`created`, `exited`, `paused`, `dead`) to `StateStopped`. Absent stays `StateAbsent`.

- [ ] **Step 1: Write the failing tests**

In `internal/docker/cli_test.go`, add a table test over the status parser (extract `parseContainerStatus(s string) (ContainerState, error)` so it is testable without docker): `running`, `restarting`, `exited`, `created`, `paused`, `dead`, and `"junk"` which errors.

In `internal/app/status_test.go`: with the fake reporting `StateRestarting`, `Status` returns `ErrUnhealthy` and `rep.ContainerState == "restarting"`; `Register` returns `ErrCaddyNotRunning`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/docker ./internal/app -run 'Status|Restarting' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Change the format string to `{{.State.Status}}` and route the output through `parseContainerStatus`. Audit every `switch st` on `ContainerState` in `internal/app`: `Start` and `Install` must treat `StateRestarting` like `StateRunning` (do not call `docker start`; proceed to `restoreThenRelay`, where `waitForAdmin` bounds the wait). Everything that checks `st != StateRunning` already refuses correctly.

- [ ] **Step 4: Run the tests and lint**

Run: `go test -race ./... && golangci-lint run`
Expected: PASS, 0 issues.

- [ ] **Step 5: Commit**

```bash
git add internal
git commit -S -m "Reported a restarting container as restarting"
```

---

### Task 6: Close the admin API to sibling containers and document the trust boundary

**Files:**
- Modify: `internal/app/install.go:59-63` (`caddyBootScript` admin block gains `enforce_origin` and `origins`)
- Modify: `internal/caddyapi/config.go` (`adminConfig` gains `EnforceOrigin bool \`json:"enforce_origin"\`` and `Origins []string \`json:"origins"\``)
- Modify: `internal/caddyapi/testdata/*.json` (regenerate golden fixtures)
- Modify: `README.md:226` and the security notes section
- Test: `internal/caddyapi/config_test.go`, `internal/app/recreate_test.go`, `internal/app/reconcile_test.go`

**Interfaces:**
- Produces: a package-level `var AdminOrigins = []string{"127.0.0.1:2019", "localhost:2019"}` in `internal/caddyapi/config.go`, used by both the rendered config and (as a joined string) the boot Caddyfile in `install.go`. The Caddyfile form is:

```
{
	admin 0.0.0.0:2019 {
		enforce_origin
		origins 127.0.0.1:2019 localhost:2019
	}
}
```

- [ ] **Step 1: Write the failing tests**

In `internal/caddyapi/config_test.go`: the rendered config's `admin` object equals `{"listen":"0.0.0.0:2019","enforce_origin":true,"origins":["127.0.0.1:2019","localhost:2019"]}`.

In `internal/app/recreate_test.go`: `specDrift` against a `ContainerInfo` whose `Command` is the pre-change boot script (copy the old string literally into the test) reports `"outdated container command"`.

In `internal/app/reconcile_test.go`: `compareConfig` with an empty state and a live config consisting only of the new admin block reports `InSync: true`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/caddyapi ./internal/app -run 'Admin|Drift|InSync' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add the fields and the var, update the boot script using `strings.Join(caddyapi.AdminOrigins, " ")`, regenerate the golden fixtures with the package's existing update mechanism (or by hand if there is none, and say which in the report).

- [ ] **Step 4: Update the README**

Change line 226 so it no longer says loopback-only. Add a "Security notes" section (or extend the existing one) covering, in this order: the admin API is reachable from containers on the same Docker networks and `enforce_origin` is a Host-header check a hostile container can spoof; Let's Encrypt private keys are embedded in the pushed config and therefore in Caddy's autosave volume and readable from the admin API; the LAN listener's PROXY protocol wrapper accepts a header from any sender on the shared network, and an `allow` list would not exclude siblings because the forwarder shares that subnet, so it is documented rather than filtered. State that existing installs will be told to run `caddy_shack install` to recreate the container.

- [ ] **Step 5: Run the tests and lint**

Run: `go test -race ./... && golangci-lint run`
Expected: PASS, 0 issues.

- [ ] **Step 6: Commit**

```bash
git add internal README.md
git commit -S -m "Enforced admin API origins and documented the trust boundary"
```

---

### Task 7: Restructure the config builder

**Files:**
- Modify: `internal/caddyapi/config.go:88-388`
- Create: `internal/caddyapi/types.go` (the JSON shape types)
- Create: `internal/caddyapi/lan.go` (`lanServers`)
- Test: existing `internal/caddyapi` tests and golden fixtures, unchanged

**Interfaces:**
- Produces: package-level unexported types moved verbatim from inside `Build`; `func lanServers(s *state.State, byName func([]string, map[string]state.Backend) []route, covered map[string]bool) (servers map[string]server, subjects []string)` in `lan.go`. `Build`'s signature is unchanged.

- [ ] **Step 1: Confirm the golden tests pass before touching anything**

Run: `go test ./internal/caddyapi`
Expected: PASS.

- [ ] **Step 2: Hoist the types**

Move every `type ... struct` declared inside `Build` to `types.go`, unchanged, with a one-line doc comment each.

- [ ] **Step 3: Extract `lanServers`**

Move the `if s.LANEnabled() { ... }` block into `lanServers`, returning the two LAN servers and the LAN TLS subjects. `Build` merges them into its `servers` map and appends the subjects before sorting.

- [ ] **Step 4: Run the golden tests, the race suite and lint**

Run: `go test -race ./... && golangci-lint run && git status --porcelain internal/caddyapi/testdata`
Expected: PASS, 0 issues, and no fixture modified.

- [ ] **Step 5: Commit**

```bash
git add internal/caddyapi
git commit -S -m "Split the config builder into types and LAN servers"
```

---

### Task 8: Route certificate issuance through commit

**Files:**
- Modify: `internal/app/register.go:583-612` (`commit` gains an undo hook)
- Modify: `internal/app/cert.go:101-230`
- Test: `internal/app/cert_test.go` (existing rollback tests stay green unchanged)

**Interfaces:**
- Consumes: Task 1's helper usage in `cert.go`.
- Produces: `func (a *App) commitWith(ctx context.Context, prev, updated *state.State, undo func() error) error`. `commit` becomes `return a.commitWith(ctx, prev, updated, nil)`. Semantics of `commitWith`: build and push `updated`; on push failure run `undo` (if non-nil) and return the push error, with the undo error appended as "(additionally, ... failed: ...)" if it fails; on state-save failure run `undo` first, then roll Caddy back to `prev`, and return the save error annotated exactly as `commit` does today, with any undo failure appended the same way. `undo` runs before the rollback config is built because `buildConfig` reads material from disk.

- [ ] **Step 1: Read the existing rollback tests**

Run: `go test ./internal/app -run 'Cert' -v`
Expected: PASS. Note each assertion on error text; these are the contract.

- [ ] **Step 2: Implement `commitWith` and rewrite `issueAndInstall`**

`issueAndInstall` becomes: issue; lock; load; capture prior material; save new material; clone state and set the cert entry; `return a.commitWith(ctx, s, updated, func() error { return a.restoreMaterialOnly(domain, hadPrior, prior) })`. Delete `restoreMaterial`. Keep `restoreMaterialOnly`.

- [ ] **Step 3: Run the certificate tests**

Run: `go test ./internal/app -run 'Cert' -v`
Expected: PASS without editing any assertion. If an assertion on error text fails, match the new wording to the old exactly rather than editing the test, and report any case where that is impossible.

- [ ] **Step 4: Run the race suite and lint**

Run: `go test -race ./... && golangci-lint run`
Expected: PASS, 0 issues.

- [ ] **Step 5: Commit**

```bash
git add internal/app
git commit -S -m "Routed certificate issuance through commit"
```

---

### Task 9: Record the deliberate non-changes

**Files:**
- Modify: `internal/app/recreate.go:62` (doc comment on `recreateCaddy`)

- [ ] **Step 1: Extend the `recreateCaddy` doc comment**

Add one sentence: the state lock is held across the admin and relay waits (up to 45s) on purpose, so a concurrent command gets exit 8 rather than reconciling against a half-recreated container.

- [ ] **Step 2: Run lint**

Run: `golangci-lint run`
Expected: 0 issues.

- [ ] **Step 3: Commit**

```bash
git add internal/app/recreate.go
git commit -S -m "Documented why recreate holds the state lock"
```

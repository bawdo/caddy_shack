# Container-upstream routing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an opt-in `register --container <alias>` mode that makes caddy_shack reverse-proxy `<name>.test` to a container over a shared Docker network, leaving the existing `--port`/`host.docker.internal` path unchanged.

**Architecture:** A foundation layer adds idempotent Docker network primitives (`EnsureNetwork`, `ConnectNetwork`, `NetworkAttached`) to `docker.Client` plus an `App.ensureSharedNetwork()` helper. Three independent consumers then build on it in parallel: register container mode, install/start network attachment, and a doctor check. The Caddy config builder and state schema are untouched — `state.Backend.Upstream` already stores a full dial string and `caddyapi.Build` routes to any dial.

**Tech Stack:** Go, Cobra CLI, the `docker` binary shelled out via `internal/docker`, an in-memory `docker.Fake` for app-layer tests, golden-file JSON tests for `caddyapi`.

---

## Parallelisation & worktree strategy

Spec: `docs/superpowers/specs/2026-06-07-container-upstream-routing-design.md`.

Work is organised into three phases. **Phase 0 must complete and be committed before Phase 1 starts**, because every Phase-1 task depends on the new `docker.Client` methods, the `CaddyNetworkName` constant, and `App.ensureSharedNetwork()`.

```
Phase 0 (sequential, on branch container-upstream-routing)
  Task 0: docker network primitives + ensureSharedNetwork helper
        |
        v  (commit)
Phase 1 (PARALLEL - one git worktree per task, each branched from Phase-0 commit)
  Task A: register --container mode        (worktree wt-register)
  Task B: install/start network attach     (worktree wt-lifecycle)
  Task C: doctor shared-network check       (worktree wt-doctor)
  Task D: caddyapi golden test for dial     (worktree wt-golden)
        |  merge A, B, C, D back to container-upstream-routing (review each)
        v
Phase 2 (sequential, on branch container-upstream-routing)
  Task E: integration test + full-suite green
```

**Why these four run in parallel safely:** they touch disjoint files. Each Phase-1 task adds its tests in a NEW test file (never editing an existing `*_test.go`) to avoid merge conflicts:

| Task | Production files | New test file |
|------|------------------|---------------|
| A | `internal/app/register.go`, `cmd/register.go` | `internal/app/register_container_test.go` |
| B | `internal/app/install.go`, `internal/app/lifecycle.go` | `internal/app/install_network_test.go` |
| C | `internal/app/doctor.go`, `internal/app/doctor_checks.go` | `internal/app/doctor_network_test.go` |
| D | (none) | `internal/caddyapi/config_container_test.go` + `testdata/` |

`internal/app/doctor.go` is touched only by Task C (one line in `buildCheckList`). No two tasks edit the same file.

**Worktree commands (run from the repo root for each Phase-1 task):**

```bash
# After Phase 0 is committed on container-upstream-routing:
git worktree add -b wt-register  ../caddy_shack-wt-register  container-upstream-routing
git worktree add -b wt-lifecycle ../caddy_shack-wt-lifecycle container-upstream-routing
git worktree add -b wt-doctor    ../caddy_shack-wt-doctor    container-upstream-routing
git worktree add -b wt-golden    ../caddy_shack-wt-golden    container-upstream-routing
```

**After each Phase-1 task is reviewed and green, merge it back (sequentially, to keep review simple):**

```bash
git checkout container-upstream-routing
git merge --no-ff wt-register      # then wt-lifecycle, wt-doctor, wt-golden
git worktree remove ../caddy_shack-wt-register
```

Run `go build ./... && go test ./...` on `container-upstream-routing` after each merge to catch cross-task integration issues early.

---

## Phase 0 — Foundation (sequential)

### Task 0: Docker network primitives + `ensureSharedNetwork` helper

**Files:**
- Modify: `internal/app/app.go` (add `CaddyNetworkName` constant)
- Modify: `internal/docker/docker.go` (extend `Client` interface)
- Modify: `internal/docker/cli.go` (real implementations)
- Modify: `internal/docker/fake.go` (fakes + new fields)
- Create: `internal/app/network.go` (`ensureSharedNetwork` helper)
- Create: `internal/docker/network_fake_test.go` (Fake idempotency tests)
- Create: `internal/app/network_test.go` (helper test against Fake)

- [ ] **Step 1: Add the network name constant**

In `internal/app/app.go`, add after the `CaddyConfigVolume` const:

```go
// CaddyNetworkName is the shared user-defined Docker network that
// container-mode backends are reached over. The caddy container is attached
// to it so it can resolve app containers by their network alias. Host-mode
// (--port) backends never use it.
const CaddyNetworkName = "caddy_shack"
```

- [ ] **Step 2: Write the failing Fake test**

Create `internal/docker/network_fake_test.go`:

```go
package docker

import "testing"

func TestFakeEnsureNetworkIsIdempotent(t *testing.T) {
	f := NewFake()
	if err := f.EnsureNetwork("caddy_shack"); err != nil {
		t.Fatalf("EnsureNetwork first: %v", err)
	}
	if err := f.EnsureNetwork("caddy_shack"); err != nil {
		t.Fatalf("EnsureNetwork second: %v", err)
	}
	if !f.Networks["caddy_shack"] {
		t.Fatal("network not recorded as created")
	}
}

func TestFakeConnectNetworkThenAttached(t *testing.T) {
	f := NewFake()
	attached, err := f.NetworkAttached("caddy_shack", "caddy_shack")
	if err != nil {
		t.Fatalf("NetworkAttached: %v", err)
	}
	if attached {
		t.Fatal("expected not attached before connect")
	}
	if err := f.ConnectNetwork("caddy_shack", "caddy_shack"); err != nil {
		t.Fatalf("ConnectNetwork: %v", err)
	}
	attached, err = f.NetworkAttached("caddy_shack", "caddy_shack")
	if err != nil {
		t.Fatalf("NetworkAttached after connect: %v", err)
	}
	if !attached {
		t.Fatal("expected attached after connect")
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/docker/ -run TestFake.*Network -v`
Expected: FAIL — compile error, `EnsureNetwork`/`ConnectNetwork`/`NetworkAttached`/`Networks` undefined.

- [ ] **Step 4: Extend the `Client` interface**

In `internal/docker/docker.go`, add to the `Client` interface (after `Exec`):

```go
	// EnsureNetwork creates the named user-defined bridge network if it
	// does not exist. Idempotent: an already-existing network is success.
	EnsureNetwork(name string) error
	// ConnectNetwork attaches the named container to the named network.
	// Idempotent: an already-attached container is success.
	ConnectNetwork(network, container string) error
	// NetworkAttached reports whether the named container is attached to
	// the named network. Returns (false, nil) if the container is absent.
	NetworkAttached(network, container string) (bool, error)
```

- [ ] **Step 5: Implement on the Fake**

In `internal/docker/fake.go`, add fields to the `Fake` struct (after `Removed []string`):

```go
	Networks           map[string]bool
	NetworkConnections map[string]bool // key: "<network>:<container>"

	EnsureNetworkErr  error
	ConnectNetworkErr error
```

Initialise the maps in `NewFake()` (alongside the existing maps):

```go
		Networks:           map[string]bool{},
		NetworkConnections: map[string]bool{},
```

Add the methods at the end of the file:

```go
func (f *Fake) EnsureNetwork(name string) error {
	if f.EnsureNetworkErr != nil {
		return f.EnsureNetworkErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Networks[name] = true
	return nil
}

func (f *Fake) ConnectNetwork(network, container string) error {
	if f.ConnectNetworkErr != nil {
		return f.ConnectNetworkErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.NetworkConnections[network+":"+container] = true
	return nil
}

func (f *Fake) NetworkAttached(network, container string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.NetworkConnections[network+":"+container], nil
}
```

- [ ] **Step 6: Run the Fake test to verify it passes**

Run: `go test ./internal/docker/ -run TestFake.*Network -v`
Expected: PASS.

- [ ] **Step 7: Implement on the real CLI**

In `internal/docker/cli.go`, add `"encoding/json"` to the import block, then add these methods (after `Exec`):

```go
func (CLI) EnsureNetwork(name string) error {
	cmd := exec.Command("docker", "network", "create", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		// `docker network create` errors if the network already exists;
		// treat that as success so the call is idempotent.
		if strings.Contains(strings.ToLower(string(out)), "already exists") {
			return nil
		}
		return fmt.Errorf("docker network create %s: %w: %s", name, err, out)
	}
	return nil
}

func (CLI) ConnectNetwork(network, container string) error {
	cmd := exec.Command("docker", "network", "connect", network, container)
	if out, err := cmd.CombinedOutput(); err != nil {
		low := strings.ToLower(string(out))
		// Already attached: docker reports "endpoint with name <c> already
		// exists in network <n>". Treat as success (idempotent).
		if strings.Contains(low, "already exists in network") ||
			strings.Contains(low, "already in network") {
			return nil
		}
		return fmt.Errorf("docker network connect %s %s: %w: %s", network, container, err, out)
	}
	return nil
}

func (CLI) NetworkAttached(network, container string) (bool, error) {
	cmd := exec.Command("docker", "inspect", "--format", "{{json .NetworkSettings.Networks}}", container)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && strings.Contains(strings.ToLower(stderr.String()), "no such object") {
			return false, nil
		}
		return false, fmt.Errorf("docker inspect %s networks: %w: %s", container, err, stderr.String())
	}
	var nets map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &nets); err != nil {
		return false, fmt.Errorf("parse networks for %s: %w", container, err)
	}
	_, ok := nets[network]
	return ok, nil
}
```

- [ ] **Step 8: Write the failing helper test**

Create `internal/app/network_test.go`:

```go
package app

import (
	"testing"

	"github.com/bawdo/caddy_shack/internal/docker"
)

func TestEnsureSharedNetworkCreatesAndConnects(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.ensureSharedNetwork(); err != nil {
		t.Fatalf("ensureSharedNetwork: %v", err)
	}
	if !fake.Networks[CaddyNetworkName] {
		t.Errorf("network %q not created", CaddyNetworkName)
	}
	attached, _ := fake.NetworkAttached(CaddyNetworkName, CaddyContainerName)
	if !attached {
		t.Errorf("caddy not attached to %q", CaddyNetworkName)
	}
}
```

- [ ] **Step 9: Run it to verify it fails**

Run: `go test ./internal/app/ -run TestEnsureSharedNetwork -v`
Expected: FAIL — `ensureSharedNetwork` undefined.

- [ ] **Step 10: Write the helper**

Create `internal/app/network.go`:

```go
package app

// ensureSharedNetwork makes the shared Docker network exist and attaches the
// caddy container to it, so container-mode (--container) backends can be
// reached by their network alias. Idempotent; safe to call repeatedly. The
// caller is responsible for ensuring the caddy container is running first.
func (a *App) ensureSharedNetwork() error {
	if err := a.docker.EnsureNetwork(CaddyNetworkName); err != nil {
		return err
	}
	return a.docker.ConnectNetwork(CaddyNetworkName, CaddyContainerName)
}
```

- [ ] **Step 11: Run all touched packages to verify green**

Run: `go build ./... && go test ./internal/docker/ ./internal/app/ -run 'Network|ensureShared|Fake' -v`
Expected: PASS. Then `go vet ./...` clean.

- [ ] **Step 12: Commit**

```bash
git add internal/app/app.go internal/app/network.go internal/app/network_test.go \
        internal/docker/docker.go internal/docker/cli.go internal/docker/fake.go \
        internal/docker/network_fake_test.go
git commit -m "Added shared-network docker primitives and helper"
```

---

## Phase 1 — Parallel consumers (one worktree each)

> Create the four worktrees now (see "Worktree commands" above). Each task below assumes it runs inside its own worktree branched from the Phase-0 commit.

### Task A: `register --container` mode

**Files:**
- Modify: `internal/app/register.go`
- Modify: `cmd/register.go`
- Test: `internal/app/register_container_test.go` (new)

- [ ] **Step 1: Write the failing tests**

Create `internal/app/register_container_test.go`:

```go
package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/state"
)

func TestRegisterContainerModeSetsContainerDial(t *testing.T) {
	f := newFixture(t, http.StatusOK)

	err := f.app.Register(context.Background(), RegisterInput{
		Name:      "myapp",
		Container: "myapp",
		Port:      3000,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	s, err := state.Load(f.statePath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b := s.Backends["myapp"]
	if b.Upstream != "myapp:3000" {
		t.Errorf("Upstream: got %q want %q", b.Upstream, "myapp:3000")
	}
	if !strings.Contains(string(f.lastBody), "myapp:3000") {
		t.Errorf("pushed config missing container dial; body=%s", f.lastBody)
	}
}

func TestRegisterHostModeUnchanged(t *testing.T) {
	f := newFixture(t, http.StatusOK)

	err := f.app.Register(context.Background(), RegisterInput{
		Name: "myapp",
		Port: 3000,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	s, _ := state.Load(f.statePath)
	if got := s.Backends["myapp"].Upstream; got != "host.docker.internal:3000" {
		t.Errorf("host mode Upstream changed: got %q", got)
	}
}

func TestRegisterContainerInvalidAlias(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	err := f.app.Register(context.Background(), RegisterInput{
		Name:      "myapp",
		Container: "Bad_Alias",
		Port:      3000,
	})
	if !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("expected ErrInvalidArgs, got %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/ -run TestRegisterContainer -v`
Expected: FAIL — `RegisterInput` has no `Container` field.

- [ ] **Step 3: Add the `Container` field and dial branch**

In `internal/app/register.go`, add `"regexp"` to imports if not present (it is). Add the alias regexp near `nameRE`:

```go
// containerRE validates a Docker network alias supplied via --container. A
// single DNS label keeps it usable as a network alias.
var containerRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
```

Add `Container string` to `RegisterInput` (after `Port int`), with a doc comment:

```go
	// Container, when non-empty, switches to container-routing mode: the
	// upstream dial becomes <Container>:<Port> over the shared network
	// instead of host.docker.internal:<Port>. Requires the caddy container
	// to be attached to CaddyNetworkName (ensured here).
	Container string
```

Replace the existing upstream construction line
`upstream := fmt.Sprintf("%s:%d", UpstreamHost, in.Port)` with:

```go
	var upstream string
	if in.Container != "" {
		if !containerRE.MatchString(in.Container) {
			return fmt.Errorf("%w: container alias must match ^[a-z0-9][a-z0-9-]{0,62}$ (got %q)", ErrInvalidArgs, in.Container)
		}
		upstream = fmt.Sprintf("%s:%d", in.Container, in.Port)
	} else {
		upstream = fmt.Sprintf("%s:%d", UpstreamHost, in.Port)
	}
```

- [ ] **Step 4: Ensure the shared network in container mode**

In `internal/app/register.go`, immediately after the existing container-running guard (the block returning `ErrCaddyNotRunning` when `st != docker.StateRunning`), add:

```go
	// Container mode needs the caddy container attached to the shared
	// network so it can resolve the app container by alias. Host mode never
	// touches the network. Idempotent; safe on repeated registers.
	if in.Container != "" {
		if err := a.ensureSharedNetwork(); err != nil {
			return fmt.Errorf("ensure shared network: %w", err)
		}
	}
```

- [ ] **Step 5: Run app-layer tests to verify pass**

Run: `go test ./internal/app/ -run 'TestRegister' -v`
Expected: PASS (new container tests + existing register tests).

- [ ] **Step 6: Wire the `--container` CLI flag**

In `cmd/register.go`, add `container` to the var block:

```go
	var (
		port      int
		domain    string
		container string
	)
```

Pass it through in the `RegisterInput`:

```go
			return a.Register(cmd.Context(), app.RegisterInput{
				Name:      args[0],
				Domain:    domain,
				Port:      port,
				Container: container,
			})
```

Register the flag (after the `domain` flag):

```go
	c.Flags().StringVar(&container, "container", "",
		"Container network alias to route to over the shared caddy_shack network "+
			"(instead of host.docker.internal). Requires --port.")
```

Append to the command `Long` description:

```
Container mode: pass --container <alias> to route to a container reachable on
the shared "caddy_shack" Docker network instead of host.docker.internal. The
app's compose must attach the service to that external network with the given
alias. --port is still required (the container's listening port).
```

- [ ] **Step 7: Verify build + full app/cmd tests**

Run: `go build ./... && go test ./internal/app/ ./cmd/ -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/app/register.go cmd/register.go internal/app/register_container_test.go
git commit -m "Added container-routing mode to register"
```

### Task B: install/start network attachment

**Files:**
- Modify: `internal/app/install.go`
- Modify: `internal/app/lifecycle.go`
- Test: `internal/app/install_network_test.go` (new)

- [ ] **Step 1: Write the failing tests**

Create `internal/app/install_network_test.go`:

```go
package app

import (
	"testing"

	"github.com/bawdo/caddy_shack/internal/docker"
)

func newNetFixtureApp(t *testing.T, fake *docker.Fake) *App {
	t.Helper()
	dir := t.TempDir()
	return NewWithPaths(Paths{
		StatePath: dir + "/state.json",
		LockPath:  dir + "/state.json.lock",
	}, nil, fake)
}

func TestInstallAttachesSharedNetwork(t *testing.T) {
	fake := docker.NewFake() // container absent -> create path
	a := newNetFixtureApp(t, fake)

	if err := a.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !fake.Networks[CaddyNetworkName] {
		t.Errorf("network %q not created on install", CaddyNetworkName)
	}
	attached, _ := fake.NetworkAttached(CaddyNetworkName, CaddyContainerName)
	if !attached {
		t.Errorf("caddy not attached to %q on install", CaddyNetworkName)
	}
}

func TestStartAttachesSharedNetwork(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateStopped
	a := newNetFixtureApp(t, fake)

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	attached, _ := fake.NetworkAttached(CaddyNetworkName, CaddyContainerName)
	if !attached {
		t.Errorf("caddy not attached to %q on start", CaddyNetworkName)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/ -run 'TestInstallAttaches|TestStartAttaches' -v`
Expected: FAIL — network not created/attached (no calls yet).

- [ ] **Step 3: Attach on the install create path**

In `internal/app/install.go`, replace the final `return nil` of `Install` (the line immediately after the `CreateAndStart` error block closes) with:

```go
	if err := a.ensureSharedNetwork(); err != nil {
		return fmt.Errorf("attach shared network: %w", err)
	}
	return nil
```

Also change the `StateStopped` case in `Install` from
`return a.docker.Start(CaddyContainerName)` to:

```go
	case docker.StateStopped:
		if err := a.docker.Start(CaddyContainerName); err != nil {
			return err
		}
		return a.ensureSharedNetwork()
```

- [ ] **Step 4: Attach on the lifecycle start path**

In `internal/app/lifecycle.go`, change the `StateStopped` case of `Start` from
`return a.docker.Start(CaddyContainerName)` to:

```go
	case docker.StateStopped:
		if err := a.docker.Start(CaddyContainerName); err != nil {
			return err
		}
		return a.ensureSharedNetwork()
```

- [ ] **Step 5: Run to verify pass**

Run: `go test ./internal/app/ -run 'TestInstall|TestStart' -v`
Expected: PASS (new network tests + existing install/lifecycle tests).

- [ ] **Step 6: Verify build + package tests**

Run: `go build ./... && go test ./internal/app/ -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/install.go internal/app/lifecycle.go internal/app/install_network_test.go
git commit -m "Attached caddy to shared network on install and start"
```

### Task C: doctor shared-network check

**Files:**
- Modify: `internal/app/doctor.go` (add check to `buildCheckList`)
- Modify: `internal/app/doctor_checks.go` (add `checkSharedNetwork`)
- Test: `internal/app/doctor_network_test.go` (new)

This check only requires attachment when at least one registered backend uses
container mode, so host-mode-only users never see a spurious failure.

- [ ] **Step 1: Write the failing tests**

Create `internal/app/doctor_network_test.go`:

```go
package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

func netDoctorApp(t *testing.T, fake *docker.Fake, backends map[string]state.Backend) *App {
	t.Helper()
	dir := t.TempDir()
	sp := filepath.Join(dir, "state.json")
	if err := state.Save(sp, &state.State{SchemaVersion: 1, Backends: backends}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	return NewWithPaths(Paths{StatePath: sp, LockPath: sp + ".lock"}, nil, fake)
}

func TestSharedNetworkPassesWithNoContainerBackends(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := netDoctorApp(t, fake, map[string]state.Backend{
		"web": {Domain: "web.test", Upstream: "host.docker.internal:3000"},
	})
	r := checkSharedNetwork(context.Background(), a)
	if r.Status != SeverityPass {
		t.Fatalf("status: got %v want pass (%s)", r.Status, r.Summary)
	}
}

func TestSharedNetworkFailsWhenContainerBackendButNotAttached(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := netDoctorApp(t, fake, map[string]state.Backend{
		"app": {Domain: "app.test", Upstream: "app:3000"},
	})
	r := checkSharedNetwork(context.Background(), a)
	if r.Status != SeverityFail {
		t.Fatalf("status: got %v want fail", r.Status)
	}
	if r.Fix == nil {
		t.Fatal("expected a fix action")
	}
	if _, err := a.ApplyFix(context.Background(), r, func(string) bool { return true }); err != nil {
		t.Fatalf("ApplyFix: %v", err)
	}
	attached, _ := fake.NetworkAttached(CaddyNetworkName, CaddyContainerName)
	if !attached {
		t.Error("fix did not attach caddy to shared network")
	}
}

func TestSharedNetworkPassesWhenAttached(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	_ = fake.ConnectNetwork(CaddyNetworkName, CaddyContainerName)
	a := netDoctorApp(t, fake, map[string]state.Backend{
		"app": {Domain: "app.test", Upstream: "app:3000"},
	})
	r := checkSharedNetwork(context.Background(), a)
	if r.Status != SeverityPass {
		t.Fatalf("status: got %v want pass (%s)", r.Status, r.Summary)
	}
}

func TestSharedNetworkSkippedWhenContainerNotRunning(t *testing.T) {
	fake := docker.NewFake() // absent
	a := netDoctorApp(t, fake, map[string]state.Backend{})
	r := checkSharedNetwork(context.Background(), a)
	if r.Status != SeveritySkip {
		t.Fatalf("status: got %v want skip", r.Status)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/ -run TestSharedNetwork -v`
Expected: FAIL — `checkSharedNetwork` undefined.

- [ ] **Step 3: Add the check**

In `internal/app/doctor_checks.go`, add (the `context`, `net`, `docker`, and `state` imports are already present):

```go
func checkSharedNetwork(_ context.Context, a *App) CheckResult {
	if err := a.docker.DaemonReachable(); err != nil {
		return CheckResult{Name: "shared-network", Status: SeveritySkip, Summary: "docker daemon unreachable"}
	}
	cstate, _ := a.docker.ContainerState(CaddyContainerName)
	if cstate != docker.StateRunning {
		return CheckResult{Name: "shared-network", Status: SeveritySkip, Summary: "container not running"}
	}
	st, err := state.Load(a.paths.StatePath)
	if err != nil {
		return CheckResult{Name: "shared-network", Status: SeveritySkip, Summary: "state unreadable; covered by state-integrity"}
	}
	containerBackends := 0
	for _, b := range st.Backends {
		host, _, err := net.SplitHostPort(b.Upstream)
		if err == nil && host != UpstreamHost {
			containerBackends++
		}
	}
	if containerBackends == 0 {
		return CheckResult{
			Name:    "shared-network",
			Status:  SeverityPass,
			Summary: "no container-mode backends; shared network not required",
		}
	}
	attached, err := a.docker.NetworkAttached(CaddyNetworkName, CaddyContainerName)
	if err != nil {
		return CheckResult{
			Name:    "shared-network",
			Status:  SeverityWarn,
			Summary: "cannot inspect container networks",
			Detail:  []string{err.Error()},
		}
	}
	if attached {
		return CheckResult{
			Name:    "shared-network",
			Status:  SeverityPass,
			Summary: fmt.Sprintf("caddy attached to %q (%d container-mode backend(s))", CaddyNetworkName, containerBackends),
		}
	}
	return CheckResult{
		Name:    "shared-network",
		Status:  SeverityFail,
		Summary: fmt.Sprintf("caddy not attached to %q but %d container-mode backend(s) need it", CaddyNetworkName, containerBackends),
		Detail:  []string{"container-mode (--container) registrations are unreachable until caddy joins this network"},
		Fix: &FixAction{
			Label: "create and attach shared network ('docker network connect " + CaddyNetworkName + " " + CaddyContainerName + "')",
			Apply: func(_ context.Context) error { return a.ensureSharedNetwork() },
		},
	}
}
```

- [ ] **Step 4: Register the check in the ordered list**

In `internal/app/doctor.go`, in `buildCheckList`, add this line immediately after the `checkContainerHealth` entry:

```go
		func(ctx context.Context) CheckResult { return checkSharedNetwork(ctx, a) },
```

- [ ] **Step 5: Run to verify pass**

Run: `go test ./internal/app/ -run 'TestSharedNetwork|TestDoctor' -v`
Expected: PASS. (If an existing doctor test asserts an exact check count or ordered list of names, update that test to include `shared-network` after `container-health`.)

- [ ] **Step 6: Verify build + package tests**

Run: `go build ./... && go test ./internal/app/ -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/doctor.go internal/app/doctor_checks.go internal/app/doctor_network_test.go
git commit -m "Added doctor check for the shared network"
```

### Task D: caddyapi golden test for a container dial

**Files:**
- Test: `internal/caddyapi/config_container_test.go` (new)
- Create: `internal/caddyapi/testdata/one_container_backend.json` (golden)

This proves `Build` routes a container-alias dial verbatim — no production
change, a regression guard for the upstream-agnostic builder.

- [ ] **Step 1: Write the test**

Create `internal/caddyapi/config_container_test.go`:

```go
package caddyapi

import (
	"testing"

	"github.com/bawdo/caddy_shack/internal/state"
)

func TestBuildConfigContainerUpstream(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"app": {Domain: "app.test", Upstream: "app:3000"},
		},
	}
	got := Build(s)
	assertJSONMatchesGolden(t, got, "one_container_backend.json")
}
```

- [ ] **Step 2: Run to verify it fails (missing golden)**

Run: `go test ./internal/caddyapi/ -run TestBuildConfigContainerUpstream -v`
Expected: FAIL — golden file `one_container_backend.json` not found, OR a mismatch the harness prints.

- [ ] **Step 3: Generate the golden from the existing one_backend fixture**

The existing `testdata/one_backend.json` has domain `myapp.test` and dial
`127.0.0.1:3000`. The container golden is identical except the domain becomes
`app.test` and every dial becomes `app:3000`. The `assertJSONMatchesGolden`
helper compares JSON semantically, so formatting does not need to match. Produce
it deterministically:

Run:
```bash
sed -e 's/myapp\.test/app.test/g' \
    -e 's#127\.0\.0\.1:3000#app:3000#g' \
    internal/caddyapi/testdata/one_backend.json \
    > internal/caddyapi/testdata/one_container_backend.json
```

Then open `internal/caddyapi/testdata/one_container_backend.json` and confirm:
the two `dial` values read `"app:3000"`, the two host matchers read `app.test`
and `*.app.test`, and the two TLS subjects read `app.test` and `*.app.test`.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/caddyapi/ -run TestBuildConfigContainerUpstream -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/caddyapi/config_container_test.go internal/caddyapi/testdata/one_container_backend.json
git commit -m "Added golden test for container upstream dial"
```

---

## Phase 2 — Integration & sign-off (sequential, after all merges)

> Run on `container-upstream-routing` after Tasks A–D are merged and `go test ./...` is green.

### Task E: docker-gated integration test

**Files:**
- Inspect: `test/integration/helpers.go` (reuse its docker-availability gate + register helpers)
- Test: `test/integration/container_upstream_test.go` (new)

- [ ] **Step 1: Learn the existing integration gate**

Run: `sed -n '1,120p' test/integration/helpers.go`
Read how tests skip when docker is unavailable, how they install caddy, and the
helper(s) used to register/deregister. Reuse those — do not reinvent the gate.

- [ ] **Step 2: Write the integration test**

Create `test/integration/container_upstream_test.go`. Mirror the structure of
the existing integration tests you just read (same build tag/skip gate, same
setup helper). The test must:
1. Skip if docker is unavailable (use the existing gate helper).
2. Ensure caddy is installed/running (existing helper).
3. `docker network create caddy_shack` is idempotent — assert install/start
   left caddy attached, OR call the register path below which ensures it.
4. Start a throwaway container attached to `caddy_shack` with alias `itapp`
   that serves HTTP on a known port (e.g. `docker run -d --network caddy_shack
   --network-alias itapp <small-http-image>`).
5. Register it: `caddy_shack register itapp --container itapp --port <port>`.
6. Assert `https://itapp.test` (through caddy) returns the container's response,
   trusting caddy's internal CA as the existing integration tests do.
7. Cleanup: deregister, stop/remove the throwaway container.

Write the concrete code by copying the closest existing integration test and
substituting the container-upstream specifics above. Keep all literals (image
name, ports, timeouts) consistent with the existing tests' conventions.

- [ ] **Step 3: Run the integration test**

Run: `go test ./test/integration/ -run ContainerUpstream -v`
Expected: PASS when docker is available; SKIP otherwise.

- [ ] **Step 4: Full suite + vet + lint**

Run: `go build ./... && go test ./... && go vet ./...`
Expected: all PASS. If the repo has a `Makefile` lint target, run it too.

- [ ] **Step 5: Commit**

```bash
git add test/integration/container_upstream_test.go
git commit -m "Added integration test for container upstream routing"
```

---

## Self-review notes (author checklist, done before handoff)

- **Spec coverage:** CLI surface → Task A; shared network + lifecycle/migration → Tasks 0/B (+ register-ensures in A) and doctor `--fix` in C; internal changes (docker.Client, register.go, cmd) → Tasks 0/A; caddyapi/state unchanged → asserted by Task D golden; testing section → Tasks A–E; exit codes (ErrInvalidArgs, no new codes) → Task A. Lifecycle "register --container ensures" → Task A Step 4.
- **Type consistency:** `EnsureNetwork(string) error`, `ConnectNetwork(network, container string) error`, `NetworkAttached(network, container string) (bool, error)`, `App.ensureSharedNetwork() error`, `RegisterInput.Container string`, const `CaddyNetworkName` — used identically across Tasks 0, A, B, C.
- **Placeholders:** none; every code step shows full code. Task E intentionally instructs copying the nearest existing integration test (its exact helpers are repo-specific and must be read at execution time, per Step 1) rather than inventing helper signatures that may not exist.
- **Migration:** existing running caddy that predates the network self-heals via Task A Step 4 (register --container ensures) and Task C's `--fix`; host-mode users are unaffected (Task C passes when no container backends, Task A leaves the host dial unchanged — guarded by `TestRegisterHostModeUnchanged`).

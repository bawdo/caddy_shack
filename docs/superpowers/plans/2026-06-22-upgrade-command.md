# Upgrade Command Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `caddy_shack upgrade` command that re-pulls `caddy:2`, recreates the container if the image changed (preserving volumes), and restores registered backends from state.

**Architecture:** A new `App.Upgrade` orchestrator re-pulls the pinned image, compares image IDs to decide whether to recreate, and on a change recreates the container via a spec builder shared with `install`, then re-pushes backends from `state.json` via the Caddy admin API. A thin Cobra command renders the outcome.

**Tech Stack:** Go, Cobra, the `docker` CLI abstraction, the Caddy admin API client. Tests use the in-memory `docker.Fake` plus an `httptest` Caddy stub, matching the existing app tests.

## Global Constraints

- Module path: `github.com/bawdo/caddy_shack`. All imports use this prefix.
- No new error sentinels or exit codes. Reuse `ErrDockerUnreachable` (4), `ErrNotInstalled` (6), `ErrStateCorrupt` (7), `ErrLockContention` (8); a reload failure is a generic error (exit 1).
- Pinned image constant is `CaddyImage = "caddy:2"`. `upgrade` re-pulls this same tag; no new image constant.
- Container name `CaddyContainerName = "caddy_shack"`; volumes `CaddyDataVolume`, `CaddyConfigVolume`; network `CaddyNetworkName`. All already defined in `internal/app/app.go`.
- Commit message style: past tense, subject <= 60 chars, no mention of any AI assistant, no signing.
- Run the full suite with `go test ./...` before each commit.

---

### Task 1: Image-ID inspection in the docker layer

**Files:**
- Modify: `internal/docker/docker.go` (add two methods to the `Client` interface)
- Modify: `internal/docker/cli.go` (implement on `CLI`)
- Modify: `internal/docker/fake.go` (implement on `Fake` + config fields)
- Test: `internal/docker/fake_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `ImageID(ref string) (string, error)` on `docker.Client`
  - `ContainerImageID(name string) (string, error)` on `docker.Client`
  - `Fake` fields: `ImageIDs map[string]string`, `ContainerImageIDs map[string]string`, `ImageIDErr error`, `ContainerImageIDErr error`

- [ ] **Step 1: Write the failing tests**

Append to `internal/docker/fake_test.go`:

```go
func TestFakeImageIDReturnsConfiguredValue(t *testing.T) {
	f := NewFake()
	f.ImageIDs["caddy:2"] = "sha256:abc"
	id, err := f.ImageID("caddy:2")
	if err != nil {
		t.Fatalf("ImageID: %v", err)
	}
	if id != "sha256:abc" {
		t.Errorf("ImageID: got %q want %q", id, "sha256:abc")
	}
}

func TestFakeContainerImageIDReturnsConfiguredValue(t *testing.T) {
	f := NewFake()
	f.ContainerImageIDs["caddy_shack"] = "sha256:def"
	id, err := f.ContainerImageID("caddy_shack")
	if err != nil {
		t.Fatalf("ContainerImageID: %v", err)
	}
	if id != "sha256:def" {
		t.Errorf("ContainerImageID: got %q want %q", id, "sha256:def")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/docker/ -run 'TestFakeImageID|TestFakeContainerImageID' -v`
Expected: compile failure — `f.ImageIDs undefined` / `f.ImageID undefined`.

- [ ] **Step 3: Add the interface methods**

In `internal/docker/docker.go`, inside the `Client` interface (after `NetworkAttached`), add:

```go
	// ImageID returns the local image ID (e.g. "sha256:...") for ref.
	// Errors if the image is not present locally.
	ImageID(ref string) (string, error)
	// ContainerImageID returns the image ID the named container was created
	// from. Works against stopped containers.
	ContainerImageID(name string) (string, error)
```

- [ ] **Step 4: Implement on `CLI`**

Append to `internal/docker/cli.go`:

```go
func (CLI) ImageID(ref string) (string, error) {
	cmd := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", ref)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker image inspect %s: %w: %s", ref, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (CLI) ContainerImageID(name string) (string, error) {
	cmd := exec.Command("docker", "inspect", "--format", "{{.Image}}", name)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker inspect %s image: %w: %s", name, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}
```

(`bytes`, `fmt`, `strings`, `os/exec` are already imported in `cli.go`.)

- [ ] **Step 5: Implement on `Fake`**

In `internal/docker/fake.go`, add fields to the `Fake` struct (next to the other error-injection fields and maps):

```go
	ImageIDs            map[string]string // ref -> image id
	ContainerImageIDs   map[string]string // container -> image id
	ImageIDErr          error
	ContainerImageIDErr error
```

In `NewFake`, initialise the two maps alongside the others:

```go
		ImageIDs:           map[string]string{},
		ContainerImageIDs:  map[string]string{},
```

Append the methods:

```go
func (f *Fake) ImageID(ref string) (string, error) {
	if f.ImageIDErr != nil {
		return "", f.ImageIDErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ImageIDs[ref], nil
}

func (f *Fake) ContainerImageID(name string) (string, error) {
	if f.ContainerImageIDErr != nil {
		return "", f.ContainerImageIDErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ContainerImageIDs[name], nil
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/docker/ -v`
Expected: PASS (including the two new tests).

- [ ] **Step 7: Commit**

```bash
git add internal/docker/docker.go internal/docker/cli.go internal/docker/fake.go internal/docker/fake_test.go
git commit -m "Added image-ID inspection to docker client"
```

---

### Task 2: Extract the shared container-spec builder

**Files:**
- Modify: `internal/app/install.go` (extract `caddyContainerSpec`, call it from `Install`)

**Interfaces:**
- Consumes: `docker.ContainerSpec`, `CaddyContainerName`, `CaddyDataVolume`, `CaddyConfigVolume`.
- Produces: `caddyContainerSpec(image string) docker.ContainerSpec` (package-private, used by `Install` and later by `Upgrade`).

This is a pure refactor: the existing install tests are the safety net.

- [ ] **Step 1: Run the install tests to capture the green baseline**

Run: `go test ./internal/app/ -run 'TestInstall' -v`
Expected: PASS.

- [ ] **Step 2: Add the spec builder**

In `internal/app/install.go`, add this function (above `Install`):

```go
// caddyContainerSpec returns the ContainerSpec for the managed Caddy
// container running the given image. Shared by install and upgrade so the
// two cannot drift.
func caddyContainerSpec(image string) docker.ContainerSpec {
	return docker.ContainerSpec{
		Name:  CaddyContainerName,
		Image: image,
		PortMappings: []docker.PortMapping{
			{HostAddr: "127.0.0.1", HostPort: 80, ContainerPort: 80},
			{HostAddr: "127.0.0.1", HostPort: 443, ContainerPort: 443},
			{HostAddr: "127.0.0.1", HostPort: 2019, ContainerPort: 2019},
		},
		Volumes: []docker.VolumeMount{
			{VolumeName: CaddyDataVolume, ContainerPath: "/data"},
			{VolumeName: CaddyConfigVolume, ContainerPath: "/config"},
		},
		// Write a minimal Caddyfile that binds the admin endpoint to
		// 0.0.0.0:2019 so the host port-forward can reach it (caddy:2's
		// default binds admin to localhost:2019, invisible from outside the
		// container). First `register` swaps the whole config via POST /load,
		// so the Caddyfile contents don't matter beyond bootstrapping the
		// admin listener.
		Command: []string{
			"sh", "-c",
			"printf '{\\n\\tadmin 0.0.0.0:2019\\n}\\n' > /etc/caddy/Caddyfile && " +
				"exec caddy run --config /etc/caddy/Caddyfile --adapter caddyfile",
		},
	}
}
```

- [ ] **Step 3: Replace the inline spec in `Install`**

In `Install`, replace the inline `spec := docker.ContainerSpec{ ... }` literal (the block currently spanning the ports, volumes, and Command) with:

```go
	spec := caddyContainerSpec(CaddyImage)
```

Leave the surrounding `EnsureVolume`/`PullImage` calls and the `CreateAndStart(spec)` rollback block exactly as they are.

- [ ] **Step 4: Run the install tests to verify unchanged behaviour**

Run: `go test ./internal/app/ -run 'TestInstall' -v`
Expected: PASS (same set as the baseline).

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/install.go
git commit -m "Extracted shared container-spec builder"
```

---

### Task 3: The `App.Upgrade` orchestrator

**Files:**
- Create: `internal/app/upgrade.go`
- Test: `internal/app/upgrade_test.go`

**Interfaces:**
- Consumes: `caddyContainerSpec` (Task 2), `docker.Client.ImageID`/`ContainerImageID` (Task 1), `a.ensureSharedNetwork`, `a.caddy.Reachable`, `a.caddy.Load`, `caddyapi.Build`, `state.Load`, `state.AcquireLock`, `state.ErrLockTimeout`, `state.IsCorrupt`.
- Produces:
  - `type UpgradeReport struct { AlreadyCurrent bool; Version string }`
  - `func (a *App) Upgrade(ctx context.Context) (UpgradeReport, error)`
  - package vars `adminReadyTimeout`, `adminReadyInterval` (overridable in tests)

- [ ] **Step 1: Write the failing tests**

Create `internal/app/upgrade_test.go`:

```go
package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// newUpgradeFixture extends newFixture with a running container, scripted
// `caddy version` output, and the image-ID maps upgrade inspects.
func newUpgradeFixture(t *testing.T, caddyStatus int) *fixture {
	t.Helper()
	f := newFixture(t, caddyStatus)
	d := f.app.docker.(*docker.Fake)
	d.Containers[CaddyContainerName] = docker.StateRunning
	d.ExecResponses["caddy_shack:caddy version"] = docker.ExecResponse{
		Stdout: []byte("v2.8.4 h1:abcdef="),
	}
	return f
}

func TestUpgradeAlreadyCurrentLeavesContainerUntouched(t *testing.T) {
	f := newUpgradeFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	d.ImageIDs[CaddyImage] = "sha256:same"
	d.ContainerImageIDs[CaddyContainerName] = "sha256:same"

	rep, err := f.app.Upgrade(context.Background())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if !rep.AlreadyCurrent {
		t.Error("expected AlreadyCurrent=true")
	}
	if rep.Version != "v2.8.4" {
		t.Errorf("Version: got %q want v2.8.4", rep.Version)
	}
	if len(d.Removed) != 0 {
		t.Errorf("container should not be removed on no-op: %v", d.Removed)
	}
}

func TestUpgradeRecreatesAndRestoresBackends(t *testing.T) {
	f := newUpgradeFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	d.ImageIDs[CaddyImage] = "sha256:new"
	d.ContainerImageIDs[CaddyContainerName] = "sha256:old"

	seed := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends: map[string]state.Backend{
			"myapp": {Domain: "myapp.test", Upstream: "host.docker.internal:3000", RegisteredAt: "2026-06-22T00:00:00Z"},
		},
	}
	if err := state.Save(f.statePath, seed); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	rep, err := f.app.Upgrade(context.Background())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if rep.AlreadyCurrent {
		t.Error("expected AlreadyCurrent=false")
	}
	if rep.Version != "v2.8.4" {
		t.Errorf("Version: got %q want v2.8.4", rep.Version)
	}
	if !d.PulledImg[CaddyImage] {
		t.Error("image was not pulled")
	}
	if len(d.Removed) != 1 || d.Removed[0] != CaddyContainerName {
		t.Errorf("Removed: got %v want [caddy_shack]", d.Removed)
	}
	if d.Spec.Image != CaddyImage {
		t.Errorf("recreated with image %q want %q", d.Spec.Image, CaddyImage)
	}
	if !d.Volumes[CaddyDataVolume] || !d.Volumes[CaddyConfigVolume] {
		t.Error("both volumes should be ensured")
	}
	if !d.NetworkConnections[CaddyNetworkName+":"+CaddyContainerName] {
		t.Error("shared network not reattached")
	}
	if !strings.Contains(string(f.lastBody), "myapp.test") {
		t.Errorf("backends not restored to Caddy: %s", f.lastBody)
	}
}

func TestUpgradeNotInstalledReturnsErrNotInstalled(t *testing.T) {
	f := newUpgradeFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	delete(d.Containers, CaddyContainerName)

	_, err := f.app.Upgrade(context.Background())
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("expected ErrNotInstalled, got %v", err)
	}
}

func TestUpgradeDaemonUnreachableReturnsErrDockerUnreachable(t *testing.T) {
	f := newUpgradeFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	d.DaemonErr = errors.New("daemon down")

	_, err := f.app.Upgrade(context.Background())
	if !errors.Is(err, ErrDockerUnreachable) {
		t.Errorf("expected ErrDockerUnreachable, got %v", err)
	}
}

func TestUpgradeCorruptStateDoesNotTouchContainer(t *testing.T) {
	f := newUpgradeFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	d.ImageIDs[CaddyImage] = "sha256:new"
	d.ContainerImageIDs[CaddyContainerName] = "sha256:old"
	if err := os.WriteFile(f.statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}

	_, err := f.app.Upgrade(context.Background())
	if !errors.Is(err, ErrStateCorrupt) {
		t.Errorf("expected ErrStateCorrupt, got %v", err)
	}
	if len(d.Removed) != 0 {
		t.Errorf("container mutated despite corrupt state: %v", d.Removed)
	}
}

func TestUpgradeReloadFailureReturnsGenericError(t *testing.T) {
	savedTimeout, savedInterval := adminReadyTimeout, adminReadyInterval
	adminReadyTimeout, adminReadyInterval = 50*time.Millisecond, 10*time.Millisecond
	defer func() { adminReadyTimeout, adminReadyInterval = savedTimeout, savedInterval }()

	f := newUpgradeFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	d.ImageIDs[CaddyImage] = "sha256:new"
	d.ContainerImageIDs[CaddyContainerName] = "sha256:old"
	f.caddySrv.Close() // admin API never comes up after the recreate

	_, err := f.app.Upgrade(context.Background())
	if err == nil {
		t.Fatal("expected an error when the admin API never becomes reachable")
	}
	if errors.Is(err, ErrNotInstalled) || errors.Is(err, ErrDockerUnreachable) ||
		errors.Is(err, ErrStateCorrupt) || errors.Is(err, ErrLockContention) {
		t.Errorf("expected a generic error, got a sentinel: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/app/ -run TestUpgrade -v`
Expected: compile failure — `Upgrade` / `UpgradeReport` / `adminReadyTimeout` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/app/upgrade.go`:

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// adminReadyTimeout and adminReadyInterval bound the wait for the Caddy
// admin API to come back up after a recreate. Package vars so tests can
// shrink them; an already-reachable API returns on the first poll.
var (
	adminReadyTimeout  = 30 * time.Second
	adminReadyInterval = 250 * time.Millisecond
)

// UpgradeReport is the outcome of an upgrade; the cmd layer renders it.
type UpgradeReport struct {
	// AlreadyCurrent is true when the running container already used the
	// freshly-pulled image, so nothing was recreated.
	AlreadyCurrent bool
	// Version is the running Caddy version (e.g. "v2.8.4"), or "" when the
	// probe was unavailable.
	Version string
}

// Upgrade re-pulls the pinned Caddy image and, if it differs from the image
// the current container runs, recreates the container against it (preserving
// both named volumes), then re-pushes the registered backends from state so
// every .test URL keeps working. If the pulled image matches what is already
// running, it reports AlreadyCurrent and leaves the container untouched.
func (a *App) Upgrade(ctx context.Context) (UpgradeReport, error) {
	if err := a.docker.DaemonReachable(); err != nil {
		return UpgradeReport{}, fmt.Errorf("%w: %v", ErrDockerUnreachable, err)
	}

	st, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return UpgradeReport{}, fmt.Errorf("inspect container: %w", err)
	}
	if st == docker.StateAbsent {
		return UpgradeReport{}, fmt.Errorf("%w: run 'caddy_shack install' first", ErrNotInstalled)
	}

	if err := a.docker.PullImage(CaddyImage); err != nil {
		return UpgradeReport{}, err
	}

	newID, err := a.docker.ImageID(CaddyImage)
	if err != nil {
		return UpgradeReport{}, fmt.Errorf("image id: %w", err)
	}
	curID, err := a.docker.ContainerImageID(CaddyContainerName)
	if err != nil {
		return UpgradeReport{}, fmt.Errorf("container image id: %w", err)
	}
	if newID == curID {
		return UpgradeReport{AlreadyCurrent: true, Version: a.caddyVersion(ctx)}, nil
	}

	lock, err := state.AcquireLock(a.paths.LockPath, 5*time.Second)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return UpgradeReport{}, fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return UpgradeReport{}, err
	}
	defer func() { _ = lock.Release() }()

	// Load backends before touching the container so a corrupt state aborts
	// without any container mutation.
	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return UpgradeReport{}, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return UpgradeReport{}, err
	}

	if err := a.docker.Remove(CaddyContainerName); err != nil {
		return UpgradeReport{}, err
	}
	if err := a.docker.EnsureVolume(CaddyDataVolume); err != nil {
		return UpgradeReport{}, err
	}
	if err := a.docker.EnsureVolume(CaddyConfigVolume); err != nil {
		return UpgradeReport{}, err
	}
	if err := a.docker.CreateAndStart(caddyContainerSpec(CaddyImage)); err != nil {
		return UpgradeReport{}, err
	}
	if err := a.ensureSharedNetwork(); err != nil {
		return UpgradeReport{}, fmt.Errorf("attach shared network: %w", err)
	}

	if err := a.waitForAdmin(ctx); err != nil {
		return UpgradeReport{}, fmt.Errorf("recreated container but the caddy admin API did not come up; run 'caddy_shack status': %w", err)
	}
	if err := a.caddy.Load(ctx, caddyapi.Build(s)); err != nil {
		return UpgradeReport{}, fmt.Errorf("recreated container but failed to restore backends; run 'caddy_shack status': %w", err)
	}

	return UpgradeReport{AlreadyCurrent: false, Version: a.caddyVersion(ctx)}, nil
}

// waitForAdmin polls the Caddy admin API until it is reachable or the bound
// elapses. Honours ctx so Ctrl+C exits promptly.
func (a *App) waitForAdmin(ctx context.Context) error {
	deadline := time.Now().Add(adminReadyTimeout)
	for {
		if a.caddy.Reachable(ctx) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not reachable within %s", adminReadyTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(adminReadyInterval):
		}
	}
}

// caddyVersion returns the running Caddy version via `caddy version` inside
// the container. Best-effort: returns "" when the exec fails or output is
// empty.
func (a *App) caddyVersion(ctx context.Context) string {
	out, err := a.docker.Exec(ctx, CaddyContainerName, []string{"caddy", "version"})
	if err != nil {
		return ""
	}
	return parseCaddyVersion(out)
}

// parseCaddyVersion takes the first whitespace-separated field of
// `caddy version` output (e.g. "v2.8.4 h1:..." -> "v2.8.4").
func parseCaddyVersion(out []byte) string {
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/app/ -run TestUpgrade -v`
Expected: PASS (all six tests).

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/upgrade.go internal/app/upgrade_test.go
git commit -m "Added Upgrade orchestrator for upgrade command"
```

---

### Task 4: The `upgrade` Cobra command

**Files:**
- Create: `cmd/upgrade.go`
- Modify: `cmd/root.go` (register the command)
- Test: `cmd/upgrade_test.go`

**Interfaces:**
- Consumes: `app.New`, `app.UpgradeReport`, `(*app.App).Upgrade`.
- Produces: `newUpgradeCmd() *cobra.Command`, `renderUpgrade(w io.Writer, rep app.UpgradeReport)`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/upgrade_test.go`:

```go
package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/app"
)

func TestRenderUpgradeAlreadyCurrentWithVersion(t *testing.T) {
	var buf bytes.Buffer
	renderUpgrade(&buf, app.UpgradeReport{AlreadyCurrent: true, Version: "v2.8.4"})
	if got := buf.String(); !strings.Contains(got, "already up to date (v2.8.4)") {
		t.Errorf("output: %q", got)
	}
}

func TestRenderUpgradeAlreadyCurrentNoVersion(t *testing.T) {
	var buf bytes.Buffer
	renderUpgrade(&buf, app.UpgradeReport{AlreadyCurrent: true})
	got := buf.String()
	if !strings.Contains(got, "already up to date") || strings.Contains(got, "(") {
		t.Errorf("output: %q", got)
	}
}

func TestRenderUpgradeUpgradedWithVersion(t *testing.T) {
	var buf bytes.Buffer
	renderUpgrade(&buf, app.UpgradeReport{Version: "v2.8.4"})
	if got := buf.String(); !strings.Contains(got, "upgraded to v2.8.4") {
		t.Errorf("output: %q", got)
	}
}

func TestRenderUpgradeUpgradedNoVersion(t *testing.T) {
	var buf bytes.Buffer
	renderUpgrade(&buf, app.UpgradeReport{})
	if got := buf.String(); !strings.Contains(got, "run caddy_shack status") {
		t.Errorf("output: %q", got)
	}
}

func TestUpgradeCommandRegistered(t *testing.T) {
	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "upgrade" {
			return
		}
	}
	t.Fatal("upgrade command not registered on root")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/ -run 'TestRenderUpgrade|TestUpgradeCommand' -v`
Expected: compile failure — `renderUpgrade` / `newUpgradeCmd` undefined.

- [ ] **Step 3: Write the command**

Create `cmd/upgrade.go`:

```go
package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Re-pull caddy:2 and recreate the container, preserving config",
		Long: `Re-pulls the caddy:2 image and, if it differs from the running image,
recreates the container against it. Both named volumes are preserved, so
certs survive, and registered backends are restored from state. No-op when
already up to date.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			rep, err := a.Upgrade(cmd.Context())
			if err != nil {
				return err
			}
			renderUpgrade(cmd.OutOrStdout(), rep)
			return nil
		},
	}
}

// renderUpgrade writes the human-readable outcome of an upgrade to w.
func renderUpgrade(w io.Writer, rep app.UpgradeReport) {
	switch {
	case rep.AlreadyCurrent && rep.Version != "":
		_, _ = fmt.Fprintf(w, "already up to date (%s)\n", rep.Version)
	case rep.AlreadyCurrent:
		_, _ = fmt.Fprintln(w, "already up to date")
	case rep.Version != "":
		_, _ = fmt.Fprintf(w, "upgraded to %s\n", rep.Version)
	default:
		_, _ = fmt.Fprintln(w, "upgrade complete; run caddy_shack status to confirm version")
	}
}
```

- [ ] **Step 4: Register the command**

In `cmd/root.go`, add the registration alongside the others (e.g. after `root.AddCommand(newRestartCmd())`):

```go
	root.AddCommand(newUpgradeCmd())
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/ -run 'TestRenderUpgrade|TestUpgradeCommand' -v`
Expected: PASS (all five tests).

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/upgrade.go cmd/root.go cmd/upgrade_test.go
git commit -m "Wired up upgrade command"
```

---

### Task 5: Document the command in the README

**Files:**
- Modify: `README.md`

**Interfaces:** none.

- [ ] **Step 1: Add `upgrade` to the container-lifecycle section**

In `README.md`, under `### Container lifecycle`, add an `upgrade` line to the code block so it reads:

```
caddy_shack start      # idempotent; errors if not installed
caddy_shack stop       # no-op when already stopped
caddy_shack restart
caddy_shack upgrade    # re-pull caddy:2; recreate only if the image changed
caddy_shack status     # --json for scripts; exit 0 healthy, exit 1 unhealthy
```

Then add a short paragraph beneath that block:

```
`upgrade` re-pulls the `caddy:2` image and, if the digest changed, recreates
the container against it. Both named volumes are preserved, so certificates
and ACME state survive, and registered backends are restored from state so
every `.test` URL keeps working. It is a no-op when already current.
```

- [ ] **Step 2: Remove the stale caveat**

In the intro section, delete the sentence:

```
CaddyShack also simplifies installing Caddy (via Docker), keeping it running,
and checking on its status. Updating the image without losing configuration
is planned for a later phase.
```

Replace it with:

```
CaddyShack also simplifies installing Caddy (via Docker), keeping it running,
upgrading it without losing configuration, and checking on its status.
```

- [ ] **Step 3: Sanity-check the docs build**

Run: `go test ./...`
Expected: PASS (no code change; confirms the tree is still green).

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "Documented upgrade command in README"
```

---

## Final verification

- [ ] Run `make pre-ci` (or `go test ./... && gofmt -l . && go vet ./...`) and confirm a clean tree.
- [ ] Confirm `caddy_shack upgrade help` prints the command's long help (bareword `help` pass in `root.go` covers it automatically).
</content>

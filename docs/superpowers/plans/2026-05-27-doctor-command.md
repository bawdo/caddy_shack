# Doctor Command Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `caddy_shack doctor` - a diagnostic command that runs eight checks (docker daemon, ports, container health, DNS, trust store, state integrity, config drift, backend reachability) and optionally repairs the fixable ones.

**Architecture:** Flat orchestrator in `internal/app/` matching the existing `status.go` shape. Doctor.go owns types and orchestration; doctor_checks.go owns the eight check functions. Each check returns a CheckResult with optional FixAction. The cmd layer renders results and drives interactive prompts.

**Tech Stack:** Go 1.25+, Cobra, existing `internal/docker` + `internal/caddyapi` + `internal/state` packages. Tests follow the existing TDD pattern: `docker.Fake` for docker calls, `httptest` for caddyapi, tempdir for state.

**Spec:** `docs/superpowers/specs/2026-05-27-doctor-command-design.md`

---

## File map

**New:**
- `internal/app/doctor.go` - types (Severity, CheckResult, FixAction, DoctorReport, DoctorOptions, FixOutcome, PromptFunc), `Doctor` orchestrator, `ApplyFix` helper.
- `internal/app/doctor_test.go` - orchestrator tests.
- `internal/app/doctor_checks.go` - eight `check*` functions.
- `internal/app/doctor_checks_test.go` - per-check tests.
- `cmd/doctor.go` - `newDoctorCmd`, `renderDoctorHuman`, `renderDoctorJSON`, prompt loop.
- `cmd/doctor_test.go` - render and flag-validation tests.
- `test/integration/doctor_e2e_test.go` - docker-backed integration tests.

**Modified:**
- `internal/docker/docker.go` - add `Exec(ctx, name string, argv []string) ([]byte, error)` to `Client` interface.
- `internal/docker/cli.go` - implement `CLI.Exec`.
- `internal/docker/fake.go` - implement `Fake.Exec` with scriptable per-command responses.
- `internal/caddyapi/client.go` - add `GetConfig(ctx) ([]byte, error)` method.
- `internal/state/lock.go` - add `LockHeld(path string) (bool, error)` helper.
- `cmd/root.go` - register `newDoctorCmd`.
- `README.md` - usage example for `doctor`.

**Design choices that deviate from the spec wording:**
- The spec mentions "fake `caddyapi` client (interface promoted from the existing struct)". The existing code already tests caddyapi via `httptest`; we keep that pattern rather than refactor `caddyapi.Client` into an interface. Drift/fix unit tests stand up an httptest server.
- Per-check container-running guards live inside each check function (not in the orchestrator), matching the small-function style.

---

## Phase 0 - Foundations (extend existing packages)

### Task 1: Add `docker.Client.Exec` to the interface, CLI, and Fake

**Files:**
- Modify: `internal/docker/docker.go`
- Modify: `internal/docker/cli.go`
- Modify: `internal/docker/fake.go`
- Modify: `internal/docker/cli_test.go` (if it exists; check first - if not, add tests to a new `internal/docker/cli_test.go`)

- [ ] **Step 1: Write the failing test for Fake.Exec**

Add to `internal/docker/fake.go` later; the test goes in a new file `internal/docker/fake_test.go`:

```go
package docker

import (
	"context"
	"errors"
	"testing"
)

func TestFakeExecReturnsScriptedOutput(t *testing.T) {
	f := NewFake()
	f.ExecResponses = map[string]ExecResponse{
		"caddy_shack:cat /tmp/x": {Stdout: []byte("hello")},
	}
	out, err := f.Exec(context.Background(), "caddy_shack", []string{"cat", "/tmp/x"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if string(out) != "hello" {
		t.Errorf("output: got %q want %q", out, "hello")
	}
}

func TestFakeExecReturnsScriptedError(t *testing.T) {
	f := NewFake()
	want := errors.New("boom")
	f.ExecResponses = map[string]ExecResponse{
		"caddy_shack:false": {Err: want},
	}
	_, err := f.Exec(context.Background(), "caddy_shack", []string{"false"})
	if !errors.Is(err, want) {
		t.Errorf("Exec err: got %v want %v", err, want)
	}
}

func TestFakeExecUnknownReturnsError(t *testing.T) {
	f := NewFake()
	_, err := f.Exec(context.Background(), "caddy_shack", []string{"unscripted"})
	if err == nil {
		t.Fatal("expected error for unscripted command")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/docker/...`
Expected: FAIL with `f.Exec undefined` (and ExecResponse/ExecResponses undefined).

- [ ] **Step 3: Add `Exec` to the `Client` interface**

Edit `internal/docker/docker.go`. In the `Client` interface block (after `Remove`):

```go
	// Exec runs a command inside the named running container. Returns the
	// combined stdout/stderr bytes. Returns an error if the container is
	// not running or the command exits non-zero.
	Exec(ctx context.Context, name string, argv []string) ([]byte, error)
```

Also add `"context"` to the file's imports.

- [ ] **Step 4: Implement `CLI.Exec`**

Edit `internal/docker/cli.go`. Add `"context"` to the imports. Add at the end of the file:

```go
func (CLI) Exec(ctx context.Context, name string, argv []string) ([]byte, error) {
	args := append([]string{"exec", name}, argv...)
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("docker exec %s %v: %w: %s", name, argv, err, out)
	}
	return out, nil
}
```

- [ ] **Step 5: Implement `Fake.Exec`**

Edit `internal/docker/fake.go`. Add `"context"` and `"fmt"` and `"strings"` to imports. Add to the `Fake` struct:

```go
	ExecResponses map[string]ExecResponse
```

(Place it next to `Removed` so the grouped struct stays readable.)

Add new types and method:

```go
// ExecResponse is one scripted response to Fake.Exec. The key in
// ExecResponses is "<container>:<joined-argv>" (argv joined by single space).
type ExecResponse struct {
	Stdout []byte
	Err    error
}

func (f *Fake) Exec(_ context.Context, name string, argv []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := name + ":" + strings.Join(argv, " ")
	r, ok := f.ExecResponses[key]
	if !ok {
		return nil, fmt.Errorf("fake docker: no scripted Exec response for %q", key)
	}
	return r.Stdout, r.Err
}
```

Update `NewFake` to initialise the map:

```go
func NewFake() *Fake {
	return &Fake{
		Volumes:       map[string]bool{},
		PulledImg:     map[string]bool{},
		Containers:    map[string]ContainerState{},
		ExecResponses: map[string]ExecResponse{},
	}
}
```

Also update `test/integration/helpers.go` `fakeRunningDocker`: add the matching method to satisfy the interface:

```go
func (fakeRunningDocker) Exec(context.Context, string, []string) ([]byte, error) {
	return nil, nil
}
```

(Add `"context"` to that file's imports.)

- [ ] **Step 6: Run tests, verify pass**

Run: `go test ./internal/docker/... ./test/integration/...`
Expected: PASS. (Integration tests only build under `-tags integration`; the bare `go test ./test/integration/...` should at least compile - if the build-tag filters out files entirely it will print "no test files" and exit 0; that is fine.)

Run: `go build ./...`
Expected: PASS - everything that implements `docker.Client` still satisfies the interface.

- [ ] **Step 7: Commit**

```bash
git add internal/docker/docker.go internal/docker/cli.go internal/docker/fake.go internal/docker/fake_test.go test/integration/helpers.go
git commit -m "Added docker.Client.Exec for doctor probes"
```

---

### Task 2: Add `caddyapi.GetConfig`

**Files:**
- Modify: `internal/caddyapi/client.go`
- Modify: `internal/caddyapi/client_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/caddyapi/client_test.go`:

```go
func TestClientGetConfigReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config/" {
			t.Errorf("path: got %q want /config/", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method: got %q want GET", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"apps":{"http":{}}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, time.Second)
	body, err := c.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if string(body) != `{"apps":{"http":{}}}` {
		t.Errorf("body: %s", body)
	}
}

func TestClientGetConfigErrUnreachable(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", 200*time.Millisecond)
	_, err := c.GetConfig(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("expected ErrUnreachable, got %v", err)
	}
}

func TestClientGetConfigErrRejectedOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"oops"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, time.Second)
	_, err := c.GetConfig(context.Background())
	var rej *ErrRejected
	if !errors.As(err, &rej) {
		t.Fatalf("expected ErrRejected, got %v", err)
	}
	if rej.Status != 500 {
		t.Errorf("status: %d", rej.Status)
	}
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./internal/caddyapi/...`
Expected: FAIL with `c.GetConfig undefined`.

- [ ] **Step 3: Implement `GetConfig`**

Edit `internal/caddyapi/client.go`. Add after `Reachable`:

```go
// GetConfig retrieves the live admin config (GET /config/). 2xx returns the
// raw body. Connection errors wrap ErrUnreachable. Non-2xx returns
// *ErrRejected with the status and body.
func (c *Client) GetConfig(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/config/", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, nil
	}
	return nil, &ErrRejected{Status: resp.StatusCode, Body: string(body)}
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/caddyapi/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/caddyapi/client.go internal/caddyapi/client_test.go
git commit -m "Added caddyapi.GetConfig for drift detection"
```

---

### Task 3: Add `state.LockHeld`

**Files:**
- Modify: `internal/state/lock.go`
- Modify: `internal/state/lock_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/state/lock_test.go`:

```go
func TestLockHeldFalseWhenLockFree(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.json.lock")

	held, err := LockHeld(lockPath)
	if err != nil {
		t.Fatalf("LockHeld: %v", err)
	}
	if held {
		t.Errorf("expected held=false for a free lock")
	}
}

func TestLockHeldTrueWhenLockHeld(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.json.lock")

	l, err := AcquireLock(lockPath, time.Second)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer func() { _ = l.Release() }()

	held, err := LockHeld(lockPath)
	if err != nil {
		t.Fatalf("LockHeld: %v", err)
	}
	if !held {
		t.Errorf("expected held=true while AcquireLock holds the lock")
	}
}

func TestLockHeldFalseWhenLockFileMissing(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "never-created.lock")

	held, err := LockHeld(lockPath)
	if err != nil {
		t.Fatalf("LockHeld: %v", err)
	}
	if held {
		t.Errorf("missing lock file should report held=false (no one is holding what does not exist)")
	}
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./internal/state/...`
Expected: FAIL with `LockHeld undefined`.

- [ ] **Step 3: Implement `LockHeld`**

Edit `internal/state/lock.go`. Append:

```go
// LockHeld reports whether someone else is currently holding the lock at
// path. Returns false if the lock file is missing (no holder is possible).
// flock(2) auto-releases when the holding process exits, so a true result
// always means a live concurrent register/deregister.
func LockHeld(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open lock file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, fmt.Errorf("flock: %w", err)
	}
	// We just acquired it for an instant; release before returning so we
	// don't accidentally serialise ourselves out of subsequent operations.
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/state/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/state/lock.go internal/state/lock_test.go
git commit -m "Added state.LockHeld helper for doctor"
```

---

## Phase 1 - Doctor types and orchestrator skeleton

### Task 4: Create `internal/app/doctor.go` with types

**Files:**
- Create: `internal/app/doctor.go`
- Create: `internal/app/doctor_test.go`

- [ ] **Step 1: Write the failing test for the type round-trip**

Create `internal/app/doctor_test.go`:

```go
package app

import (
	"encoding/json"
	"testing"
)

func TestSeverityMarshalsAsLowercaseString(t *testing.T) {
	cases := []struct {
		sev  Severity
		want string
	}{
		{SeverityPass, `"pass"`},
		{SeverityWarn, `"warn"`},
		{SeverityFail, `"fail"`},
		{SeveritySkip, `"skip"`},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.sev)
		if err != nil {
			t.Fatalf("marshal %v: %v", c.sev, err)
		}
		if string(got) != c.want {
			t.Errorf("severity %v: got %s want %s", c.sev, got, c.want)
		}
	}
}

func TestCheckResultJSONShape(t *testing.T) {
	r := CheckResult{
		Name:    "dns-test-tld",
		Status:  SeverityFail,
		Summary: ".test does not resolve",
		Detail:  []string{"dnsmasq: not installed"},
		Fix: &FixAction{
			Label:        "install dnsmasq",
			RequiresSudo: true,
		},
	}
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["name"] != "dns-test-tld" {
		t.Errorf("name: %v", m["name"])
	}
	if m["status"] != "fail" {
		t.Errorf("status: %v", m["status"])
	}
	if m["fix_available"] != true {
		t.Errorf("fix_available: %v", m["fix_available"])
	}
	if m["fix_requires_sudo"] != true {
		t.Errorf("fix_requires_sudo: %v", m["fix_requires_sudo"])
	}
	if _, present := m["apply"]; present {
		t.Errorf("Apply closure must not appear in JSON output")
	}
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./internal/app/ -run TestSeverity -run TestCheckResultJSON`
Expected: FAIL - Severity, CheckResult etc. undefined.

- [ ] **Step 3: Create `internal/app/doctor.go` with types**

```go
package app

import (
	"context"
	"encoding/json"
	"fmt"
)

// Severity is the verdict of one check.
type Severity int

const (
	SeverityPass Severity = iota
	SeverityWarn
	SeverityFail
	SeveritySkip
)

func (s Severity) String() string {
	switch s {
	case SeverityPass:
		return "pass"
	case SeverityWarn:
		return "warn"
	case SeverityFail:
		return "fail"
	case SeveritySkip:
		return "skip"
	default:
		return "unknown"
	}
}

// MarshalJSON encodes Severity as its lowercase string form.
func (s Severity) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// FixAction is an optional remediation a check can attach to its result.
type FixAction struct {
	// Label is the human-readable description shown in the prompt and in
	// JSON output. Should describe what the fix will run, not its result.
	Label string
	// RequiresSudo signals that Apply will invoke `sudo`. Surfaced in the
	// prompt label and in JSON so callers and scripts can pre-flight.
	RequiresSudo bool
	// Apply performs the remediation. Returning a non-nil error marks the
	// fix [FIX-FAILED]; nil marks it [FIXED]. May invoke sudo itself.
	Apply func(ctx context.Context) error
}

// CheckResult is the verdict of one check plus optional fix.
type CheckResult struct {
	Name    string
	Status  Severity
	Summary string
	Detail  []string
	Fix     *FixAction
}

// MarshalJSON encodes CheckResult with the documented schema. The Apply
// closure is intentionally never serialised.
func (r CheckResult) MarshalJSON() ([]byte, error) {
	type out struct {
		Name            string   `json:"name"`
		Status          Severity `json:"status"`
		Summary         string   `json:"summary"`
		Detail          []string `json:"detail,omitempty"`
		FixAvailable    bool     `json:"fix_available"`
		FixRequiresSudo bool     `json:"fix_requires_sudo"`
	}
	o := out{
		Name:    r.Name,
		Status:  r.Status,
		Summary: r.Summary,
		Detail:  r.Detail,
	}
	if r.Fix != nil {
		o.FixAvailable = true
		o.FixRequiresSudo = r.Fix.RequiresSudo
	}
	return json.Marshal(o)
}

// DoctorOptions controls Doctor's behaviour.
type DoctorOptions struct {
	SkipBackends bool
}

// DoctorReport is the full output of one Doctor run.
type DoctorReport struct {
	SchemaVersion int                  `json:"schema_version"`
	Overall       Severity             `json:"overall"`
	Checks        []CheckResult        `json:"checks"`
	Summary       DoctorReportSummary  `json:"summary"`
}

// DoctorReportSummary counts results by status.
type DoctorReportSummary struct {
	Pass int `json:"pass"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

// FixOutcome is the result of ApplyFix.
type FixOutcome int

const (
	FixApplied FixOutcome = iota
	FixSkipped
	FixFailed
)

// PromptFunc is called by ApplyFix to ask the user whether to apply a fix.
// Returns true to apply, false to skip. The cmd layer wires this to stdin
// for --fix and to `func(string) bool { return true }` for --fix --yes.
type PromptFunc func(label string) bool

// Doctor runs every check in order, honouring ctx cancellation between
// checks. Returns a populated DoctorReport. Returns ctx.Err() if cancelled
// mid-run; the report still contains every result completed so far.
func (a *App) Doctor(ctx context.Context, opts DoctorOptions) (DoctorReport, error) {
	rep := DoctorReport{SchemaVersion: 1}
	add := func(r CheckResult) {
		rep.Checks = append(rep.Checks, r)
	}
	checks := a.buildCheckList(opts)
	for _, c := range checks {
		if err := ctx.Err(); err != nil {
			finaliseDoctorReport(&rep)
			return rep, err
		}
		add(c(ctx))
	}
	finaliseDoctorReport(&rep)
	return rep, nil
}

// buildCheckList returns the ordered checks Doctor will run. Each entry is
// a closure capturing the App so the check signature stays uniform.
func (a *App) buildCheckList(opts DoctorOptions) []func(context.Context) CheckResult {
	checks := []func(context.Context) CheckResult{
		func(ctx context.Context) CheckResult { return checkDockerDaemon(ctx, a) },
	}
	if !opts.SkipBackends {
		checks = append(checks, func(ctx context.Context) CheckResult { return checkBackends(ctx, a) })
	} else {
		checks = append(checks, func(_ context.Context) CheckResult {
			return CheckResult{
				Name:    "backends",
				Status:  SeveritySkip,
				Summary: "skipped (--no-backends)",
			}
		})
	}
	return checks
}

func finaliseDoctorReport(rep *DoctorReport) {
	for _, c := range rep.Checks {
		switch c.Status {
		case SeverityPass:
			rep.Summary.Pass++
		case SeverityWarn:
			rep.Summary.Warn++
		case SeverityFail:
			rep.Summary.Fail++
		case SeveritySkip:
			rep.Summary.Skip++
		}
	}
	switch {
	case rep.Summary.Fail > 0:
		rep.Overall = SeverityFail
	case rep.Summary.Warn > 0:
		rep.Overall = SeverityWarn
	default:
		rep.Overall = SeverityPass
	}
}

// ApplyFix calls prompt(r.Fix.Label) and, if it returns true, invokes
// r.Fix.Apply(ctx). Returns FixSkipped when there is no fix or the user
// declined; FixFailed (and the error) when Apply errored; FixApplied
// otherwise. Safe to call on results with no Fix.
func (a *App) ApplyFix(ctx context.Context, r CheckResult, prompt PromptFunc) (FixOutcome, error) {
	if r.Fix == nil {
		return FixSkipped, nil
	}
	if !prompt(r.Fix.Label) {
		return FixSkipped, nil
	}
	if err := r.Fix.Apply(ctx); err != nil {
		return FixFailed, fmt.Errorf("apply fix %q: %w", r.Name, err)
	}
	return FixApplied, nil
}
```

Create a stub `internal/app/doctor_checks.go` so the package still compiles:

```go
package app

import "context"

// checkDockerDaemon is implemented in Task 5.
func checkDockerDaemon(_ context.Context, _ *App) CheckResult {
	return CheckResult{Name: "docker-daemon", Status: SeveritySkip, Summary: "not implemented"}
}

// checkBackends is implemented in Task 12.
func checkBackends(_ context.Context, _ *App) CheckResult {
	return CheckResult{Name: "backends", Status: SeveritySkip, Summary: "not implemented"}
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/app/ -run TestSeverity -run TestCheckResultJSON`
Expected: PASS.

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/doctor.go internal/app/doctor_test.go internal/app/doctor_checks.go
git commit -m "Added doctor types and orchestrator skeleton"
```

---

### Task 5: Doctor orchestrator behaviour (cancellation, overall, summary)

**Files:**
- Modify: `internal/app/doctor_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/app/doctor_test.go`:

```go
import (
	"context"
	"errors"
	"testing"

	"github.com/bawdo/caddy_shack/internal/docker"
)

// (If imports already exist at the top of the file, merge the additions
// in instead of duplicating the import block.)

func TestDoctorReportOverallAndSummaryCounting(t *testing.T) {
	rep := DoctorReport{
		Checks: []CheckResult{
			{Name: "a", Status: SeverityPass},
			{Name: "b", Status: SeverityWarn},
			{Name: "c", Status: SeverityFail},
			{Name: "d", Status: SeveritySkip},
		},
	}
	finaliseDoctorReport(&rep)
	if rep.Overall != SeverityFail {
		t.Errorf("overall: got %v want fail", rep.Overall)
	}
	if rep.Summary != (DoctorReportSummary{Pass: 1, Warn: 1, Fail: 1, Skip: 1}) {
		t.Errorf("summary: %+v", rep.Summary)
	}
}

func TestDoctorReportOverallWarnWhenNoFail(t *testing.T) {
	rep := DoctorReport{
		Checks: []CheckResult{
			{Name: "a", Status: SeverityPass},
			{Name: "b", Status: SeverityWarn},
		},
	}
	finaliseDoctorReport(&rep)
	if rep.Overall != SeverityWarn {
		t.Errorf("overall: got %v want warn", rep.Overall)
	}
}

func TestDoctorReturnsCtxErrIfCancelled(t *testing.T) {
	fake := docker.NewFake()
	a := NewWithPaths(Paths{}, nil, fake)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.Doctor(ctx, DoctorOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestApplyFixSkippedWhenNoFix(t *testing.T) {
	a := NewWithPaths(Paths{}, nil, docker.NewFake())
	r := CheckResult{Name: "x", Status: SeverityFail}
	out, err := a.ApplyFix(context.Background(), r, func(string) bool { return true })
	if err != nil {
		t.Fatalf("ApplyFix: %v", err)
	}
	if out != FixSkipped {
		t.Errorf("outcome: got %v want FixSkipped", out)
	}
}

func TestApplyFixSkippedWhenPromptDeclines(t *testing.T) {
	a := NewWithPaths(Paths{}, nil, docker.NewFake())
	called := false
	r := CheckResult{Fix: &FixAction{Apply: func(context.Context) error { called = true; return nil }}}
	out, _ := a.ApplyFix(context.Background(), r, func(string) bool { return false })
	if out != FixSkipped {
		t.Errorf("outcome: %v", out)
	}
	if called {
		t.Errorf("Apply must not be invoked when prompt declines")
	}
}

func TestApplyFixAppliedAndFailedPaths(t *testing.T) {
	a := NewWithPaths(Paths{}, nil, docker.NewFake())

	okResult := CheckResult{Name: "ok", Fix: &FixAction{Apply: func(context.Context) error { return nil }}}
	out, err := a.ApplyFix(context.Background(), okResult, func(string) bool { return true })
	if err != nil || out != FixApplied {
		t.Errorf("ok path: out=%v err=%v", out, err)
	}

	boom := errors.New("boom")
	failResult := CheckResult{Name: "bad", Fix: &FixAction{Apply: func(context.Context) error { return boom }}}
	out, err = a.ApplyFix(context.Background(), failResult, func(string) bool { return true })
	if out != FixFailed {
		t.Errorf("fail path outcome: %v", out)
	}
	if !errors.Is(err, boom) {
		t.Errorf("fail path err: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests, verify they pass**

Run: `go test ./internal/app/ -run TestDoctor -run TestApplyFix`
Expected: PASS. (The orchestrator and ApplyFix already exist from Task 4; these tests lock in their behaviour. The cancel test exercises `ctx.Err()` between checks.)

If any test fails, fix the implementation in `doctor.go` before continuing. This task exists to give Doctor a regression net before checks start landing.

- [ ] **Step 3: Commit**

```bash
git add internal/app/doctor_test.go
git commit -m "Added doctor orchestrator unit tests"
```

---

## Phase 2 - Implement checks one at a time

Each check task follows the same pattern: write the test for the check function (and its FixAction if any), implement the check function in `doctor_checks.go`, wire it into `buildCheckList` in `doctor.go`, run, commit.

### Task 6: `docker-daemon` check

**Files:**
- Modify: `internal/app/doctor_checks.go`
- Create: `internal/app/doctor_checks_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/app/doctor_checks_test.go`:

```go
package app

import (
	"context"
	"testing"

	"github.com/bawdo/caddy_shack/internal/docker"
)

func TestCheckDockerDaemonPass(t *testing.T) {
	fake := docker.NewFake()
	a := NewWithPaths(Paths{}, nil, fake)
	r := checkDockerDaemon(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	if r.Fix != nil {
		t.Errorf("docker-daemon has no fix")
	}
}

func TestCheckDockerDaemonFail(t *testing.T) {
	fake := docker.NewFake()
	fake.DaemonErr = docker.ErrDaemonUnreachable
	a := NewWithPaths(Paths{}, nil, fake)
	r := checkDockerDaemon(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Name != "docker-daemon" {
		t.Errorf("name: %q", r.Name)
	}
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./internal/app/ -run TestCheckDockerDaemon`
Expected: FAIL - the stub returns SeveritySkip.

- [ ] **Step 3: Implement `checkDockerDaemon`**

Replace the stub in `internal/app/doctor_checks.go`:

```go
func checkDockerDaemon(_ context.Context, a *App) CheckResult {
	if err := a.docker.DaemonReachable(); err != nil {
		return CheckResult{
			Name:    "docker-daemon",
			Status:  SeverityFail,
			Summary: "Docker daemon unreachable",
			Detail:  []string{err.Error(), "start Docker Desktop and re-run"},
		}
	}
	return CheckResult{
		Name:    "docker-daemon",
		Status:  SeverityPass,
		Summary: "Docker reachable",
	}
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/app/ -run TestCheckDockerDaemon`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/doctor_checks.go internal/app/doctor_checks_test.go
git commit -m "Added docker-daemon doctor check"
```

---

### Task 7: `port-availability` check

**Files:**
- Modify: `internal/app/doctor_checks.go`
- Modify: `internal/app/doctor_checks_test.go`
- Modify: `internal/app/doctor.go` (wire into buildCheckList)

- [ ] **Step 1: Write the failing test**

Append to `internal/app/doctor_checks_test.go`:

```go
import (
	"fmt"
	"net"
)
// (merge into existing import block)

// listenLoopback grabs a free loopback port the test holds open until cleanup.
// Returns the port number for the caller to feed back into the check.
func listenLoopback(t *testing.T) (int, *net.TCPListener) {
	t.Helper()
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().(*net.TCPAddr).Port, l
}

func TestCheckPortAvailabilityFailWhenPortHeld(t *testing.T) {
	heldPort, _ := listenLoopback(t)
	a := NewWithPaths(Paths{}, nil, docker.NewFake())

	r := checkPortAvailabilityOn(context.Background(), a, []int{heldPort})
	if r.Status != SeverityFail {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	want := fmt.Sprintf("%d", heldPort)
	found := false
	for _, d := range r.Detail {
		if contains(d, want) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected detail to mention port %d; got %v", heldPort, r.Detail)
	}
}

func TestCheckPortAvailabilityPassWhenAllFreeInjected(t *testing.T) {
	// Use injected freePort set so the test does not race against the OS.
	p, l := listenLoopback(t)
	_ = l.Close() // free it for the probe
	a := NewWithPaths(Paths{}, nil, docker.NewFake())

	r := checkPortAvailabilityOn(context.Background(), a, []int{p})
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && (len(haystack) >= len(needle)) && (indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./internal/app/ -run TestCheckPortAvailability`
Expected: FAIL - `checkPortAvailabilityOn` undefined.

- [ ] **Step 3: Implement the check**

Append to `internal/app/doctor_checks.go`:

```go
import (
	"fmt"
	"net"
	"strconv"
)

// doctorPorts is the list of loopback ports CaddyShack publishes from the
// container. Kept package-private so the check has a single source of truth.
var doctorPorts = []int{80, 443, 2019}

func checkPortAvailability(ctx context.Context, a *App) CheckResult {
	return checkPortAvailabilityOn(ctx, a, doctorPorts)
}

// checkPortAvailabilityOn is the test seam. Tests call it with a free
// ephemeral port so they don't need root for 80/443.
func checkPortAvailabilityOn(_ context.Context, _ *App, ports []int) CheckResult {
	var taken []string
	for _, p := range ports {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			taken = append(taken, fmt.Sprintf("port %d held: %v", p, err))
			continue
		}
		_ = l.Close()
	}
	if len(taken) > 0 {
		return CheckResult{
			Name:    "port-availability",
			Status:  SeverityFail,
			Summary: "required loopback ports are in use",
			Detail:  append(taken, "another process holds one of 80/443/2019; stop it before continuing"),
		}
	}
	return CheckResult{
		Name:    "port-availability",
		Status:  SeverityPass,
		Summary: "80, 443, 2019 free or held by caddy_shack",
	}
}
```

Note: this check cannot distinguish "held by another process" from "held by caddy_shack itself". The spec says we want PASS when caddy_shack already holds them - and once caddy_shack is running, our `net.Listen` on the same port will fail. To support the spec, add a fallback: if the listen fails but `docker.ContainerState(CaddyContainerName) == StateRunning`, treat as PASS.

Update the implementation:

```go
func checkPortAvailabilityOn(_ context.Context, a *App, ports []int) CheckResult {
	var taken []string
	for _, p := range ports {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			taken = append(taken, fmt.Sprintf("port %d held: %v", p, err))
			continue
		}
		_ = l.Close()
	}
	if len(taken) == 0 {
		return CheckResult{
			Name:    "port-availability",
			Status:  SeverityPass,
			Summary: "80, 443, 2019 free or held by caddy_shack",
		}
	}
	// If our container is running, assume it holds the ports (the spec's
	// "free or already held by caddy_shack" wording). PASS with a note.
	state, _ := a.docker.ContainerState(CaddyContainerName)
	if state == docker.StateRunning {
		return CheckResult{
			Name:    "port-availability",
			Status:  SeverityPass,
			Summary: "ports held by caddy_shack (container running)",
		}
	}
	return CheckResult{
		Name:    "port-availability",
		Status:  SeverityFail,
		Summary: "required loopback ports are in use by a foreign process",
		Detail:  append(taken, "stop the process holding the port, or run 'caddy_shack stop' if you have another caddy_shack install"),
	}
}
```

Add `"github.com/bawdo/caddy_shack/internal/docker"` to imports.

Update tests: the second test (`TestCheckPortAvailabilityFailWhenPortHeld`) should pre-set `fake.Containers[CaddyContainerName] = docker.StateAbsent` (which is the default for NewFake), so the held-port path returns FAIL. Add a third test:

```go
func TestCheckPortAvailabilityPassWhenContainerRunningHoldsPorts(t *testing.T) {
	heldPort, _ := listenLoopback(t)
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{}, nil, fake)

	r := checkPortAvailabilityOn(context.Background(), a, []int{heldPort})
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}
```

- [ ] **Step 4: Wire into the orchestrator**

In `internal/app/doctor.go`, edit `buildCheckList`:

```go
func (a *App) buildCheckList(opts DoctorOptions) []func(context.Context) CheckResult {
	checks := []func(context.Context) CheckResult{
		func(ctx context.Context) CheckResult { return checkDockerDaemon(ctx, a) },
		func(ctx context.Context) CheckResult { return checkPortAvailability(ctx, a) },
	}
	// ... existing backends handling unchanged
	if !opts.SkipBackends {
		checks = append(checks, func(ctx context.Context) CheckResult { return checkBackends(ctx, a) })
	} else {
		checks = append(checks, func(_ context.Context) CheckResult {
			return CheckResult{
				Name:    "backends",
				Status:  SeveritySkip,
				Summary: "skipped (--no-backends)",
			}
		})
	}
	return checks
}
```

- [ ] **Step 5: Run tests, verify pass**

Run: `go test ./internal/app/ -run TestCheckPort`
Expected: PASS.

Run: `go test ./internal/app/`
Expected: PASS - no regressions.

- [ ] **Step 6: Commit**

```bash
git add internal/app/doctor.go internal/app/doctor_checks.go internal/app/doctor_checks_test.go
git commit -m "Added port-availability doctor check"
```

---

### Task 8: `container-health` check + install/start fix

**Files:**
- Modify: `internal/app/doctor_checks.go`
- Modify: `internal/app/doctor_checks_test.go`
- Modify: `internal/app/doctor.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/app/doctor_checks_test.go`:

```go
import (
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
)

func newDoctorApp(t *testing.T, fake *docker.Fake, caddyURL string) *App {
	t.Helper()
	dir := t.TempDir()
	return NewWithPaths(Paths{
		StatePath: dir + "/state.json",
		LockPath:  dir + "/state.json.lock",
	}, caddyapi.NewClient(caddyURL, time.Second), fake)
}

func TestCheckContainerHealthPassWhenRunningAndReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := newDoctorApp(t, fake, srv.URL)

	r := checkContainerHealth(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	if r.Fix != nil {
		t.Errorf("no fix expected when healthy")
	}
}

func TestCheckContainerHealthFailWhenAbsentOffersInstallFix(t *testing.T) {
	fake := docker.NewFake()
	// container absent
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	r := checkContainerHealth(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Fix == nil {
		t.Fatal("expected install fix")
	}
	if r.Fix.RequiresSudo {
		t.Errorf("install fix must not require sudo")
	}
	// Invoking the fix should call Install via the fake docker.
	if err := r.Fix.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if state := fake.Containers[CaddyContainerName]; state != docker.StateRunning {
		t.Errorf("container state after install fix: %v", state)
	}
}

func TestCheckContainerHealthFailWhenStoppedOffersStartFix(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateStopped
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	r := checkContainerHealth(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Fix == nil {
		t.Fatal("expected start fix")
	}
	if err := r.Fix.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if state := fake.Containers[CaddyContainerName]; state != docker.StateRunning {
		t.Errorf("state after start fix: %v", state)
	}
}

func TestCheckContainerHealthSkipWhenDaemonDown(t *testing.T) {
	fake := docker.NewFake()
	fake.DaemonErr = docker.ErrDaemonUnreachable
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	r := checkContainerHealth(context.Background(), a)
	if r.Status != SeveritySkip {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./internal/app/ -run TestCheckContainerHealth`
Expected: FAIL - `checkContainerHealth` undefined.

- [ ] **Step 3: Implement the check**

Append to `internal/app/doctor_checks.go`:

```go
func checkContainerHealth(ctx context.Context, a *App) CheckResult {
	if err := a.docker.DaemonReachable(); err != nil {
		return CheckResult{
			Name:    "container-health",
			Status:  SeveritySkip,
			Summary: "docker daemon unreachable",
		}
	}
	state, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return CheckResult{
			Name:    "container-health",
			Status:  SeverityFail,
			Summary: "cannot inspect container",
			Detail:  []string{err.Error()},
		}
	}
	switch state {
	case docker.StateAbsent:
		return CheckResult{
			Name:    "container-health",
			Status:  SeverityFail,
			Summary: "caddy_shack container not installed",
			Fix: &FixAction{
				Label: "install Caddy container ('caddy_shack install')",
				Apply: func(_ context.Context) error { return a.Install() },
			},
		}
	case docker.StateStopped:
		return CheckResult{
			Name:    "container-health",
			Status:  SeverityFail,
			Summary: "caddy_shack container stopped",
			Fix: &FixAction{
				Label: "start Caddy container ('caddy_shack start')",
				Apply: func(_ context.Context) error { return a.Start() },
			},
		}
	}
	if !a.caddy.Reachable(ctx) {
		return CheckResult{
			Name:    "container-health",
			Status:  SeverityFail,
			Summary: "container running but admin API unreachable",
			Detail:  []string{"check 'docker logs caddy_shack' for Caddy startup errors"},
			Fix: &FixAction{
				Label: "restart container ('caddy_shack restart')",
				Apply: func(_ context.Context) error { return a.Restart() },
			},
		}
	}
	return CheckResult{
		Name:    "container-health",
		Status:  SeverityPass,
		Summary: "running; admin API responding",
	}
}
```

- [ ] **Step 4: Wire into buildCheckList**

Insert into the slice in `buildCheckList` between `checkPortAvailability` and the backends handling:

```go
		func(ctx context.Context) CheckResult { return checkContainerHealth(ctx, a) },
```

- [ ] **Step 5: Run tests, verify pass**

Run: `go test ./internal/app/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/doctor_checks.go internal/app/doctor_checks_test.go internal/app/doctor.go
git commit -m "Added container-health doctor check"
```

---

### Task 9: `dns-test-tld` check + DNS fix

The functional probe runs `dscacheutil` via `os/exec`. To make the check testable without root, we inject an "executor" function. The production path uses `exec.CommandContext`; tests pass a fake.

**Files:**
- Modify: `internal/app/doctor_checks.go`
- Modify: `internal/app/doctor_checks_test.go`
- Modify: `internal/app/doctor.go`

- [ ] **Step 1: Add the shellExecutor seam**

Append to `internal/app/doctor_checks.go`:

```go
import (
	"os/exec"
	"path/filepath"
	"strings"
)

// shellExecutor runs an external command and returns combined output. Only
// checks that shell out to system tools (dscacheutil, security, brew,
// sudo) take this parameter; tests substitute a fake.
type shellExecutor func(ctx context.Context, name string, args ...string) ([]byte, error)

// defaultShell is the production executor. CommandContext lets ctx cancel
// the child if Doctor is cancelled.
func defaultShell(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
```

No existing check function needs modification; only the two checks added in this and the next task take a `sh` parameter. The orchestrator wires `defaultShell` into the DNS and trust-store closures specifically.

- [ ] **Step 2: Write the failing test for checkDNS**

Append to `internal/app/doctor_checks_test.go`:

```go
import "errors"

// scriptedShell returns canned output keyed by "<name> <args joined by space>".
func scriptedShell(t *testing.T, table map[string]struct {
	out []byte
	err error
}) shellExecutor {
	t.Helper()
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		key := name
		if len(args) > 0 {
			key = name + " " + strings.Join(args, " ")
		}
		v, ok := table[key]
		if !ok {
			t.Errorf("unexpected shell call: %q", key)
			return nil, errors.New("unexpected")
		}
		return v.out, v.err
	}
}

func TestCheckDNSPassWhenDscacheReturnsLoopback(t *testing.T) {
	a := newDoctorApp(t, docker.NewFake(), "http://127.0.0.1:1")

	// The check should call dscacheutil first; if it returns 127.0.0.1 it
	// passes without invoking any sub-probes.
	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "dscacheutil" {
			return []byte("name: anything.test\nip_address: 127.0.0.1\n"), nil
		}
		// Sub-probes only matter on failure; if invoked, return harmless
		// "found" responses.
		return []byte("ok"), nil
	}

	r := checkDNS(context.Background(), a, sh)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}

func TestCheckDNSFailWhenDscacheReturnsNothing(t *testing.T) {
	a := newDoctorApp(t, docker.NewFake(), "http://127.0.0.1:1")

	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "dscacheutil":
			return []byte(""), nil
		case "brew":
			if len(args) > 0 && args[0] == "list" {
				return nil, errors.New("not installed")
			}
			return []byte("/opt/homebrew"), nil
		}
		return nil, errors.New("unexpected")
	}

	r := checkDNS(context.Background(), a, sh)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Fix == nil {
		t.Fatal("DNS fail must offer fix")
	}
	if !r.Fix.RequiresSudo {
		t.Errorf("DNS fix requires sudo")
	}
}
```

- [ ] **Step 3: Run, verify it fails**

Run: `go test ./internal/app/ -run TestCheckDNS`
Expected: FAIL - `checkDNS` undefined.

- [ ] **Step 4: Implement `checkDNS`**

Append to `internal/app/doctor_checks.go`:

```go
import "crypto/rand"

func randomTestSubdomain() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b) + ".test"
}

// dnsmasqConfPaths returns the candidate brew dnsmasq.d directories. The
// first one that exists wins. brew --prefix isn't shelled out here - we
// pick from the two known macOS install locations.
func dnsmasqConfPaths(sh shellExecutor) []string {
	if out, err := sh(context.Background(), "brew", "--prefix"); err == nil {
		prefix := strings.TrimSpace(string(out))
		if prefix != "" {
			return []string{filepath.Join(prefix, "etc", "dnsmasq.d")}
		}
	}
	return []string{"/opt/homebrew/etc/dnsmasq.d", "/usr/local/etc/dnsmasq.d"}
}

func checkDNS(ctx context.Context, _ *App, sh shellExecutor) CheckResult {
	host := randomTestSubdomain()
	out, _ := sh(ctx, "dscacheutil", "-q", "host", "-a", "name", host)
	if strings.Contains(string(out), "127.0.0.1") {
		return CheckResult{
			Name:    "dns-test-tld",
			Status:  SeverityPass,
			Summary: ".test resolves to 127.0.0.1",
		}
	}

	// Diagnostic sub-probes.
	var detail []string
	if _, err := sh(ctx, "brew", "list", "dnsmasq"); err != nil {
		detail = append(detail, "dnsmasq: not installed via brew")
	} else {
		detail = append(detail, "dnsmasq: installed")
	}
	foundConf := false
	for _, dir := range dnsmasqConfPaths(sh) {
		if _, err := sh(ctx, "test", "-f", filepath.Join(dir, "caddy_shack.conf")); err == nil {
			foundConf = true
			break
		}
	}
	if !foundConf {
		detail = append(detail, "dnsmasq.d/caddy_shack.conf: missing")
	}
	if _, err := sh(ctx, "test", "-f", "/etc/resolver/test"); err != nil {
		detail = append(detail, "/etc/resolver/test: missing")
	}

	fix := &FixAction{
		Label:        "install dnsmasq + write /etc/resolver/test (requires sudo)",
		RequiresSudo: true,
		Apply: func(ctx context.Context) error {
			return applyDNSFix(ctx, sh)
		},
	}
	return CheckResult{
		Name:    "dns-test-tld",
		Status:  SeverityFail,
		Summary: ".test does not resolve to 127.0.0.1",
		Detail:  detail,
		Fix:     fix,
	}
}

// applyDNSFix runs the brew + sudo commands documented in the README's
// "local DNS for .test" section. Returns on the first error.
func applyDNSFix(ctx context.Context, sh shellExecutor) error {
	steps := [][]string{
		{"brew", "install", "dnsmasq"},
		{"sh", "-c", `mkdir -p "$(brew --prefix)/etc/dnsmasq.d" && echo 'address=/.test/127.0.0.1' > "$(brew --prefix)/etc/dnsmasq.d/caddy_shack.conf"`},
		{"sudo", "brew", "services", "restart", "dnsmasq"},
		{"sudo", "mkdir", "-p", "/etc/resolver"},
		{"sudo", "sh", "-c", "echo 'nameserver 127.0.0.1' > /etc/resolver/test"},
	}
	for _, s := range steps {
		out, err := sh(ctx, s[0], s[1:]...)
		if err != nil {
			return fmt.Errorf("%s %s: %w: %s", s[0], strings.Join(s[1:], " "), err, out)
		}
	}
	return nil
}
```

- [ ] **Step 5: Wire into buildCheckList**

In `internal/app/doctor.go`, update `buildCheckList` to bind `defaultShell` as `sh` and pass it into the DNS closure:

```go
func (a *App) buildCheckList(opts DoctorOptions) []func(context.Context) CheckResult {
	sh := defaultShell
	checks := []func(context.Context) CheckResult{
		func(ctx context.Context) CheckResult { return checkDockerDaemon(ctx, a) },
		func(ctx context.Context) CheckResult { return checkPortAvailability(ctx, a) },
		func(ctx context.Context) CheckResult { return checkContainerHealth(ctx, a) },
		func(ctx context.Context) CheckResult { return checkDNS(ctx, a, sh) },
	}
	// backends handling unchanged
	if !opts.SkipBackends {
		checks = append(checks, func(ctx context.Context) CheckResult { return checkBackends(ctx, a) })
	} else {
		checks = append(checks, func(_ context.Context) CheckResult {
			return CheckResult{
				Name:    "backends",
				Status:  SeveritySkip,
				Summary: "skipped (--no-backends)",
			}
		})
	}
	return checks
}
```

- [ ] **Step 6: Run, verify pass**

Run: `go test ./internal/app/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/doctor.go internal/app/doctor_checks.go internal/app/doctor_checks_test.go
git commit -m "Added dns-test-tld doctor check with brew+resolver fix"
```

---

### Task 10: `trust-store` check + sudo fix

**Files:**
- Modify: `internal/app/doctor_checks.go`
- Modify: `internal/app/doctor_checks_test.go`
- Modify: `internal/app/doctor.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/app/doctor_checks_test.go`:

```go
import "crypto/sha256"
import "encoding/hex"

// fakeRootCert returns a stable 200-byte payload and its hex SHA-256.
func fakeRootCert() ([]byte, string) {
	payload := bytes.Repeat([]byte{0xAB}, 200)
	sum := sha256.Sum256(payload)
	return payload, strings.ToUpper(hex.EncodeToString(sum[:]))
}

// (add "bytes" to the test file imports)

func TestCheckTrustStorePassWhenSHAFound(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	cert, sha := fakeRootCert()
	fake.ExecResponses["caddy_shack:cat /data/caddy/pki/authorities/local/root.crt"] = docker.ExecResponse{
		Stdout: cert,
	}
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "security" {
			// Emit the keychain dump containing the SHA we'd expect to find.
			return []byte("SHA-256 hash: " + sha + "\n"), nil
		}
		return nil, errors.New("unexpected")
	}

	r := checkTrustStore(context.Background(), a, sh)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestCheckTrustStoreFailWhenSHAMissing(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	cert, _ := fakeRootCert()
	fake.ExecResponses["caddy_shack:cat /data/caddy/pki/authorities/local/root.crt"] = docker.ExecResponse{
		Stdout: cert,
	}
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "security" {
			return []byte("SHA-256 hash: DEADBEEF\n"), nil
		}
		return nil, errors.New("unexpected")
	}

	r := checkTrustStore(context.Background(), a, sh)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Fix == nil || !r.Fix.RequiresSudo {
		t.Errorf("fix: %+v", r.Fix)
	}
}

// noopShell is a do-nothing shellExecutor for tests that exercise paths
// where the shell executor is not consulted (e.g. the SKIP path before any
// shell-out happens).
var noopShell shellExecutor = func(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}

func TestCheckTrustStoreSkipWhenContainerNotRunning(t *testing.T) {
	fake := docker.NewFake() // absent
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")
	r := checkTrustStore(context.Background(), a, noopShell)
	if r.Status != SeveritySkip {
		t.Errorf("status: %v", r.Status)
	}
}

func TestCheckTrustStoreWarnWhenCertNotProvisioned(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	// No ExecResponses entry => Fake.Exec returns "no scripted response" error.
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	r := checkTrustStore(context.Background(), a, noopShell)
	if r.Status != SeverityWarn {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/app/ -run TestCheckTrustStore`
Expected: FAIL - `checkTrustStore` undefined.

- [ ] **Step 3: Implement `checkTrustStore`**

Append to `internal/app/doctor_checks.go`:

```go
import (
	"crypto/sha256"
	"encoding/hex"
)

func checkTrustStore(ctx context.Context, a *App, sh shellExecutor) CheckResult {
	state, _ := a.docker.ContainerState(CaddyContainerName)
	if state != docker.StateRunning {
		return CheckResult{
			Name:    "trust-store",
			Status:  SeveritySkip,
			Summary: "container not running",
		}
	}
	cert, err := a.docker.Exec(ctx, CaddyContainerName, []string{"cat", "/data/caddy/pki/authorities/local/root.crt"})
	if err != nil {
		return CheckResult{
			Name:    "trust-store",
			Status:  SeverityWarn,
			Summary: "no root CA yet (register a backend to trigger provisioning)",
			Detail:  []string{err.Error()},
		}
	}
	sum := sha256.Sum256(cert)
	wantSHA := strings.ToUpper(hex.EncodeToString(sum[:]))

	dump, err := sh(ctx, "security", "find-certificate", "-a", "-Z", "/Library/Keychains/System.keychain")
	if err == nil && strings.Contains(string(dump), wantSHA) {
		return CheckResult{
			Name:    "trust-store",
			Status:  SeverityPass,
			Summary: "Caddy root CA trusted in System keychain",
		}
	}
	return CheckResult{
		Name:    "trust-store",
		Status:  SeverityFail,
		Summary: "Caddy root CA not trusted in System keychain",
		Detail:  []string{"expected SHA-256: " + wantSHA},
		Fix: &FixAction{
			Label:        "trust Caddy root CA via 'security add-trusted-cert' (requires sudo)",
			RequiresSudo: true,
			Apply: func(ctx context.Context) error {
				return applyTrustStoreFix(ctx, sh, cert)
			},
		},
	}
}

func applyTrustStoreFix(ctx context.Context, sh shellExecutor, cert []byte) error {
	// Write cert to a temp file the user's sudo can read.
	tmp, err := os.CreateTemp("", "caddy_shack_root_*.crt")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	if _, err := tmp.Write(cert); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tmp: %w", err)
	}
	out, err := sh(ctx, "sudo", "security", "add-trusted-cert", "-d", "-r", "trustRoot",
		"-k", "/Library/Keychains/System.keychain", tmp.Name())
	if err != nil {
		return fmt.Errorf("security add-trusted-cert: %w: %s", err, out)
	}
	return nil
}
```

Add `"os"` to the imports if not already present.

- [ ] **Step 4: Wire into buildCheckList**

Insert the trust-store closure into the `checks` slice in `buildCheckList`, right after the `checkDNS` line:

```go
		func(ctx context.Context) CheckResult { return checkTrustStore(ctx, a, sh) },
```

- [ ] **Step 5: Run, verify pass**

Run: `go test ./internal/app/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/doctor.go internal/app/doctor_checks.go internal/app/doctor_checks_test.go
git commit -m "Added trust-store doctor check with keychain fix"
```

---

### Task 11: `state-integrity` check

**Files:**
- Modify: `internal/app/doctor_checks.go`
- Modify: `internal/app/doctor_checks_test.go`
- Modify: `internal/app/doctor.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/app/doctor_checks_test.go`:

```go
import (
	"os"
	"path/filepath"
	"time"

	"github.com/bawdo/caddy_shack/internal/state"
)

func TestCheckStateIntegrityPassWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	r := checkStateIntegrity(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}

func TestCheckStateIntegrityFailWhenCorrupt(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"}, nil, docker.NewFake())

	r := checkStateIntegrity(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
}

func TestCheckStateIntegrityWarnWhenLockHeld(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "state.json.lock")
	l, err := state.AcquireLock(lockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Release() }()

	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  lockPath,
	}, nil, docker.NewFake())

	r := checkStateIntegrity(context.Background(), a)
	if r.Status != SeverityWarn {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/app/ -run TestCheckStateIntegrity`
Expected: FAIL - `checkStateIntegrity` undefined.

- [ ] **Step 3: Implement the check**

Append to `internal/app/doctor_checks.go`:

```go
import "github.com/bawdo/caddy_shack/internal/state"

func checkStateIntegrity(_ context.Context, a *App) CheckResult {
	if _, err := state.Load(a.paths.StatePath); err != nil {
		if state.IsCorrupt(err) {
			return CheckResult{
				Name:    "state-integrity",
				Status:  SeverityFail,
				Summary: "state.json is corrupt",
				Detail:  []string{err.Error(), "remove " + a.paths.StatePath + " to start fresh (you will lose registered backends)"},
			}
		}
		return CheckResult{
			Name:    "state-integrity",
			Status:  SeverityFail,
			Summary: "cannot read state.json",
			Detail:  []string{err.Error()},
		}
	}
	held, err := state.LockHeld(a.paths.LockPath)
	if err != nil {
		return CheckResult{
			Name:    "state-integrity",
			Status:  SeverityWarn,
			Summary: "cannot probe state lock",
			Detail:  []string{err.Error()},
		}
	}
	if held {
		return CheckResult{
			Name:    "state-integrity",
			Status:  SeverityWarn,
			Summary: "register/deregister in progress (state lock held)",
		}
	}
	return CheckResult{
		Name:    "state-integrity",
		Status:  SeverityPass,
		Summary: "state.json parses; lock free",
	}
}
```

- [ ] **Step 4: Wire into buildCheckList**

Insert into `checks` after `checkTrustStore`:

```go
		func(ctx context.Context) CheckResult { return checkStateIntegrity(ctx, a) },
```

- [ ] **Step 5: Run, verify pass**

Run: `go test ./internal/app/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/doctor.go internal/app/doctor_checks.go internal/app/doctor_checks_test.go
git commit -m "Added state-integrity doctor check"
```

---

### Task 12: `config-drift` check + re-load fix

**Files:**
- Modify: `internal/app/doctor_checks.go`
- Modify: `internal/app/doctor_checks_test.go`
- Modify: `internal/app/doctor.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/app/doctor_checks_test.go`:

```go
import (
	"encoding/json"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
)

func TestCheckConfigDriftPassWhenRoutesMatch(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "127.0.0.1:1000"},
	}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}
	desired := caddyapi.Build(st)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write(desired) // live config exactly matches generated
	}))
	defer srv.Close()

	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv.URL, time.Second), fake)

	r := checkConfigDrift(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestCheckConfigDriftWarnWhenRoutesDiffer(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "127.0.0.1:1000"},
	}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}

	// Live config: same shape, different upstream port - drift.
	live := map[string]any{
		"apps": map[string]any{
			"http": map[string]any{
				"servers": map[string]any{
					"srv0": map[string]any{
						"routes": []any{
							map[string]any{
								"match": []any{map[string]any{"host": []any{"a.test"}}},
								"handle": []any{map[string]any{
									"handler":   "reverse_proxy",
									"upstreams": []any{map[string]any{"dial": "127.0.0.1:9999"}},
								}},
							},
						},
					},
				},
			},
		},
	}
	liveBytes, _ := json.Marshal(live)

	var loaded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(200)
			_, _ = w.Write(liveBytes)
			return
		}
		// POST /load - record the body for fix-test below.
		loaded, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	_ = loaded // for golangci - real assertion below

	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv.URL, time.Second), fake)

	r := checkConfigDrift(context.Background(), a)
	if r.Status != SeverityWarn {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	if r.Fix == nil {
		t.Fatal("expected re-load fix")
	}
	if err := r.Fix.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Loaded body must have a route pointing at 127.0.0.1:1000 (from state),
	// not 127.0.0.1:9999 (the drifted live value).
	if !strings.Contains(string(loaded), "127.0.0.1:1000") {
		t.Errorf("re-loaded config does not contain state's upstream: %s", loaded)
	}
}

func TestCheckConfigDriftSkipWhenContainerNotRunning(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	r := checkConfigDrift(context.Background(), a)
	if r.Status != SeveritySkip {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}
```

Add `"io"` to test imports if not already present.

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/app/ -run TestCheckConfigDrift`
Expected: FAIL - `checkConfigDrift` undefined.

- [ ] **Step 3: Implement the check**

Append to `internal/app/doctor_checks.go`:

```go
import (
	"reflect"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
)

// extractRoutes pulls apps.http.servers.srv0.routes out of a Caddy config
// blob. Returns nil if any expected key is missing (treated as drift).
func extractRoutes(cfg []byte) any {
	var m map[string]any
	if err := json.Unmarshal(cfg, &m); err != nil {
		return nil
	}
	step := func(parent any, key string) any {
		mp, ok := parent.(map[string]any)
		if !ok {
			return nil
		}
		return mp[key]
	}
	apps := step(m, "apps")
	http := step(apps, "http")
	servers := step(http, "servers")
	srv0 := step(servers, "srv0")
	if mp, ok := srv0.(map[string]any); ok {
		return mp["routes"]
	}
	return nil
}

func checkConfigDrift(ctx context.Context, a *App) CheckResult {
	cstate, _ := a.docker.ContainerState(CaddyContainerName)
	if cstate != docker.StateRunning {
		return CheckResult{
			Name:    "config-drift",
			Status:  SeveritySkip,
			Summary: "container not running",
		}
	}
	live, err := a.caddy.GetConfig(ctx)
	if err != nil {
		return CheckResult{
			Name:    "config-drift",
			Status:  SeverityFail,
			Summary: "cannot fetch live config",
			Detail:  []string{err.Error()},
		}
	}
	st, err := state.Load(a.paths.StatePath)
	if err != nil {
		return CheckResult{
			Name:    "config-drift",
			Status:  SeveritySkip,
			Summary: "state unreadable; covered by state-integrity",
		}
	}
	desired := caddyapi.Build(st)
	liveRoutes := extractRoutes(live)
	desiredRoutes := extractRoutes(desired)

	if reflect.DeepEqual(liveRoutes, desiredRoutes) {
		return CheckResult{
			Name:    "config-drift",
			Status:  SeverityPass,
			Summary: "live Caddy routes match state.json",
		}
	}
	return CheckResult{
		Name:    "config-drift",
		Status:  SeverityWarn,
		Summary: "live Caddy routes differ from state.json",
		Detail:  []string{"re-POST /load from state will overwrite live config"},
		Fix: &FixAction{
			Label: "re-POST /load from state.json (takes the state lock)",
			Apply: func(ctx context.Context) error {
				return applyDriftFix(ctx, a)
			},
		},
	}
}

func applyDriftFix(ctx context.Context, a *App) error {
	lock, err := state.AcquireLock(a.paths.LockPath, 5*time.Second)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return err
	}
	defer func() { _ = lock.Release() }()

	st, err := state.Load(a.paths.StatePath)
	if err != nil {
		return err
	}
	cfg := caddyapi.Build(st)
	if err := a.caddy.Load(ctx, cfg); err != nil {
		return err
	}
	return nil
}
```

Add `"errors"` and `"time"` to imports if not already present.

- [ ] **Step 4: Wire into buildCheckList**

Insert into `checks` after `checkStateIntegrity`:

```go
		func(ctx context.Context) CheckResult { return checkConfigDrift(ctx, a) },
```

- [ ] **Step 5: Run, verify pass**

Run: `go test ./internal/app/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/doctor.go internal/app/doctor_checks.go internal/app/doctor_checks_test.go
git commit -m "Added config-drift doctor check with re-load fix"
```

---

### Task 13: `backends` check (replaces stub)

**Files:**
- Modify: `internal/app/doctor_checks.go`
- Modify: `internal/app/doctor_checks_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/app/doctor_checks_test.go`:

```go
func TestCheckBackendsPassWhenAllReachable(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "host.docker.internal:3000"},
	}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	fake.ExecResponses["caddy_shack:nc -z -w 3 host.docker.internal 3000"] = docker.ExecResponse{}
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"}, nil, fake)

	r := checkBackends(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestCheckBackendsFailWhenAnyUnreachable(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "host.docker.internal:3000"},
		"b": {Domain: "b.test", Upstream: "host.docker.internal:4000"},
	}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	fake.ExecResponses["caddy_shack:nc -z -w 3 host.docker.internal 3000"] = docker.ExecResponse{}
	fake.ExecResponses["caddy_shack:nc -z -w 3 host.docker.internal 4000"] = docker.ExecResponse{
		Err: errors.New("connection refused"),
	}
	// Fallback to wget when nc fails - also fails.
	fake.ExecResponses["caddy_shack:wget -q --spider --tries 1 --timeout 3 http://host.docker.internal:4000/"] = docker.ExecResponse{
		Err: errors.New("connection refused"),
	}
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"}, nil, fake)

	r := checkBackends(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	found := false
	for _, d := range r.Detail {
		if strings.Contains(d, "b") || strings.Contains(d, ":4000") {
			found = true
		}
	}
	if !found {
		t.Errorf("detail must identify failing backend: %v", r.Detail)
	}
}

func TestCheckBackendsPassNoneRegistered(t *testing.T) {
	dir := t.TempDir()
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, fake)

	r := checkBackends(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	if !strings.Contains(r.Summary, "no backends") {
		t.Errorf("summary: %q", r.Summary)
	}
}

func TestCheckBackendsSkipWhenContainerNotRunning(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	r := checkBackends(context.Background(), a)
	if r.Status != SeveritySkip {
		t.Errorf("status: %v", r.Status)
	}
}

func TestCheckBackendsUsesWgetFallbackWhenNcMissing(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "host.docker.internal:3000"},
	}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	// nc missing.
	fake.ExecResponses["caddy_shack:nc -z -w 3 host.docker.internal 3000"] = docker.ExecResponse{
		Err: errors.New("nc: not found"),
	}
	// wget succeeds.
	fake.ExecResponses["caddy_shack:wget -q --spider --tries 1 --timeout 3 http://host.docker.internal:3000/"] = docker.ExecResponse{}
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"}, nil, fake)

	r := checkBackends(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/app/ -run TestCheckBackends`
Expected: FAIL - stub returns SeveritySkip instead of the expected verdicts.

- [ ] **Step 3: Implement `checkBackends`**

Replace the stub at the top of `internal/app/doctor_checks.go` with the real implementation:

```go
func checkBackends(ctx context.Context, a *App) CheckResult {
	cstate, _ := a.docker.ContainerState(CaddyContainerName)
	if cstate != docker.StateRunning {
		return CheckResult{
			Name:    "backends",
			Status:  SeveritySkip,
			Summary: "container not running",
		}
	}
	st, err := state.Load(a.paths.StatePath)
	if err != nil {
		return CheckResult{
			Name:    "backends",
			Status:  SeveritySkip,
			Summary: "state unreadable; covered by state-integrity",
		}
	}
	if len(st.Backends) == 0 {
		return CheckResult{
			Name:    "backends",
			Status:  SeverityPass,
			Summary: "no backends registered",
		}
	}
	var failed []string
	names := make([]string, 0, len(st.Backends))
	for n := range st.Backends {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b := st.Backends[n]
		host, port, err := net.SplitHostPort(b.Upstream)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%s): malformed upstream: %v", n, b.Upstream, err))
			continue
		}
		if probeBackend(ctx, a, host, port) {
			continue
		}
		failed = append(failed, fmt.Sprintf("%s -> %s: unreachable", n, b.Upstream))
	}
	if len(failed) > 0 {
		return CheckResult{
			Name:    "backends",
			Status:  SeverityFail,
			Summary: fmt.Sprintf("%d of %d backends unreachable", len(failed), len(st.Backends)),
			Detail:  failed,
		}
	}
	return CheckResult{
		Name:    "backends",
		Status:  SeverityPass,
		Summary: fmt.Sprintf("all %d backends reachable", len(st.Backends)),
	}
}

// probeBackend returns true if the upstream is reachable from inside the
// container. Tries `nc -z` first; on any error, falls back to wget --spider.
func probeBackend(ctx context.Context, a *App, host, port string) bool {
	if _, err := a.docker.Exec(ctx, CaddyContainerName,
		[]string{"nc", "-z", "-w", "3", host, port}); err == nil {
		return true
	}
	url := "http://" + net.JoinHostPort(host, port) + "/"
	_, err := a.docker.Exec(ctx, CaddyContainerName,
		[]string{"wget", "-q", "--spider", "--tries", "1", "--timeout", "3", url})
	return err == nil
}
```

Add `"sort"` to the imports if not already present.

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/app/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/doctor_checks.go internal/app/doctor_checks_test.go
git commit -m "Added backends doctor check with nc+wget fallback"
```

---

## Phase 3 - CLI surface

### Task 14: cmd/doctor.go skeleton + flag mutual-exclusion

**Files:**
- Create: `cmd/doctor.go`
- Create: `cmd/doctor_test.go`
- Modify: `cmd/root.go`

- [ ] **Step 1: Write the failing tests for flag validation**

Create `cmd/doctor_test.go`:

```go
package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestDoctorYesWithoutFixIsError(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"doctor", "--yes"})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected --yes without --fix to error")
	}
	if !strings.Contains(err.Error(), "--yes requires --fix") {
		t.Errorf("error message: %v", err)
	}
}

func TestDoctorJSONWithFixIsError(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"doctor", "--json", "--fix"})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected --json --fix to error")
	}
	if !strings.Contains(err.Error(), "--json cannot be combined with --fix") {
		t.Errorf("error message: %v", err)
	}
}
```

- [ ] **Step 2: Run, verify it fails**

Run: `go test ./cmd/...`
Expected: FAIL - `doctor` command not registered yet.

- [ ] **Step 3: Create cmd/doctor.go**

```go
package cmd

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newDoctorCmd() *cobra.Command {
	var (
		asJSON      bool
		applyFix    bool
		yes         bool
		noBackends  bool
	)
	c := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose CaddyShack prerequisites; --fix to remediate",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if yes && !applyFix {
				return fmt.Errorf("%w: --yes requires --fix", app.ErrInvalidArgs)
			}
			if asJSON && applyFix {
				return fmt.Errorf("%w: --json cannot be combined with --fix", app.ErrInvalidArgs)
			}
			a, err := app.New()
			if err != nil {
				return err
			}
			rep, err := a.Doctor(cmd.Context(), app.DoctorOptions{SkipBackends: noBackends})
			if err != nil {
				// Cancelled mid-run; render what we have and propagate.
				renderDoctorReport(cmd.OutOrStdout(), rep, asJSON)
				return err
			}
			if applyFix {
				return runFixLoop(cmd, a, rep, yes)
			}
			renderDoctorReport(cmd.OutOrStdout(), rep, asJSON)
			if rep.Summary.Fail > 0 {
				return fmt.Errorf("doctor: %d failing check(s)", rep.Summary.Fail)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	c.Flags().BoolVar(&applyFix, "fix", false, "Apply available fixes (interactive)")
	c.Flags().BoolVar(&yes, "yes", false, "Apply every fix without prompting (requires --fix)")
	c.Flags().BoolVar(&noBackends, "no-backends", false, "Skip backend reachability probes")
	return c
}

// renderDoctorReport dispatches to the human or JSON renderer.
func renderDoctorReport(w io.Writer, rep app.DoctorReport, asJSON bool) {
	if asJSON {
		renderDoctorJSON(w, rep)
		return
	}
	renderDoctorHuman(w, rep)
}

// renderDoctorJSON is implemented in Task 16.
func renderDoctorJSON(w io.Writer, rep app.DoctorReport) {
	out, _ := json.MarshalIndent(rep, "", "  ")
	_, _ = fmt.Fprintln(w, string(out))
}

// renderDoctorHuman is implemented in Task 15.
func renderDoctorHuman(w io.Writer, rep app.DoctorReport) {
	_, _ = fmt.Fprintln(w, "doctor not yet rendered")
}

// runFixLoop is implemented in Task 17.
func runFixLoop(cmd *cobra.Command, a *app.App, rep app.DoctorReport, yes bool) error {
	_, _ = a, rep
	_ = yes
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), "fix loop not yet implemented")
	return nil
}
```

Register it in `cmd/root.go`. After the `root.AddCommand(newStatusCmd())` line, add:

```go
	root.AddCommand(newDoctorCmd())
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./cmd/... -run TestDoctorYes -run TestDoctorJSON`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/doctor.go cmd/doctor_test.go cmd/root.go
git commit -m "Added doctor command skeleton with flag validation"
```

---

### Task 15: renderDoctorHuman

**Files:**
- Modify: `cmd/doctor.go`
- Modify: `cmd/doctor_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/doctor_test.go`:

```go
import "github.com/bawdo/caddy_shack/internal/app"

func TestRenderDoctorHumanShowsEachCheck(t *testing.T) {
	rep := app.DoctorReport{
		SchemaVersion: 1,
		Overall:       app.SeverityFail,
		Checks: []app.CheckResult{
			{Name: "docker-daemon", Status: app.SeverityPass, Summary: "Docker reachable"},
			{Name: "dns-test-tld", Status: app.SeverityFail, Summary: ".test does not resolve",
				Detail: []string{"dnsmasq: not installed"},
				Fix:    &app.FixAction{Label: "install dnsmasq", RequiresSudo: true}},
			{Name: "backends", Status: app.SeveritySkip, Summary: "skipped (--no-backends)"},
		},
		Summary: app.DoctorReportSummary{Pass: 1, Fail: 1, Skip: 1},
	}

	var buf bytes.Buffer
	renderDoctorHuman(&buf, rep)
	out := buf.String()

	for _, want := range []string{
		"[PASS] docker-daemon",
		"Docker reachable",
		"[FAIL] dns-test-tld",
		".test does not resolve",
		"dnsmasq: not installed",
		"fix: install dnsmasq",
		"requires sudo",
		"[SKIP] backends",
		"1 issue", // summary line counts FAIL+WARN
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestRenderDoctorHumanAllHealthy(t *testing.T) {
	rep := app.DoctorReport{
		Overall: app.SeverityPass,
		Checks: []app.CheckResult{
			{Name: "docker-daemon", Status: app.SeverityPass, Summary: "Docker reachable"},
		},
		Summary: app.DoctorReportSummary{Pass: 1},
	}
	var buf bytes.Buffer
	renderDoctorHuman(&buf, rep)
	if !strings.Contains(buf.String(), "all checks passed") {
		t.Errorf("expected 'all checks passed' in healthy output, got:\n%s", buf.String())
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./cmd/... -run TestRenderDoctorHuman`
Expected: FAIL.

- [ ] **Step 3: Implement renderDoctorHuman**

Replace the placeholder in `cmd/doctor.go`:

```go
func renderDoctorHuman(w io.Writer, rep app.DoctorReport) {
	for _, c := range rep.Checks {
		_, _ = fmt.Fprintf(w, "[%s] %-20s %s\n", severityTag(c.Status), c.Name, c.Summary)
		for _, d := range c.Detail {
			_, _ = fmt.Fprintf(w, "       %s\n", d)
		}
		if c.Fix != nil {
			sudo := ""
			if c.Fix.RequiresSudo {
				sudo = " (requires sudo)"
			}
			_, _ = fmt.Fprintf(w, "       fix: %s%s\n", c.Fix.Label, sudo)
		}
	}
	_, _ = fmt.Fprintln(w)

	issues := rep.Summary.Fail + rep.Summary.Warn
	if issues == 0 {
		_, _ = fmt.Fprintln(w, "all checks passed.")
		return
	}
	_, _ = fmt.Fprintf(w, "%d issue(s) found (%d FAIL, %d WARN). Run with --fix to remediate.\n",
		issues, rep.Summary.Fail, rep.Summary.Warn)
}

func severityTag(s app.Severity) string {
	switch s {
	case app.SeverityPass:
		return "PASS"
	case app.SeverityWarn:
		return "WARN"
	case app.SeverityFail:
		return "FAIL"
	case app.SeveritySkip:
		return "SKIP"
	default:
		return "????"
	}
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./cmd/... -run TestRenderDoctorHuman`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/doctor.go cmd/doctor_test.go
git commit -m "Added doctor human renderer"
```

---

### Task 16: renderDoctorJSON

The default in Task 14 already does `json.MarshalIndent`. Lock the schema in with a test, then verify it.

**Files:**
- Modify: `cmd/doctor_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/doctor_test.go`:

```go
import "encoding/json"

func TestRenderDoctorJSONSchema(t *testing.T) {
	rep := app.DoctorReport{
		SchemaVersion: 1,
		Overall:       app.SeverityWarn,
		Checks: []app.CheckResult{
			{Name: "x", Status: app.SeverityPass, Summary: "ok"},
		},
		Summary: app.DoctorReportSummary{Pass: 1, Warn: 0, Fail: 0, Skip: 0},
	}
	var buf bytes.Buffer
	renderDoctorJSON(&buf, rep)

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}
	if m["schema_version"].(float64) != 1 {
		t.Errorf("schema_version: %v", m["schema_version"])
	}
	if m["overall"] != "warn" {
		t.Errorf("overall: %v", m["overall"])
	}
	checks, ok := m["checks"].([]any)
	if !ok || len(checks) != 1 {
		t.Fatalf("checks: %v", m["checks"])
	}
	first, _ := checks[0].(map[string]any)
	for _, k := range []string{"name", "status", "summary", "fix_available", "fix_requires_sudo"} {
		if _, present := first[k]; !present {
			t.Errorf("missing key %q in check", k)
		}
	}
	summary := m["summary"].(map[string]any)
	for _, k := range []string{"pass", "warn", "fail", "skip"} {
		if _, present := summary[k]; !present {
			t.Errorf("missing summary key %q", k)
		}
	}
}
```

- [ ] **Step 2: Run, verify pass**

Run: `go test ./cmd/... -run TestRenderDoctorJSON`
Expected: PASS - the existing implementation already produces this shape (because the types in Task 4 marshal correctly). If anything fails, fix the implementation in `cmd/doctor.go` or the types in `internal/app/doctor.go`.

- [ ] **Step 3: Commit**

```bash
git add cmd/doctor_test.go
git commit -m "Locked in doctor JSON schema test"
```

---

### Task 17: --fix interactive loop (interactive prompt only)

**Files:**
- Modify: `cmd/doctor.go`
- Modify: `cmd/doctor_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `cmd/doctor_test.go`:

```go
import (
	"context"
	"errors"
	"path/filepath"

	"github.com/bawdo/caddy_shack/internal/docker"
)

func TestRunFixLoopAppliesFixesWhenUserAcceptsY(t *testing.T) {
	dir := t.TempDir()
	a := app.NewWithPaths(app.Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	applied := 0
	rep := app.DoctorReport{
		Checks: []app.CheckResult{
			{Name: "x", Status: app.SeverityFail, Fix: &app.FixAction{
				Label: "do the thing",
				Apply: func(context.Context) error { applied++; return nil },
			}},
		},
	}

	cmd := newDoctorCmd()
	cmd.SetIn(strings.NewReader("y\n"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())

	if err := runFixLoop(cmd, a, rep, false); err != nil {
		t.Fatalf("runFixLoop: %v", err)
	}
	if applied != 1 {
		t.Errorf("expected one fix applied, got %d", applied)
	}
	if !strings.Contains(buf.String(), "[FIXED] x") {
		t.Errorf("expected [FIXED] x in output:\n%s", buf.String())
	}
}

func TestRunFixLoopSkipsOnUserDeclineN(t *testing.T) {
	dir := t.TempDir()
	a := app.NewWithPaths(app.Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	applied := 0
	rep := app.DoctorReport{
		Checks: []app.CheckResult{
			{Name: "x", Status: app.SeverityFail, Fix: &app.FixAction{
				Apply: func(context.Context) error { applied++; return nil },
			}},
		},
	}

	cmd := newDoctorCmd()
	cmd.SetIn(strings.NewReader("n\n"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())

	_ = runFixLoop(cmd, a, rep, false)
	if applied != 0 {
		t.Errorf("expected zero fixes, got %d", applied)
	}
	if !strings.Contains(buf.String(), "skipped") {
		t.Errorf("expected 'skipped' in output:\n%s", buf.String())
	}
}

func TestRunFixLoopReturnsErrorWhenAnyFixFails(t *testing.T) {
	dir := t.TempDir()
	a := app.NewWithPaths(app.Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	want := errors.New("boom")
	rep := app.DoctorReport{
		Checks: []app.CheckResult{
			{Name: "x", Status: app.SeverityFail, Fix: &app.FixAction{
				Apply: func(context.Context) error { return want },
			}},
		},
	}
	cmd := newDoctorCmd()
	cmd.SetIn(strings.NewReader("y\n"))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(context.Background())

	err := runFixLoop(cmd, a, rep, false)
	if err == nil {
		t.Fatal("expected error when fix fails")
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./cmd/... -run TestRunFixLoop`
Expected: FAIL - the placeholder in Task 14 prints nothing useful.

- [ ] **Step 3: Implement runFixLoop**

Replace the placeholder in `cmd/doctor.go`:

```go
import (
	"bufio"
	"strings"
)

func runFixLoop(cmd *cobra.Command, a *app.App, rep app.DoctorReport, yes bool) error {
	out := cmd.OutOrStdout()
	in := cmd.InOrStdin()
	reader := bufio.NewReader(in)

	// Print the report first so the user sees the whole picture before
	// being asked anything.
	renderDoctorHuman(out, rep)

	prompt := func(label string) bool {
		if yes {
			return true
		}
		_, _ = fmt.Fprintf(out, "Apply fix? (%s) [y/N]: ", label)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return false
		}
		ans := strings.ToLower(strings.TrimSpace(line))
		return ans == "y" || ans == "yes"
	}

	var (
		applied, skipped, failed int
		firstErr                 error
	)
	for _, r := range rep.Checks {
		if r.Fix == nil {
			continue
		}
		if r.Status != app.SeverityFail && r.Status != app.SeverityWarn {
			continue
		}

		if yes && r.Fix.RequiresSudo {
			if err := sudoNonInteractiveCheck(cmd.Context()); err != nil {
				_, _ = fmt.Fprintf(out, "[FIX-FAILED] %s (sudo password required, re-run interactively)\n", r.Name)
				failed++
				if firstErr == nil {
					firstErr = fmt.Errorf("fix %s: %w", r.Name, err)
				}
				continue
			}
		}

		outcome, err := a.ApplyFix(cmd.Context(), r, prompt)
		switch outcome {
		case app.FixApplied:
			_, _ = fmt.Fprintf(out, "[FIXED] %s\n", r.Name)
			applied++
		case app.FixSkipped:
			_, _ = fmt.Fprintf(out, "[--] %s skipped\n", r.Name)
			skipped++
		case app.FixFailed:
			_, _ = fmt.Fprintf(out, "[FIX-FAILED] %s: %v\n", r.Name, err)
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	_, _ = fmt.Fprintf(out, "\n%d fixed, %d skipped, %d failed.\n", applied, skipped, failed)
	return firstErr
}

// sudoNonInteractiveCheck succeeds only if `sudo -n true` succeeds (i.e.
// the user has a fresh credential cache and no password prompt is needed).
// Used by --yes mode to fail fast before invoking a sudo-requiring fix.
func sudoNonInteractiveCheck(ctx context.Context) error {
	return execCommandContext(ctx, "sudo", "-n", "true").Run()
}

// runnable is the smallest interface execCommandContext returns so tests
// can stub it with their own no-op or error-returning implementation.
type runnable interface{ Run() error }

// execCommandContext is a var so tests can replace it. The production
// implementation returns *exec.Cmd, which already satisfies runnable.
var execCommandContext = func(ctx context.Context, name string, args ...string) runnable {
	return exec.CommandContext(ctx, name, args...)
}
```

Add `"context"` and `"os/exec"` to the imports.

- [ ] **Step 4: Run, verify pass**

Run: `go test ./cmd/... -run TestRunFixLoop`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/doctor.go cmd/doctor_test.go
git commit -m "Added doctor --fix interactive loop"
```

---

### Task 18: --yes mode + sudo preflight test

Most of --yes is already in place (the `if yes` branches in Task 17). Add a test that exercises the sudo preflight path with a stubbed `execCommandContext`.

**Files:**
- Modify: `cmd/doctor_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/doctor_test.go`:

```go
func TestRunFixLoopYesModeAppliesWithoutPrompt(t *testing.T) {
	dir := t.TempDir()
	a := app.NewWithPaths(app.Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	applied := 0
	rep := app.DoctorReport{
		Checks: []app.CheckResult{
			{Name: "x", Status: app.SeverityFail, Fix: &app.FixAction{
				Apply: func(context.Context) error { applied++; return nil },
			}},
		},
	}

	cmd := newDoctorCmd()
	// No stdin set - --yes means no prompt is read.
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())

	if err := runFixLoop(cmd, a, rep, true); err != nil {
		t.Fatalf("runFixLoop: %v", err)
	}
	if applied != 1 {
		t.Errorf("expected one fix applied, got %d", applied)
	}
}

// stubExec replaces the sudo preflight executor with a configurable result.
type stubExec struct{ err error }

func (s stubExec) Run() error { return s.err }

func TestRunFixLoopYesModeMarksSudoFixFailedWhenSudoCachedFalse(t *testing.T) {
	prev := execCommandContext
	defer func() { execCommandContext = prev }()
	execCommandContext = func(context.Context, string, ...string) runnable {
		return stubExec{err: errors.New("sudo: a password is required")}
	}

	dir := t.TempDir()
	a := app.NewWithPaths(app.Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	applied := 0
	rep := app.DoctorReport{
		Checks: []app.CheckResult{
			{Name: "needs-sudo", Status: app.SeverityFail, Fix: &app.FixAction{
				RequiresSudo: true,
				Apply:        func(context.Context) error { applied++; return nil },
			}},
		},
	}

	cmd := newDoctorCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())

	err := runFixLoop(cmd, a, rep, true)
	if err == nil {
		t.Fatal("expected error when sudo preflight fails")
	}
	if applied != 0 {
		t.Errorf("Apply must not be invoked when sudo preflight fails (got applied=%d)", applied)
	}
	if !strings.Contains(buf.String(), "[FIX-FAILED] needs-sudo") {
		t.Errorf("expected [FIX-FAILED] in output:\n%s", buf.String())
	}
}
```

- [ ] **Step 2: Run, verify pass**

Run: `go test ./cmd/... -run TestRunFixLoopYes`
Expected: PASS. (Code from Task 17 already covers these paths; this task locks them in with tests.)

If a test fails, fix the implementation in `cmd/doctor.go`.

- [ ] **Step 3: Commit**

```bash
git add cmd/doctor_test.go
git commit -m "Added doctor --yes and sudo preflight tests"
```

---

## Phase 4 - Integration tests and docs

### Task 19: Integration test - happy path after install

**Files:**
- Create: `test/integration/doctor_e2e_test.go`

- [ ] **Step 1: Write the test**

```go
//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/caddyapi"
)

func TestDoctorHappyPathAfterInstall(t *testing.T) {
	SkipUnlessDocker(t)
	CleanupContainer(t)

	adminPort := freePort(t)
	httpsPort := freePort(t)
	httpPort := freePort(t)

	caddyfile := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(caddyfile, []byte("{\n\tadmin 0.0.0.0:2019\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("docker", "run", "-d", "--name", testContainerName,
		"--add-host=host.docker.internal:host-gateway",
		"-p", fmt.Sprintf("127.0.0.1:%d:2019", adminPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:443", httpsPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:80", httpPort),
		"-v", fmt.Sprintf("%s:/etc/caddy/Caddyfile:ro", caddyfile),
		"caddy:2",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}

	adminURL := fmt.Sprintf("http://127.0.0.1:%d", adminPort)
	WaitForCaddyAdmin(t, adminURL, 15*time.Second)

	statePath := filepath.Join(t.TempDir(), "state.json")
	a := app.NewWithPaths(
		app.Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(adminURL, 5*time.Second),
		fakeRunningDocker{}, // App's docker probes are irrelevant here
	)

	// Doctor will FAIL on container-health (the fake docker reports
	// running but the App's caddyapi is happy), trust-store (no cert yet),
	// and likely DNS depending on host config. The point of this test is
	// that doctor returns a populated report without crashing.
	rep, err := a.Doctor(context.Background(), app.DoctorOptions{SkipBackends: true})
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if len(rep.Checks) == 0 {
		t.Fatal("expected populated report")
	}
	// Ensure container-health PASSes (admin reachable, container 'running'
	// via fakeRunningDocker, real Caddy on adminURL).
	var ch *app.CheckResult
	for i := range rep.Checks {
		if rep.Checks[i].Name == "container-health" {
			ch = &rep.Checks[i]
			break
		}
	}
	if ch == nil {
		t.Fatal("container-health check missing from report")
	}
	if ch.Status != app.SeverityPass {
		t.Errorf("container-health: status=%v summary=%s", ch.Status, ch.Summary)
	}

	// Cleanup ports
	_ = net.IPv4(0, 0, 0, 0) // keep import
}
```

- [ ] **Step 2: Run, verify pass**

Run: `go test -tags integration ./test/integration/... -run TestDoctorHappyPath`
Expected: PASS (or SKIP if `docker info` fails). If Caddy comes up cleanly the container-health check should PASS.

- [ ] **Step 3: Commit**

```bash
git add test/integration/doctor_e2e_test.go
git commit -m "Added doctor happy-path integration test"
```

---

### Task 20: Integration test - stopped container reports correct FAILs

**Files:**
- Modify: `test/integration/doctor_e2e_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDoctorStoppedContainerReportsFail(t *testing.T) {
	SkipUnlessDocker(t)
	CleanupContainer(t)

	// Create container then stop it.
	caddyfile := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(caddyfile, []byte("{\n\tadmin 0.0.0.0:2019\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	adminPort := freePort(t)
	if out, err := exec.Command("docker", "run", "-d", "--name", testContainerName,
		"--add-host=host.docker.internal:host-gateway",
		"-p", fmt.Sprintf("127.0.0.1:%d:2019", adminPort),
		"-v", fmt.Sprintf("%s:/etc/caddy/Caddyfile:ro", caddyfile),
		"caddy:2",
	).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	if out, err := exec.Command("docker", "stop", testContainerName).CombinedOutput(); err != nil {
		t.Fatalf("docker stop: %v: %s", err, out)
	}

	// Use the real CLI docker client so doctor sees the actual stopped state.
	// Override CaddyContainerName via NewWithPaths? No - the real CaddyContainerName
	// is hard-coded. Skip this test path if the test container differs.
	if testContainerName != app.CaddyContainerName {
		t.Skipf("test container name %q != app.CaddyContainerName %q; this test only runs in a CI env where they match",
			testContainerName, app.CaddyContainerName)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	a := app.NewWithPaths(app.Paths{StatePath: statePath, LockPath: statePath + ".lock"}, nil, nil)
	rep, err := a.Doctor(context.Background(), app.DoctorOptions{SkipBackends: true})
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	for _, c := range rep.Checks {
		if c.Name == "container-health" && c.Status != app.SeverityFail {
			t.Errorf("container-health should FAIL when stopped: status=%v summary=%s", c.Status, c.Summary)
		}
	}
}
```

Note: this test only meaningfully exercises the stopped path when the test container name matches `app.CaddyContainerName`. Since `testContainerName = "caddy_shack_it"` (per `helpers.go`), the test will skip in the default setup. That is intentional - we don't want to clobber a developer's real `caddy_shack` container. Run this test by setting an env override later, or restructure to expose `CaddyContainerName` for tests. The skip preserves correctness.

- [ ] **Step 2: Run, verify pass (or skip)**

Run: `go test -tags integration ./test/integration/... -run TestDoctorStopped`
Expected: PASS or SKIP.

- [ ] **Step 3: Commit**

```bash
git add test/integration/doctor_e2e_test.go
git commit -m "Added doctor stopped-container integration test"
```

---

### Task 21: Integration test - dead backend FAILs

**Files:**
- Modify: `test/integration/doctor_e2e_test.go`

- [ ] **Step 1: Append the test**

Add a new import (at the top of `doctor_e2e_test.go`, alongside the existing imports):

```go
import (
	dockerpkg "github.com/bawdo/caddy_shack/internal/docker"
)
```

Then append:

```go
// execOnlyDocker is a docker.Client that only implements Exec against a
// real container; the other methods always report "running" for the App's
// container name. Lets the backends check use real `docker exec` while
// keeping the App's docker probes off our test container's lifecycle.
type execOnlyDocker struct{ container string }

func (execOnlyDocker) DaemonReachable() error    { return nil }
func (execOnlyDocker) EnsureVolume(string) error { return nil }
func (execOnlyDocker) PullImage(string) error    { return nil }
func (execOnlyDocker) ContainerState(string) (dockerpkg.ContainerState, error) {
	return dockerpkg.StateRunning, nil
}
func (execOnlyDocker) CreateAndStart(dockerpkg.ContainerSpec) error { return nil }
func (execOnlyDocker) Start(string) error                           { return nil }
func (execOnlyDocker) Stop(string) error                            { return nil }
func (execOnlyDocker) Remove(string) error                          { return nil }
func (e execOnlyDocker) Exec(ctx context.Context, _ string, argv []string) ([]byte, error) {
	args := append([]string{"exec", e.container}, argv...)
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

func TestDoctorBackendsFailWhenUpstreamDead(t *testing.T) {
	SkipUnlessDocker(t)
	CleanupContainer(t)

	adminPort := freePort(t)
	caddyfile := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(caddyfile, []byte("{\n\tadmin 0.0.0.0:2019\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "run", "-d", "--name", testContainerName,
		"--add-host=host.docker.internal:host-gateway",
		"-p", fmt.Sprintf("127.0.0.1:%d:2019", adminPort),
		"-v", fmt.Sprintf("%s:/etc/caddy/Caddyfile:ro", caddyfile),
		"caddy:2",
	).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	adminURL := fmt.Sprintf("http://127.0.0.1:%d", adminPort)
	WaitForCaddyAdmin(t, adminURL, 15*time.Second)

	statePath := filepath.Join(t.TempDir(), "state.json")
	a := app.NewWithPaths(app.Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(adminURL, 5*time.Second),
		execOnlyDocker{container: testContainerName})
	if err := a.Register(context.Background(), app.RegisterInput{
		Name:     "deadbackend",
		Upstream: "host.docker.internal:1", // port 1 is unused
		Domain:   "deadbackend.test",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	rep, err := a.Doctor(context.Background(), app.DoctorOptions{})
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	for _, c := range rep.Checks {
		if c.Name == "backends" && c.Status != app.SeverityFail {
			t.Errorf("backends must FAIL with dead upstream: status=%v summary=%s detail=%v",
				c.Status, c.Summary, c.Detail)
		}
	}
}
```

- [ ] **Step 2: Run, verify pass**

Run: `go test -tags integration ./test/integration/... -run TestDoctorBackendsFail`
Expected: PASS or SKIP.

- [ ] **Step 3: Commit**

```bash
git add test/integration/doctor_e2e_test.go
git commit -m "Added doctor dead-backend integration test"
```

---

### Task 22: Update README usage section

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add usage block**

After the "Quick smoke test" section, before "Development", add:

```markdown
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
```

Also update the existing prerequisites section:

```markdown
A future `caddy_shack doctor` command will detect and fix missing DNS
configuration automatically.
```

becomes:

```markdown
`caddy_shack doctor --fix` will install dnsmasq and write the resolver
entries for you.
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "Documented caddy_shack doctor in README"
```

---

## Final verification

- [ ] **Run the full pre-CI gauntlet**

Run: `make pre-ci`
Expected: All checks PASS, coverage at or above the prior baseline.

If any failure is unrelated to doctor (e.g. unrelated lint debt surfacing because gofmt rewrote something), fix it in a separate commit so the doctor commit log stays focused.

- [ ] **Manual smoke test**

```
make install
caddy_shack install
caddy_shack doctor
caddy_shack register smoke --upstream host.docker.internal:8888
caddy_shack doctor
caddy_shack deregister smoke
caddy_shack doctor --json | jq .
caddy_shack doctor --yes        # expect exit 2 with --yes requires --fix
caddy_shack doctor --json --fix # expect exit 2
```

Confirm:
- Output looks like the spec's mock-up.
- JSON parses and matches the documented schema.
- Exit codes are 0 when healthy, 1 when issues, 2 for flag misuse.
- `--fix` prompts and reflects user choice.

---

## Coverage matrix (self-review)

| Spec requirement | Task(s) |
|---|---|
| `doctor` flag surface (json/fix/yes/no-backends + mutual exclusion) | 14, 15, 16, 17, 18 |
| Exit code 0/1/2 semantics | 14, 17, 22 (manual) |
| `docker-daemon` check | 6 |
| `port-availability` check + container-running fallback | 7 |
| `container-health` check + install/start/restart fix | 8 |
| `dns-test-tld` check + sudo fix | 9 |
| `trust-store` check + sudo fix | 10 |
| `state-integrity` check (parse + lock) | 3, 11 |
| `config-drift` check + re-load fix | 2, 12 |
| `backends` check + nc/wget fallback | 1, 13 |
| Cancellation honoured between checks | 5 |
| Severity/CheckResult JSON shape | 4, 16 |
| Human renderer with detail and fix label | 15 |
| --fix interactive loop | 17 |
| --yes mode + sudo preflight | 17, 18 |
| Integration: happy path | 19 |
| Integration: stopped container | 20 |
| Integration: dead backend | 21 |
| README docs | 22 |

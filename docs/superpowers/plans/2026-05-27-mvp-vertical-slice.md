# MVP Vertical Slice Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the first end-to-end usable slice of CaddyShack: install Caddy in Docker, register backends, serve them over HTTPS at `.test` URLs, and deregister them - all driven from project startup/teardown scripts.

**Architecture:** Three internal packages (`state`, `caddyapi`, `docker`) sit behind an `app` orchestration layer that one-to-one maps cobra commands to behaviour. State is a JSON file with file-lock-guarded read-modify-write; Caddy config is rendered fresh from state and pushed via `POST /load` for atomic swaps; Docker is shelled out to via the `docker` CLI. Errors map to a stable exit-code taxonomy that startup scripts can branch on.

**Tech Stack:** Go 1.25, cobra v1.10.2 (already present), Go stdlib only for HTTP and `flock(2)` syscall. No new module dependencies for unit code. Integration tests shell out to the `docker` CLI against the real Caddy image.

**Spec:** `docs/superpowers/specs/2026-05-27-mvp-vertical-slice-design.md`

---

## File Structure

```
internal/
  state/
    state.go              State struct, schema_version, load/save, validate
    state_test.go
    lock.go               flock(2) wrapper with 5s timeout
    lock_test.go
  caddyapi/
    config.go             State -> Caddy JSON config renderer (pure)
    config_test.go
    client.go             HTTP client: Load, Reachable, GetConfig
    client_test.go
    testdata/             Golden files for config_test.go
  docker/
    docker.go             Client interface
    cli.go                Concrete impl that shells out to `docker`
    fake.go               In-memory fake for app-layer tests
    cli_test.go           (integration-tagged)
  app/
    errors.go             Sentinel errors mapped to exit codes
    errors_test.go
    app.go                App struct holding state, caddy, docker deps
    install.go
    install_test.go
    lifecycle.go          Start/Stop/Restart
    lifecycle_test.go
    register.go           Register + Deregister + List
    register_test.go
    status.go
    status_test.go
cmd/
  root.go                 (modify) wire new subcommands + new classifyError
  install.go
  start.go
  stop.go
  restart.go
  status.go
  register.go
  deregister.go
  list.go
test/
  integration/
    register_e2e_test.go  Real Caddy container, end-to-end register/curl
    lock_race_test.go     Two processes register simultaneously
    helpers.go            Container lifecycle helpers
.github/workflows/
  ci.yml                  (modify) add integration job on ubuntu-latest
```

---

## Task 1: State store - file lock

**Files:**
- Create: `internal/state/lock.go`
- Test: `internal/state/lock_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/state/lock_test.go
package state

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLockAcquireAndRelease(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.json.lock")

	l, err := AcquireLock(lockPath, time.Second)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Re-acquire after release should succeed.
	l2, err := AcquireLock(lockPath, time.Second)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	_ = l2.Release()
}

func TestLockContentionTimesOut(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.json.lock")

	held, err := AcquireLock(lockPath, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer held.Release()

	start := time.Now()
	_, err = AcquireLock(lockPath, 200*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("returned too fast: %v", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("returned too slow: %v", elapsed)
	}
}

func TestLockSerialisesConcurrentAcquirers(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.json.lock")

	var wg sync.WaitGroup
	var mu sync.Mutex
	insideCount := 0
	maxInside := 0

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := AcquireLock(lockPath, 2*time.Second)
			if err != nil {
				t.Errorf("AcquireLock: %v", err)
				return
			}
			mu.Lock()
			insideCount++
			if insideCount > maxInside {
				maxInside = insideCount
			}
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
			mu.Lock()
			insideCount--
			mu.Unlock()
			_ = l.Release()
		}()
	}
	wg.Wait()

	if maxInside != 1 {
		t.Errorf("lock allowed concurrency: maxInside=%d", maxInside)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/state/ -run TestLock -v`
Expected: FAIL with "AcquireLock undefined"

- [ ] **Step 3: Implement the lock**

```go
// internal/state/lock.go
package state

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// ErrLockTimeout is returned when AcquireLock cannot obtain the lock within
// the given timeout.
var ErrLockTimeout = errors.New("state: lock already held by another process")

// Lock represents a held file lock. Release MUST be called.
type Lock struct {
	file *os.File
}

// AcquireLock blocks until it can flock(2)-lock the file at path, or the
// timeout elapses. The file is created if missing.
func AcquireLock(path string, timeout time.Duration) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &Lock{file: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("flock: %w", err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, ErrLockTimeout
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// Release unlocks and closes the underlying file.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/state/ -run TestLock -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/state/lock.go internal/state/lock_test.go
git commit -m "Added file-lock helper for state-store concurrency"
```

---

## Task 2: State store - schema, load, atomic save

**Files:**
- Create: `internal/state/state.go`
- Test: `internal/state/state_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/state/state_test.go
package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingFileReturnsEmptyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.SchemaVersion != 1 {
		t.Errorf("SchemaVersion: got %d want 1", s.SchemaVersion)
	}
	if len(s.Backends) != 0 {
		t.Errorf("Backends: got %d want 0", len(s.Backends))
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := &State{
		SchemaVersion: 1,
		Backends: map[string]Backend{
			"myapp": {
				Domain:       "myapp.test",
				Upstream:     "127.0.0.1:3000",
				RegisteredAt: "2026-05-27T11:58:00+10:00",
			},
		},
	}
	if err := Save(path, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Backends["myapp"].Upstream != "127.0.0.1:3000" {
		t.Errorf("upstream not round-tripped: %+v", got)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := &State{SchemaVersion: 1, Backends: map[string]Backend{}}
	if err := Save(path, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// No leftover temp file.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp file leaked: %v", err)
	}
}

func TestLoadCorruptFileReturnsErrCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected ErrCorrupt, got nil")
	}
	if !IsCorrupt(err) {
		t.Errorf("not classified as corrupt: %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should mention path: %v", err)
	}
}

func TestLoadUnknownSchemaVersionReturnsErrCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":999,"backends":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if !IsCorrupt(err) {
		t.Errorf("unknown schema_version should be corrupt: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/state/ -run "TestLoad|TestSave" -v`
Expected: FAIL with "Load undefined", "Save undefined", "State undefined".

- [ ] **Step 3: Implement state types and IO**

```go
// internal/state/state.go
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SchemaVersion is bumped when the on-disk JSON shape changes in a
// non-backward-compatible way.
const SchemaVersion = 1

// Backend is one registered upstream service.
type Backend struct {
	Domain       string `json:"domain"`
	Upstream     string `json:"upstream"`
	RegisteredAt string `json:"registered_at"`
}

// State is the on-disk shape of state.json.
type State struct {
	SchemaVersion int                `json:"schema_version"`
	Backends      map[string]Backend `json:"backends"`
}

// ErrCorrupt classifies a load failure as "the file is unreadable or has an
// unknown schema; require manual remediation".
type ErrCorrupt struct {
	Path   string
	Reason error
}

func (e *ErrCorrupt) Error() string {
	return fmt.Sprintf("state: %s is corrupt: %v", e.Path, e.Reason)
}

func (e *ErrCorrupt) Unwrap() error { return e.Reason }

// IsCorrupt reports whether err (or any wrapped error) is *ErrCorrupt.
func IsCorrupt(err error) bool {
	var e *ErrCorrupt
	return errors.As(err, &e)
}

// Load reads state.json. A missing file returns an empty State (schema_version=1).
// A malformed or unknown-version file returns *ErrCorrupt.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{SchemaVersion: SchemaVersion, Backends: map[string]Backend{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, &ErrCorrupt{Path: path, Reason: err}
	}
	if s.SchemaVersion != SchemaVersion {
		return nil, &ErrCorrupt{
			Path:   path,
			Reason: fmt.Errorf("unknown schema_version %d (want %d)", s.SchemaVersion, SchemaVersion),
		}
	}
	if s.Backends == nil {
		s.Backends = map[string]Backend{}
	}
	return &s, nil
}

// Save writes state.json atomically (write tmp, fsync, rename).
func Save(path string, s *State) error {
	if s.SchemaVersion == 0 {
		s.SchemaVersion = SchemaVersion
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename tmp: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/state/ -run "TestLoad|TestSave" -v`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/state/state.go internal/state/state_test.go
git commit -m "Added state JSON store with atomic save and corrupt detection"
```

---

## Task 3: Caddy config builder (state -> JSON)

**Files:**
- Create: `internal/caddyapi/config.go`
- Test: `internal/caddyapi/config_test.go`
- Create: `internal/caddyapi/testdata/empty.json`
- Create: `internal/caddyapi/testdata/one_backend.json`
- Create: `internal/caddyapi/testdata/two_backends.json`

- [ ] **Step 1: Write the golden test data files**

`internal/caddyapi/testdata/empty.json`:

```json
{
  "apps": {
    "http": {
      "servers": {
        "srv0": {
          "listen": [":443"],
          "routes": [],
          "automatic_https": {
            "disable_redirects": false
          }
        }
      }
    },
    "tls": {
      "automation": {
        "policies": [
          {
            "subjects": ["*.test"],
            "issuers": [{"module": "internal"}]
          }
        ]
      }
    }
  }
}
```

`internal/caddyapi/testdata/one_backend.json`:

```json
{
  "apps": {
    "http": {
      "servers": {
        "srv0": {
          "listen": [":443"],
          "routes": [
            {
              "match": [{"host": ["myapp.test"]}],
              "handle": [
                {
                  "handler": "reverse_proxy",
                  "upstreams": [{"dial": "127.0.0.1:3000"}]
                }
              ]
            }
          ],
          "automatic_https": {
            "disable_redirects": false
          }
        }
      }
    },
    "tls": {
      "automation": {
        "policies": [
          {
            "subjects": ["*.test"],
            "issuers": [{"module": "internal"}]
          }
        ]
      }
    }
  }
}
```

`internal/caddyapi/testdata/two_backends.json`:

```json
{
  "apps": {
    "http": {
      "servers": {
        "srv0": {
          "listen": [":443"],
          "routes": [
            {
              "match": [{"host": ["api.test"]}],
              "handle": [
                {
                  "handler": "reverse_proxy",
                  "upstreams": [{"dial": "127.0.0.1:4000"}]
                }
              ]
            },
            {
              "match": [{"host": ["web.test"]}],
              "handle": [
                {
                  "handler": "reverse_proxy",
                  "upstreams": [{"dial": "127.0.0.1:3000"}]
                }
              ]
            }
          ],
          "automatic_https": {
            "disable_redirects": false
          }
        }
      }
    },
    "tls": {
      "automation": {
        "policies": [
          {
            "subjects": ["*.test"],
            "issuers": [{"module": "internal"}]
          }
        ]
      }
    }
  }
}
```

- [ ] **Step 2: Write the failing tests**

```go
// internal/caddyapi/config_test.go
package caddyapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bawdo/caddy_shack/internal/state"
)

func TestBuildConfigEmptyState(t *testing.T) {
	s := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{}}
	got := Build(s)
	assertJSONMatchesGolden(t, got, "empty.json")
}

func TestBuildConfigOneBackend(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"myapp": {Domain: "myapp.test", Upstream: "127.0.0.1:3000"},
		},
	}
	got := Build(s)
	assertJSONMatchesGolden(t, got, "one_backend.json")
}

func TestBuildConfigTwoBackendsSortedByDomain(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"web": {Domain: "web.test", Upstream: "127.0.0.1:3000"},
			"api": {Domain: "api.test", Upstream: "127.0.0.1:4000"},
		},
	}
	got := Build(s)
	assertJSONMatchesGolden(t, got, "two_backends.json")
}

func assertJSONMatchesGolden(t *testing.T, got []byte, name string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	var gotAny, wantAny any
	if err := json.Unmarshal(got, &gotAny); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if err := json.Unmarshal(want, &wantAny); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	gotNorm, _ := json.MarshalIndent(gotAny, "", "  ")
	wantNorm, _ := json.MarshalIndent(wantAny, "", "  ")
	if string(gotNorm) != string(wantNorm) {
		t.Errorf("golden mismatch for %s\nwant:\n%s\n\ngot:\n%s", name, wantNorm, gotNorm)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/caddyapi/ -run TestBuildConfig -v`
Expected: FAIL with "Build undefined".

- [ ] **Step 4: Implement the builder**

```go
// internal/caddyapi/config.go
package caddyapi

import (
	"encoding/json"
	"sort"

	"github.com/bawdo/caddy_shack/internal/state"
)

// Build renders the full Caddy admin-API JSON config for the given State.
// Routes are sorted by domain for deterministic output (important for
// golden-file tests and predictable Caddy /load behaviour).
func Build(s *state.State) []byte {
	type upstream struct {
		Dial string `json:"dial"`
	}
	type handler struct {
		Handler   string     `json:"handler"`
		Upstreams []upstream `json:"upstreams"`
	}
	type match struct {
		Host []string `json:"host"`
	}
	type route struct {
		Match  []match   `json:"match"`
		Handle []handler `json:"handle"`
	}
	type automaticHTTPS struct {
		DisableRedirects bool `json:"disable_redirects"`
	}
	type server struct {
		Listen         []string       `json:"listen"`
		Routes         []route        `json:"routes"`
		AutomaticHTTPS automaticHTTPS `json:"automatic_https"`
	}
	type httpApp struct {
		Servers map[string]server `json:"servers"`
	}
	type issuer struct {
		Module string `json:"module"`
	}
	type policy struct {
		Subjects []string `json:"subjects"`
		Issuers  []issuer `json:"issuers"`
	}
	type automation struct {
		Policies []policy `json:"policies"`
	}
	type tlsApp struct {
		Automation automation `json:"automation"`
	}
	type apps struct {
		HTTP httpApp `json:"http"`
		TLS  tlsApp  `json:"tls"`
	}
	type config struct {
		Apps apps `json:"apps"`
	}

	domains := make([]string, 0, len(s.Backends))
	byDomain := make(map[string]state.Backend, len(s.Backends))
	for _, b := range s.Backends {
		domains = append(domains, b.Domain)
		byDomain[b.Domain] = b
	}
	sort.Strings(domains)

	routes := make([]route, 0, len(domains))
	for _, d := range domains {
		b := byDomain[d]
		routes = append(routes, route{
			Match: []match{{Host: []string{d}}},
			Handle: []handler{{
				Handler:   "reverse_proxy",
				Upstreams: []upstream{{Dial: b.Upstream}},
			}},
		})
	}

	cfg := config{
		Apps: apps{
			HTTP: httpApp{
				Servers: map[string]server{
					"srv0": {
						Listen:         []string{":443"},
						Routes:         routes,
						AutomaticHTTPS: automaticHTTPS{DisableRedirects: false},
					},
				},
			},
			TLS: tlsApp{
				Automation: automation{
					Policies: []policy{{
						Subjects: []string{"*.test"},
						Issuers:  []issuer{{Module: "internal"}},
					}},
				},
			},
		},
	}

	out, _ := json.MarshalIndent(cfg, "", "  ")
	return out
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/caddyapi/ -run TestBuildConfig -v`
Expected: PASS (3 tests)

- [ ] **Step 6: Commit**

```bash
git add internal/caddyapi/config.go internal/caddyapi/config_test.go internal/caddyapi/testdata/
git commit -m "Added Caddy config renderer with golden-file tests"
```

---

## Task 4: Caddy admin API client

**Files:**
- Create: `internal/caddyapi/client.go`
- Test: `internal/caddyapi/client_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/caddyapi/client_test.go
package caddyapi

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientLoadPostsConfig(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, time.Second)
	if err := c.Load([]byte(`{"apps":{}}`)); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if gotPath != "/load" {
		t.Errorf("path: got %q want /load", gotPath)
	}
	if gotMethod != "POST" {
		t.Errorf("method: got %q want POST", gotMethod)
	}
	if !strings.Contains(string(gotBody), `"apps":{}`) {
		t.Errorf("body not forwarded: %s", gotBody)
	}
}

func TestClientLoadReturnsErrRejectedOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad config"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, time.Second)
	err := c.Load([]byte(`{}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var rej *ErrRejected
	if !errors.As(err, &rej) {
		t.Errorf("not ErrRejected: %v", err)
	}
	if rej.Status != 400 {
		t.Errorf("Status: got %d want 400", rej.Status)
	}
	if !strings.Contains(rej.Body, "bad config") {
		t.Errorf("Body: %q", rej.Body)
	}
}

func TestClientLoadReturnsErrUnreachableOnConnFail(t *testing.T) {
	// Pick a port that is almost certainly unused.
	c := NewClient("http://127.0.0.1:1", 200*time.Millisecond)
	err := c.Load([]byte(`{}`))
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("expected ErrUnreachable, got %v", err)
	}
}

func TestClientReachableTrueOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config/" {
			t.Errorf("path: %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, time.Second)
	if !c.Reachable() {
		t.Error("expected Reachable=true")
	}
}

func TestClientReachableFalseOnNoServer(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", 200*time.Millisecond)
	if c.Reachable() {
		t.Error("expected Reachable=false")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/caddyapi/ -run TestClient -v`
Expected: FAIL with "NewClient undefined" / "ErrRejected undefined" / "ErrUnreachable undefined".

- [ ] **Step 3: Implement the client**

```go
// internal/caddyapi/client.go
package caddyapi

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrUnreachable indicates the Caddy admin API is not responding (Caddy
// container not running, port not published, etc.).
var ErrUnreachable = errors.New("caddyapi: admin API unreachable")

// ErrRejected indicates Caddy returned a non-2xx response to a config push.
// The Body field carries Caddy's error JSON so callers can surface it.
type ErrRejected struct {
	Status int
	Body   string
}

func (e *ErrRejected) Error() string {
	return fmt.Sprintf("caddyapi: admin API rejected config: %d %s", e.Status, e.Body)
}

// Client talks to the Caddy admin API.
type Client struct {
	base string
	http *http.Client
}

// NewClient builds a Client targeting baseURL (e.g. "http://127.0.0.1:2019")
// with the given per-request timeout.
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		base: baseURL,
		http: &http.Client{Timeout: timeout},
	}
}

// Load POSTs config to /load. Treats 2xx as success.
func (c *Client) Load(config []byte) error {
	req, err := http.NewRequest(http.MethodPost, c.base+"/load", bytes.NewReader(config))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return &ErrRejected{Status: resp.StatusCode, Body: string(body)}
}

// Reachable returns true when GET /config/ returns 2xx within the client's
// timeout. Used by `status`.
func (c *Client) Reachable() bool {
	resp, err := c.http.Get(c.base + "/config/")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/caddyapi/ -run TestClient -v`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/caddyapi/client.go internal/caddyapi/client_test.go
git commit -m "Added Caddy admin API client with Load and Reachable"
```

---

## Task 5: Docker layer - interface and fake

**Files:**
- Create: `internal/docker/docker.go`
- Create: `internal/docker/cli.go`
- Create: `internal/docker/fake.go`

This task introduces the Docker abstraction without integration-testing the real CLI yet. The real implementation is exercised by integration tests in Task 16. The fake makes the app-layer tests in later tasks deterministic and docker-free.

- [ ] **Step 1: Define the interface and value types**

```go
// internal/docker/docker.go
// Package docker is a thin abstraction over the docker CLI for the single
// container CaddyShack manages. The interface keeps app-layer tests off
// real docker; the concrete impl in cli.go shells out for production use.
package docker

import "errors"

// ContainerState is the lifecycle phase of CaddyShack's Caddy container.
type ContainerState int

const (
	// StateAbsent means no container with the managed name exists.
	StateAbsent ContainerState = iota
	// StateStopped means a container exists but is not running.
	StateStopped
	// StateRunning means a container exists and is running.
	StateRunning
)

func (s ContainerState) String() string {
	switch s {
	case StateAbsent:
		return "absent"
	case StateStopped:
		return "stopped"
	case StateRunning:
		return "running"
	default:
		return "unknown"
	}
}

// ErrDaemonUnreachable indicates the docker CLI could not contact the
// daemon (not installed, not running).
var ErrDaemonUnreachable = errors.New("docker: daemon unreachable")

// Client is the interface the app layer depends on.
type Client interface {
	// DaemonReachable returns nil if `docker info` succeeds.
	DaemonReachable() error
	// EnsureVolume creates the named volume if it does not exist. Idempotent.
	EnsureVolume(name string) error
	// PullImage pulls the image. Idempotent.
	PullImage(image string) error
	// ContainerState reports the lifecycle phase of the named container.
	ContainerState(name string) (ContainerState, error)
	// CreateAndStart creates the container with the given spec and starts it.
	// Errors if a container with the same name already exists.
	CreateAndStart(spec ContainerSpec) error
	// Start starts an existing (stopped) container.
	Start(name string) error
	// Stop stops a running container. No-op if already stopped.
	Stop(name string) error
}

// ContainerSpec describes the CaddyShack container CreateAndStart should run.
type ContainerSpec struct {
	Name         string
	Image        string
	PortMappings []PortMapping
	Volumes      []VolumeMount
	Command      []string
}

// PortMapping publishes a container port to a specific host address/port.
type PortMapping struct {
	HostAddr      string
	HostPort      int
	ContainerPort int
}

// VolumeMount mounts a named volume at a container path.
type VolumeMount struct {
	VolumeName    string
	ContainerPath string
}
```

- [ ] **Step 2: Implement the in-memory fake**

```go
// internal/docker/fake.go
package docker

import "sync"

// Fake is an in-memory Client for app-layer tests. Records calls and
// transitions container state per CreateAndStart/Start/Stop.
type Fake struct {
	mu sync.Mutex

	DaemonErr error
	PullErr   error

	Volumes    map[string]bool
	PulledImg  map[string]bool
	Containers map[string]ContainerState
	Spec       ContainerSpec
}

// NewFake returns a Fake with empty maps and a reachable daemon.
func NewFake() *Fake {
	return &Fake{
		Volumes:    map[string]bool{},
		PulledImg:  map[string]bool{},
		Containers: map[string]ContainerState{},
	}
}

func (f *Fake) DaemonReachable() error { return f.DaemonErr }

func (f *Fake) EnsureVolume(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Volumes[name] = true
	return nil
}

func (f *Fake) PullImage(image string) error {
	if f.PullErr != nil {
		return f.PullErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.PulledImg[image] = true
	return nil
}

func (f *Fake) ContainerState(name string) (ContainerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.Containers[name]
	if !ok {
		return StateAbsent, nil
	}
	return s, nil
}

func (f *Fake) CreateAndStart(spec ContainerSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Spec = spec
	f.Containers[spec.Name] = StateRunning
	return nil
}

func (f *Fake) Start(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Containers[name] = StateRunning
	return nil
}

func (f *Fake) Stop(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.Containers[name]; ok {
		f.Containers[name] = StateStopped
	}
	return nil
}
```

- [ ] **Step 3: Implement the CLI-shelling concrete impl**

```go
// internal/docker/cli.go
package docker

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// CLI is the production Client. Shells out to the `docker` binary on PATH.
type CLI struct{}

// NewCLI returns a CLI client. There is no constructor argument; the
// `docker` binary on PATH is used.
func NewCLI() *CLI { return &CLI{} }

func (CLI) DaemonReachable() error {
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %v", ErrDaemonUnreachable, err)
	}
	return nil
}

func (CLI) EnsureVolume(name string) error {
	// `docker volume create` is idempotent: succeeds whether or not it exists.
	cmd := exec.Command("docker", "volume", "create", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker volume create %s: %w: %s", name, err, out)
	}
	return nil
}

func (CLI) PullImage(image string) error {
	cmd := exec.Command("docker", "pull", image)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker pull %s: %w: %s", image, err, out)
	}
	return nil
}

func (CLI) ContainerState(name string) (ContainerState, error) {
	cmd := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", name)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// "No such object" => absent. exec.ExitError reflects docker's exit 1.
		var ee *exec.ExitError
		if errors.As(err, &ee) && strings.Contains(stderr.String(), "No such object") {
			return StateAbsent, nil
		}
		return StateAbsent, fmt.Errorf("docker inspect %s: %w: %s", name, err, stderr.String())
	}
	switch strings.TrimSpace(stdout.String()) {
	case "true":
		return StateRunning, nil
	case "false":
		return StateStopped, nil
	default:
		return StateAbsent, fmt.Errorf("docker inspect %s: unexpected output %q", name, stdout.String())
	}
}

func (CLI) CreateAndStart(spec ContainerSpec) error {
	args := []string{"run", "-d", "--name", spec.Name, "--restart", "unless-stopped"}
	for _, p := range spec.PortMappings {
		args = append(args, "-p",
			fmt.Sprintf("%s:%s:%s", p.HostAddr, strconv.Itoa(p.HostPort), strconv.Itoa(p.ContainerPort)))
	}
	for _, v := range spec.Volumes {
		args = append(args, "-v", fmt.Sprintf("%s:%s", v.VolumeName, v.ContainerPath))
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)
	cmd := exec.Command("docker", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker run: %w: %s", err, out)
	}
	return nil
}

func (CLI) Start(name string) error {
	cmd := exec.Command("docker", "start", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker start %s: %w: %s", name, err, out)
	}
	return nil
}

func (CLI) Stop(name string) error {
	cmd := exec.Command("docker", "stop", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		// docker stop on a stopped container exits 0, so any error is real.
		return fmt.Errorf("docker stop %s: %w: %s", name, err, out)
	}
	return nil
}
```

- [ ] **Step 4: Verify it builds**

Run: `go build ./internal/docker/`
Expected: no output (success). No tests added for the CLI impl yet; it's exercised in Task 16's integration tests.

- [ ] **Step 5: Commit**

```bash
git add internal/docker/
git commit -m "Added docker abstraction layer with CLI impl and fake"
```

---

## Task 6: App sentinel errors and exit-code mapping

**Files:**
- Create: `internal/app/errors.go`
- Test: `internal/app/errors_test.go`
- Modify: `cmd/root.go`

This task introduces the public exit-code contract documented in the spec.

- [ ] **Step 1: Write the failing tests**

```go
// internal/app/errors_test.go
package app

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"generic", errors.New("boom"), 1},
		{"invalid args", fmt.Errorf("wrap: %w", ErrInvalidArgs), 2},
		{"domain conflict", fmt.Errorf("wrap: %w", ErrDomainConflict), 3},
		{"docker unreachable", fmt.Errorf("wrap: %w", ErrDockerUnreachable), 4},
		{"caddy not running", fmt.Errorf("wrap: %w", ErrCaddyNotRunning), 5},
		{"not installed", fmt.Errorf("wrap: %w", ErrNotInstalled), 6},
		{"state corrupt", fmt.Errorf("wrap: %w", ErrStateCorrupt), 7},
		{"lock contention", fmt.Errorf("wrap: %w", ErrLockContention), 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExitCode(tc.err)
			if got != tc.want {
				t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/ -run TestExitCodeMapping -v`
Expected: FAIL with "ExitCode undefined" / sentinel errors undefined.

- [ ] **Step 3: Implement sentinels and ExitCode**

```go
// internal/app/errors.go
package app

import "errors"

// Sentinel errors. Each maps to a specific exit code in ExitCode below.
// These are part of the public CLI contract documented in caddy_shack --help.
var (
	ErrInvalidArgs       = errors.New("invalid arguments")
	ErrDomainConflict    = errors.New("domain already claimed by another backend")
	ErrDockerUnreachable = errors.New("docker daemon unreachable")
	ErrCaddyNotRunning   = errors.New("caddy container not running")
	ErrNotInstalled      = errors.New("caddy not installed")
	ErrStateCorrupt      = errors.New("state file corrupt")
	ErrLockContention    = errors.New("state lock held by another process")
)

// ExitCode maps an error to the documented exit codes.
// 0 = nil, 1 = generic failure, 2-8 = specific sentinels.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrInvalidArgs):
		return 2
	case errors.Is(err, ErrDomainConflict):
		return 3
	case errors.Is(err, ErrDockerUnreachable):
		return 4
	case errors.Is(err, ErrCaddyNotRunning):
		return 5
	case errors.Is(err, ErrNotInstalled):
		return 6
	case errors.Is(err, ErrStateCorrupt):
		return 7
	case errors.Is(err, ErrLockContention):
		return 8
	default:
		return 1
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/ -run TestExitCodeMapping -v`
Expected: PASS (9 sub-tests).

- [ ] **Step 5: Wire ExitCode into cmd/root.go**

Replace the `classifyError` function in `cmd/root.go`:

```go
// cmd/root.go (modify lines 19-26)
// classifyError maps an error to documented exit codes. Delegates to
// app.ExitCode so the sentinel-to-code mapping has a single source of truth.
func classifyError(err error) int {
	return app.ExitCode(err)
}
```

Add the import at the top of `cmd/root.go`:

```go
import (
	"context"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)
```

- [ ] **Step 6: Verify the build and existing tests still pass**

Run: `go build ./... && go test ./...`
Expected: build OK, all existing tests still PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/errors.go internal/app/errors_test.go cmd/root.go
git commit -m "Added sentinel errors with stable exit-code mapping"
```

---

## Task 7: App scaffolding (App struct, paths)

**Files:**
- Create: `internal/app/app.go`
- Create: `internal/app/paths.go`
- Test: `internal/app/app_test.go`

The `App` struct holds the three dependencies (state path, caddy client, docker client) and is constructed once per command invocation. Paths are computed from `os.UserConfigDir()` so they're predictable on macOS and don't hard-code `/Users/bawdo`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/app/app_test.go
package app

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNewAppWithOverrides(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, nil)
	if a == nil {
		t.Fatal("nil app")
	}
	if a.paths.StatePath == "" {
		t.Error("StatePath not propagated")
	}
}

func TestDefaultPathsUnderUserConfigDir(t *testing.T) {
	p, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if !strings.HasSuffix(p.StatePath, "/caddy_shack/state.json") {
		t.Errorf("StatePath: %s", p.StatePath)
	}
	if !strings.HasSuffix(p.LockPath, "/caddy_shack/state.json.lock") {
		t.Errorf("LockPath: %s", p.LockPath)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/ -run "TestNewApp|TestDefaultPaths" -v`
Expected: FAIL with undefined identifiers.

- [ ] **Step 3: Implement Paths and App**

```go
// internal/app/paths.go
package app

import (
	"fmt"
	"os"
	"path/filepath"
)

// Paths bundles every filesystem path the app touches.
type Paths struct {
	StatePath string
	LockPath  string
}

// DefaultPaths returns the macOS paths under os.UserConfigDir().
// On macOS that's "~/Library/Application Support".
func DefaultPaths() (Paths, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, fmt.Errorf("user config dir: %w", err)
	}
	dir := filepath.Join(base, "caddy_shack")
	return Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil
}
```

```go
// internal/app/app.go
package app

import (
	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
)

// CaddyContainerName is the fixed container name CaddyShack manages.
const CaddyContainerName = "caddy_shack"

// CaddyImage is the pinned Caddy image tag.
const CaddyImage = "caddy:2"

// CaddyDataVolume holds certs and ACME state.
const CaddyDataVolume = "caddy_shack_data"

// CaddyConfigVolume holds Caddy's autosaved config.
const CaddyConfigVolume = "caddy_shack_config"

// App is the orchestrator that backs every CLI command.
type App struct {
	paths  Paths
	caddy  *caddyapi.Client
	docker docker.Client
}

// New builds an App with default paths, a real Caddy client at the loopback
// admin port, and the real docker CLI client.
func New() (*App, error) {
	p, err := DefaultPaths()
	if err != nil {
		return nil, err
	}
	return NewWithPaths(p, nil, nil), nil
}

// NewWithPaths is the test-friendly constructor. Passing nil for caddy or
// docker substitutes the production defaults; tests pass their own.
func NewWithPaths(p Paths, caddy *caddyapi.Client, dock docker.Client) *App {
	if caddy == nil {
		caddy = caddyapi.NewClient("http://127.0.0.1:2019", 0)
	}
	if dock == nil {
		dock = docker.NewCLI()
	}
	return &App{paths: p, caddy: caddy, docker: dock}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/ -run "TestNewApp|TestDefaultPaths" -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/app/app.go internal/app/paths.go internal/app/app_test.go
git commit -m "Added app orchestrator scaffolding and default paths"
```

---

## Task 8: App.Install + CLI install

**Files:**
- Create: `internal/app/install.go`
- Test: `internal/app/install_test.go`
- Create: `cmd/install.go`
- Modify: `cmd/root.go` (add subcommand)

- [ ] **Step 1: Write the failing tests**

```go
// internal/app/install_test.go
package app

import (
	"errors"
	"testing"

	"github.com/bawdo/caddy_shack/internal/docker"
)

func TestInstallCreatesVolumesPullsImageAndStartsContainer(t *testing.T) {
	fake := docker.NewFake()
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if !fake.Volumes[CaddyDataVolume] {
		t.Errorf("data volume not created")
	}
	if !fake.Volumes[CaddyConfigVolume] {
		t.Errorf("config volume not created")
	}
	if !fake.PulledImg[CaddyImage] {
		t.Errorf("image not pulled")
	}
	state, _ := fake.ContainerState(CaddyContainerName)
	if state != docker.StateRunning {
		t.Errorf("container state: got %v want running", state)
	}
	// Spec sanity: published ports include 80/443/2019 on loopback.
	wantPorts := map[int]bool{80: false, 443: false, 2019: false}
	for _, p := range fake.Spec.PortMappings {
		if p.HostAddr != "127.0.0.1" {
			t.Errorf("port %d not bound to loopback: %s", p.HostPort, p.HostAddr)
		}
		if _, ok := wantPorts[p.HostPort]; ok {
			wantPorts[p.HostPort] = true
		}
	}
	for port, seen := range wantPorts {
		if !seen {
			t.Errorf("port %d not published", port)
		}
	}
}

func TestInstallIsNoOpWhenAlreadyRunning(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// PullImage and CreateAndStart should not have run.
	if fake.PulledImg[CaddyImage] {
		t.Error("image pulled despite already-installed container")
	}
}

func TestInstallStartsStoppedContainer(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateStopped
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	state, _ := fake.ContainerState(CaddyContainerName)
	if state != docker.StateRunning {
		t.Errorf("container state: got %v want running", state)
	}
}

func TestInstallReturnsErrDockerUnreachableWhenDaemonDown(t *testing.T) {
	fake := docker.NewFake()
	fake.DaemonErr = docker.ErrDaemonUnreachable
	a := NewWithPaths(Paths{}, nil, fake)

	err := a.Install()
	if !errors.Is(err, ErrDockerUnreachable) {
		t.Errorf("expected ErrDockerUnreachable, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/ -run TestInstall -v`
Expected: FAIL with "Install undefined".

- [ ] **Step 3: Implement Install**

```go
// internal/app/install.go
package app

import (
	"errors"
	"fmt"

	"github.com/bawdo/caddy_shack/internal/docker"
)

// Install ensures Caddy is installed and running. Idempotent: a no-op if
// the container is already running; starts it if stopped; creates from
// scratch if absent.
func (a *App) Install() error {
	if err := a.docker.DaemonReachable(); err != nil {
		return fmt.Errorf("%w: %v", ErrDockerUnreachable, err)
	}

	state, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}

	switch state {
	case docker.StateRunning:
		return nil
	case docker.StateStopped:
		return a.docker.Start(CaddyContainerName)
	case docker.StateAbsent:
		// fall through
	}

	if err := a.docker.EnsureVolume(CaddyDataVolume); err != nil {
		return err
	}
	if err := a.docker.EnsureVolume(CaddyConfigVolume); err != nil {
		return err
	}
	if err := a.docker.PullImage(CaddyImage); err != nil {
		return err
	}
	spec := docker.ContainerSpec{
		Name:  CaddyContainerName,
		Image: CaddyImage,
		PortMappings: []docker.PortMapping{
			{HostAddr: "127.0.0.1", HostPort: 80, ContainerPort: 80},
			{HostAddr: "127.0.0.1", HostPort: 443, ContainerPort: 443},
			{HostAddr: "127.0.0.1", HostPort: 2019, ContainerPort: 2019},
		},
		Volumes: []docker.VolumeMount{
			{VolumeName: CaddyDataVolume, ContainerPath: "/data"},
			{VolumeName: CaddyConfigVolume, ContainerPath: "/config"},
		},
		// Start with an empty admin-enabled config; first register populates it.
		Command: []string{"caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"},
	}
	if err := a.docker.CreateAndStart(spec); err != nil {
		var rejErr error
		if errors.As(err, &rejErr) {
			// Surface as generic so cmd layer prints it and exits 1.
		}
		return err
	}
	return nil
}
```

> Note: the default `caddy:2` image starts with an empty Caddyfile and exposes
> the admin API on `:2019`. First `register` swaps the whole config via
> `POST /load`, so the Caddyfile contents don't matter beyond bootstrapping.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/ -run TestInstall -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Write the CLI command and wire it in**

```go
// cmd/install.go
package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install and start the local Caddy container",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			return a.Install()
		},
	}
}
```

Modify `cmd/root.go` to register the subcommand. Edit the `newRootCmd` function:

```go
// cmd/root.go - inside newRootCmd, after root.AddCommand(newVersionCmd()):
root.AddCommand(newInstallCmd())
```

- [ ] **Step 6: Verify the wiring**

Run: `go build ./... && go test ./...`
Expected: build OK, all tests PASS. `./bin/caddy_shack install --help` (after `make build`) prints the new command.

- [ ] **Step 7: Commit**

```bash
git add internal/app/install.go internal/app/install_test.go cmd/install.go cmd/root.go
git commit -m "Added install command for the Caddy container"
```

---

## Task 9: App lifecycle (Start/Stop/Restart) + CLI commands

**Files:**
- Create: `internal/app/lifecycle.go`
- Test: `internal/app/lifecycle_test.go`
- Create: `cmd/start.go`, `cmd/stop.go`, `cmd/restart.go`
- Modify: `cmd/root.go` (register subcommands)

- [ ] **Step 1: Write the failing tests**

```go
// internal/app/lifecycle_test.go
package app

import (
	"errors"
	"testing"

	"github.com/bawdo/caddy_shack/internal/docker"
)

func TestStartReturnsErrNotInstalledWhenAbsent(t *testing.T) {
	fake := docker.NewFake()
	a := NewWithPaths(Paths{}, nil, fake)
	err := a.Start()
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("expected ErrNotInstalled, got %v", err)
	}
}

func TestStartStartsStoppedContainer(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateStopped
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	state, _ := fake.ContainerState(CaddyContainerName)
	if state != docker.StateRunning {
		t.Errorf("state: %v", state)
	}
}

func TestStartIsNoOpWhenRunning(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func TestStopIsNoOpWhenAbsent(t *testing.T) {
	fake := docker.NewFake()
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Stop(); err != nil {
		t.Errorf("expected nil err on absent stop, got %v", err)
	}
}

func TestStopStopsRunningContainer(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	state, _ := fake.ContainerState(CaddyContainerName)
	if state != docker.StateStopped {
		t.Errorf("state: %v", state)
	}
}

func TestRestartStopsThenStarts(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	state, _ := fake.ContainerState(CaddyContainerName)
	if state != docker.StateRunning {
		t.Errorf("state: %v", state)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/ -run "TestStart|TestStop|TestRestart" -v`
Expected: FAIL with undefined methods.

- [ ] **Step 3: Implement lifecycle**

```go
// internal/app/lifecycle.go
package app

import (
	"fmt"

	"github.com/bawdo/caddy_shack/internal/docker"
)

// Start starts the existing CaddyShack container.
func (a *App) Start() error {
	if err := a.docker.DaemonReachable(); err != nil {
		return fmt.Errorf("%w: %v", ErrDockerUnreachable, err)
	}
	state, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	switch state {
	case docker.StateAbsent:
		return fmt.Errorf("%w: run 'caddy_shack install' first", ErrNotInstalled)
	case docker.StateRunning:
		return nil
	case docker.StateStopped:
		return a.docker.Start(CaddyContainerName)
	}
	return nil
}

// Stop stops the CaddyShack container if running. No-op if absent or stopped.
func (a *App) Stop() error {
	if err := a.docker.DaemonReachable(); err != nil {
		return fmt.Errorf("%w: %v", ErrDockerUnreachable, err)
	}
	state, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	if state == docker.StateRunning {
		return a.docker.Stop(CaddyContainerName)
	}
	return nil
}

// Restart stops then starts. Tolerates absent (returns ErrNotInstalled).
func (a *App) Restart() error {
	if err := a.Stop(); err != nil {
		return err
	}
	return a.Start()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/ -run "TestStart|TestStop|TestRestart" -v`
Expected: PASS (6 tests).

- [ ] **Step 5: Add CLI commands**

```go
// cmd/start.go
package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the existing Caddy container",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			return a.Start()
		},
	}
}
```

```go
// cmd/stop.go
package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the Caddy container",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			return a.Stop()
		},
	}
}
```

```go
// cmd/restart.go
package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Stop then start the Caddy container",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			return a.Restart()
		},
	}
}
```

Modify `cmd/root.go` `newRootCmd` to register them:

```go
root.AddCommand(newStartCmd())
root.AddCommand(newStopCmd())
root.AddCommand(newRestartCmd())
```

- [ ] **Step 6: Verify build and tests**

Run: `go build ./... && go test ./...`
Expected: build OK, all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/lifecycle.go internal/app/lifecycle_test.go cmd/start.go cmd/stop.go cmd/restart.go cmd/root.go
git commit -m "Added start, stop, and restart lifecycle commands"
```

---

## Task 10: App.Register + CLI register

**Files:**
- Create: `internal/app/register.go`
- Test: `internal/app/register_test.go`
- Create: `cmd/register.go`
- Modify: `cmd/root.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/app/register_test.go
package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// fixture wires an App against a temp dir + httptest Caddy stub + Fake docker.
type fixture struct {
	app       *App
	statePath string
	caddyHits int
	lastBody  []byte
	mu        sync.Mutex
	caddySrv  *httptest.Server
}

func newFixture(t *testing.T, caddyStatus int) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{
		statePath: filepath.Join(dir, "state.json"),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.caddyHits++
		f.lastBody, _ = io.ReadAll(r.Body)
		f.mu.Unlock()
		w.WriteHeader(caddyStatus)
		if caddyStatus >= 400 {
			_, _ = w.Write([]byte(`{"error":"stub rejection"}`))
		}
	}))
	f.caddySrv = srv
	t.Cleanup(srv.Close)

	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning

	f.app = NewWithPaths(
		Paths{
			StatePath: f.statePath,
			LockPath:  f.statePath + ".lock",
		},
		caddyapi.NewClient(srv.URL, time.Second),
		fakeDock,
	)
	return f
}

func TestRegisterNewBackendPersistsAndPushesConfig(t *testing.T) {
	f := newFixture(t, http.StatusOK)

	err := f.app.Register(context.Background(), RegisterInput{
		Name:     "myapp",
		Upstream: "127.0.0.1:3000",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	s, err := state.Load(f.statePath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, ok := s.Backends["myapp"]
	if !ok {
		t.Fatal("backend not persisted")
	}
	if b.Domain != "myapp.test" {
		t.Errorf("Domain: %q", b.Domain)
	}
	if b.Upstream != "127.0.0.1:3000" {
		t.Errorf("Upstream: %q", b.Upstream)
	}
	if !strings.Contains(string(f.lastBody), "myapp.test") {
		t.Errorf("Caddy not given the route: %s", f.lastBody)
	}
}

func TestRegisterCustomDomain(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	err := f.app.Register(context.Background(), RegisterInput{
		Name:     "myapp",
		Upstream: "127.0.0.1:3000",
		Domain:   "custom.test",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	s, _ := state.Load(f.statePath)
	if s.Backends["myapp"].Domain != "custom.test" {
		t.Errorf("Domain: %s", s.Backends["myapp"].Domain)
	}
}

func TestRegisterSameFieldsIsNoOp(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	in := RegisterInput{Name: "myapp", Upstream: "127.0.0.1:3000"}
	if err := f.app.Register(context.Background(), in); err != nil {
		t.Fatalf("Register1: %v", err)
	}
	hits := f.caddyHits
	if err := f.app.Register(context.Background(), in); err != nil {
		t.Fatalf("Register2: %v", err)
	}
	if f.caddyHits != hits {
		t.Errorf("Caddy hit again on identical re-register: %d -> %d", hits, f.caddyHits)
	}
}

func TestRegisterUpdatesOnFieldChange(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "myapp", Upstream: "127.0.0.1:3000"}); err != nil {
		t.Fatalf("Register1: %v", err)
	}
	if err := f.app.Register(context.Background(), RegisterInput{Name: "myapp", Upstream: "127.0.0.1:9999"}); err != nil {
		t.Fatalf("Register2: %v", err)
	}
	s, _ := state.Load(f.statePath)
	if s.Backends["myapp"].Upstream != "127.0.0.1:9999" {
		t.Errorf("Upstream not updated: %s", s.Backends["myapp"].Upstream)
	}
}

func TestRegisterDomainConflictReturnsErrDomainConflict(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Upstream: "127.0.0.1:3000"}); err != nil {
		t.Fatalf("foo: %v", err)
	}
	err := f.app.Register(context.Background(), RegisterInput{Name: "bar", Domain: "foo.test", Upstream: "127.0.0.1:4000"})
	if !errors.Is(err, ErrDomainConflict) {
		t.Errorf("expected ErrDomainConflict, got %v", err)
	}
	// State must not have been mutated.
	s, _ := state.Load(f.statePath)
	if _, ok := s.Backends["bar"]; ok {
		t.Error("bar persisted despite conflict")
	}
}

func TestRegisterInvalidNameReturnsErrInvalidArgs(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	err := f.app.Register(context.Background(), RegisterInput{Name: "Bad Name!", Upstream: "127.0.0.1:3000"})
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("expected ErrInvalidArgs, got %v", err)
	}
}

func TestRegisterInvalidUpstreamReturnsErrInvalidArgs(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Upstream: "not a host port"})
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("expected ErrInvalidArgs, got %v", err)
	}
}

func TestRegisterCaddyRejectionDoesNotPersistState(t *testing.T) {
	f := newFixture(t, http.StatusBadRequest)
	err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Upstream: "127.0.0.1:3000"})
	if err == nil {
		t.Fatal("expected error from caddy rejection")
	}
	s, _ := state.Load(f.statePath)
	if _, ok := s.Backends["foo"]; ok {
		t.Error("state persisted despite Caddy rejection")
	}
}

func TestRegisterReturnsErrCaddyNotRunningWhenContainerStopped(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.caddySrv.Close() // simulate unreachable admin API
	// Also reflect container state so Register detects it before HTTP.
	f.app.docker.(*docker.Fake).Containers[CaddyContainerName] = docker.StateStopped

	err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Upstream: "127.0.0.1:3000"})
	if !errors.Is(err, ErrCaddyNotRunning) {
		t.Errorf("expected ErrCaddyNotRunning, got %v", err)
	}
}

// Smoke check: the body POSTed to Caddy is the JSON our builder produces.
func TestRegisterPostsBuiltConfig(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "myapp", Upstream: "127.0.0.1:3000"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(f.lastBody, &got); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if _, ok := got["apps"]; !ok {
		t.Errorf("body missing 'apps' key: %s", f.lastBody)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/ -run TestRegister -v`
Expected: FAIL with "Register undefined" / "RegisterInput undefined".

- [ ] **Step 3: Implement Register**

```go
// internal/app/register.go
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// RegisterInput holds the fields the register command accepts.
type RegisterInput struct {
	Name     string
	Domain   string // optional; defaults to <Name>.test
	Upstream string
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// Register adds or updates a backend. Idempotent: identical re-registration
// is a no-op (no Caddy push, no state write).
func (a *App) Register(ctx context.Context, in RegisterInput) error {
	if !nameRE.MatchString(in.Name) {
		return fmt.Errorf("%w: name must match ^[a-z0-9][a-z0-9-]{0,30}$ (got %q)", ErrInvalidArgs, in.Name)
	}
	if err := validateUpstream(in.Upstream); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArgs, err)
	}
	if in.Domain == "" {
		in.Domain = in.Name + ".test"
	}

	// Caddy must be running before we attempt any state change.
	st, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	if st != docker.StateRunning {
		return fmt.Errorf("%w (state=%s)", ErrCaddyNotRunning, st)
	}

	lock, err := state.AcquireLock(a.paths.LockPath, 5*time.Second)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return err
	}
	defer lock.Release()

	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return err
	}

	// Domain-conflict check (skipping the same name's own row).
	for name, b := range s.Backends {
		if name != in.Name && b.Domain == in.Domain {
			return fmt.Errorf("%w: %s already owned by backend %q", ErrDomainConflict, in.Domain, name)
		}
	}

	existing, present := s.Backends[in.Name]
	if present && existing.Domain == in.Domain && existing.Upstream == in.Upstream {
		// Identical re-registration. No-op.
		return nil
	}

	now := time.Now().Format(time.RFC3339)
	s.Backends[in.Name] = state.Backend{
		Domain:       in.Domain,
		Upstream:     in.Upstream,
		RegisteredAt: now,
	}

	cfg := caddyapi.Build(s)
	if err := a.caddy.Load(cfg); err != nil {
		if errors.Is(err, caddyapi.ErrUnreachable) {
			return fmt.Errorf("%w: %v", ErrCaddyNotRunning, err)
		}
		return err
	}

	if err := state.Save(a.paths.StatePath, s); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	_ = ctx // reserved for future cancellation
	return nil
}

// validateUpstream checks the host:port shape. Hostname is anything
// net.SplitHostPort accepts; port must be 1-65535.
func validateUpstream(u string) error {
	host, portStr, err := net.SplitHostPort(u)
	if err != nil {
		return fmt.Errorf("upstream must be host:port (got %q): %w", u, err)
	}
	if host == "" {
		return fmt.Errorf("upstream host is empty")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("upstream port out of range (got %q)", portStr)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/ -run TestRegister -v`
Expected: PASS (10 tests).

- [ ] **Step 5: Add the CLI command**

```go
// cmd/register.go
package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newRegisterCmd() *cobra.Command {
	var upstream, domain string
	c := &cobra.Command{
		Use:   "register <name>",
		Short: "Register a backend so Caddy serves it over HTTPS at <name>.test",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			return a.Register(cmd.Context(), app.RegisterInput{
				Name:     args[0],
				Domain:   domain,
				Upstream: upstream,
			})
		},
	}
	c.Flags().StringVar(&upstream, "upstream", "", "Upstream host:port (required)")
	c.Flags().StringVar(&domain, "domain", "", "Optional domain (defaults to <name>.test)")
	_ = c.MarkFlagRequired("upstream")
	return c
}
```

Register in `cmd/root.go` `newRootCmd`:

```go
root.AddCommand(newRegisterCmd())
```

- [ ] **Step 6: Verify build and tests**

Run: `go build ./... && go test ./...`
Expected: build OK, all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/register.go internal/app/register_test.go cmd/register.go cmd/root.go
git commit -m "Added register command with idempotency and conflict detection"
```

---

## Task 11: App.Deregister + CLI deregister

**Files:**
- Create: `internal/app/deregister.go`
- Test: append to `internal/app/register_test.go`
- Create: `cmd/deregister.go`
- Modify: `cmd/root.go`

- [ ] **Step 1: Write the failing tests** (append to register_test.go)

Ensure `strings` is in the existing import block at the top of `internal/app/register_test.go` (already imported in Task 10). Then append these three test functions to the file:

```go
func TestDeregisterRemovesBackendAndPushesConfig(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Upstream: "127.0.0.1:3000"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := f.app.Deregister(context.Background(), "foo"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	s, _ := state.Load(f.statePath)
	if _, ok := s.Backends["foo"]; ok {
		t.Error("backend still present in state")
	}
	// Caddy should have been called twice: register + deregister.
	if f.caddyHits != 2 {
		t.Errorf("caddy hits: got %d want 2", f.caddyHits)
	}
}

func TestDeregisterUnknownNameIsNoOp(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	hits := f.caddyHits
	if err := f.app.Deregister(context.Background(), "unknown"); err != nil {
		t.Errorf("expected nil err, got %v", err)
	}
	if f.caddyHits != hits {
		t.Error("Caddy was called for unknown name")
	}
}

func TestDeregisterCaddyRejectionLeavesStateIntact(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Upstream: "127.0.0.1:3000"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Swap server to return 400 from now on.
	f.caddySrv.Close()
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("{}"))
	}))
	defer rejecting.Close()
	f.app = NewWithPaths(
		Paths{StatePath: f.statePath, LockPath: f.statePath + ".lock"},
		caddyapi.NewClient(rejecting.URL, time.Second),
		f.app.docker,
	)

	err := f.app.Deregister(context.Background(), "foo")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "rejected") && !strings.Contains(err.Error(), "stub") {
		t.Logf("error: %v", err)
	}
	s, _ := state.Load(f.statePath)
	if _, ok := s.Backends["foo"]; !ok {
		t.Error("state lost despite Caddy rejection")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/ -run TestDeregister -v`
Expected: FAIL with "Deregister undefined".

- [ ] **Step 3: Implement Deregister**

```go
// internal/app/deregister.go
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// Deregister removes a backend. Idempotent: unknown name returns nil and
// makes no Caddy call.
func (a *App) Deregister(ctx context.Context, name string) error {
	st, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	if st != docker.StateRunning {
		return fmt.Errorf("%w (state=%s)", ErrCaddyNotRunning, st)
	}

	lock, err := state.AcquireLock(a.paths.LockPath, 5*time.Second)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return err
	}
	defer lock.Release()

	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return err
	}

	if _, ok := s.Backends[name]; !ok {
		return nil
	}

	// Work on a copy so a Caddy failure leaves on-disk state intact.
	updated := *s
	updated.Backends = make(map[string]state.Backend, len(s.Backends))
	for k, v := range s.Backends {
		if k != name {
			updated.Backends[k] = v
		}
	}

	cfg := caddyapi.Build(&updated)
	if err := a.caddy.Load(cfg); err != nil {
		if errors.Is(err, caddyapi.ErrUnreachable) {
			return fmt.Errorf("%w: %v", ErrCaddyNotRunning, err)
		}
		return err
	}

	if err := state.Save(a.paths.StatePath, &updated); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	_ = ctx
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/ -run TestDeregister -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Add the CLI command**

```go
// cmd/deregister.go
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newDeregisterCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "deregister <name>",
		Short: "Deregister a previously registered backend",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			if err := a.Deregister(cmd.Context(), args[0]); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "deregistered:", args[0])
			return nil
		},
	}
}
```

Register in `cmd/root.go`:

```go
root.AddCommand(newDeregisterCmd())
```

- [ ] **Step 6: Verify build and tests**

Run: `go build ./... && go test ./...`
Expected: build OK, all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/deregister.go internal/app/register_test.go cmd/deregister.go cmd/root.go
git commit -m "Added deregister command with idempotent removal"
```

---

## Task 12: App.List + CLI list

**Files:**
- Create: `internal/app/list.go`
- Test: `internal/app/list_test.go`
- Create: `cmd/list.go`
- Modify: `cmd/root.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/app/list_test.go
package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/state"
)

func TestListEmptyReturnsEmptySlice(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, nil)

	got, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d items, want 0", len(got))
	}
}

func TestListReturnsBackendsSortedByName(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"zeta":  {Domain: "zeta.test", Upstream: "127.0.0.1:3000"},
			"alpha": {Domain: "alpha.test", Upstream: "127.0.0.1:4000"},
		},
	}
	if err := state.Save(statePath, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	a := NewWithPaths(Paths{
		StatePath: statePath,
		LockPath:  statePath + ".lock",
	}, nil, nil)

	got, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Errorf("not sorted by name: %+v", got)
	}
}

func TestListJSONOutputIsValidArray(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := state.Save(statePath, &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"foo": {Domain: "foo.test", Upstream: "127.0.0.1:3000"},
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	a := NewWithPaths(Paths{
		StatePath: statePath,
		LockPath:  statePath + ".lock",
	}, nil, nil)

	got, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out, _ := json.Marshal(got)
	if !strings.Contains(string(out), `"foo"`) {
		t.Errorf("JSON missing foo: %s", out)
	}
}

func TestListReturnsErrStateCorruptOnBadFile(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := writeFile(t, statePath, "{not json"); err != nil {
		t.Fatal(err)
	}
	a := NewWithPaths(Paths{
		StatePath: statePath,
		LockPath:  statePath + ".lock",
	}, nil, nil)
	_, err := a.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("expected corrupt error, got %v", err)
	}
}

func writeFile(t *testing.T, path, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o600)
}
```

Add at the top of the file:

```go
import "os"
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/ -run TestList -v`
Expected: FAIL with "List undefined" / "Listing undefined".

- [ ] **Step 3: Implement List**

```go
// internal/app/list.go
package app

import (
	"context"
	"fmt"
	"sort"

	"github.com/bawdo/caddy_shack/internal/state"
)

// Listing is one row in the output of List.
type Listing struct {
	Name         string `json:"name"`
	Domain       string `json:"domain"`
	Upstream     string `json:"upstream"`
	RegisteredAt string `json:"registered_at"`
}

// List returns the registered backends, sorted by name.
func (a *App) List(ctx context.Context) ([]Listing, error) {
	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return nil, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return nil, err
	}
	names := make([]string, 0, len(s.Backends))
	for n := range s.Backends {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Listing, 0, len(names))
	for _, n := range names {
		b := s.Backends[n]
		out = append(out, Listing{
			Name: n, Domain: b.Domain, Upstream: b.Upstream, RegisteredAt: b.RegisteredAt,
		})
	}
	_ = ctx
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/ -run TestList -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Add the CLI command**

```go
// cmd/list.go
package cmd

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newListCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List registered backends",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			items, err := a.List(cmd.Context())
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if asJSON {
				out, err := json.MarshalIndent(items, "", "  ")
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintln(w, string(out))
				return nil
			}
			if len(items) == 0 {
				_, _ = fmt.Fprintln(w, "no backends registered")
				return nil
			}
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "NAME\tDOMAIN\tUPSTREAM\tREGISTERED")
			for _, it := range items {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", it.Name, it.Domain, it.Upstream, it.RegisteredAt)
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return c
}
```

Register in `cmd/root.go`:

```go
root.AddCommand(newListCmd())
```

- [ ] **Step 6: Verify build and tests**

Run: `go build ./... && go test ./...`
Expected: build OK, all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/list.go internal/app/list_test.go cmd/list.go cmd/root.go
git commit -m "Added list command with table and JSON output"
```

---

## Task 13: App.Status + CLI status

**Files:**
- Create: `internal/app/status.go`
- Test: `internal/app/status_test.go`
- Create: `cmd/status.go`
- Modify: `cmd/root.go`

`status` returns exit 0 when the container is running AND the admin API is reachable; exit 1 otherwise. The cmd layer maps `ErrStatusUnhealthy` (a sentinel introduced here) to that.

- [ ] **Step 1: Write the failing tests**

```go
// internal/app/status_test.go
package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
)

func TestStatusHealthyWhenRunningAndReachable(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning

	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, caddyapi.NewClient(srv.URL, time.Second), fakeDock)

	rep, err := a.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !rep.Healthy {
		t.Errorf("expected Healthy=true: %+v", rep)
	}
	if rep.ContainerState != "running" {
		t.Errorf("ContainerState: %s", rep.ContainerState)
	}
	if !rep.AdminReachable {
		t.Errorf("AdminReachable=false")
	}
}

func TestStatusUnhealthyWhenContainerStopped(t *testing.T) {
	dir := t.TempDir()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateStopped

	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, caddyapi.NewClient("http://127.0.0.1:1", 200*time.Millisecond), fakeDock)

	rep, err := a.Status()
	if !errors.Is(err, ErrUnhealthy) {
		t.Errorf("expected ErrUnhealthy, got %v", err)
	}
	if rep.Healthy {
		t.Errorf("expected Healthy=false")
	}
}

func TestStatusReportsBackendCount(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := writeFile(t, statePath, `{"schema_version":1,"backends":{"a":{"domain":"a.test","upstream":"127.0.0.1:1"}}}`); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("{}")) }))
	defer srv.Close()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning

	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv.URL, time.Second), fakeDock)
	rep, err := a.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.BackendCount != 1 {
		t.Errorf("BackendCount: %d", rep.BackendCount)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/ -run TestStatus -v`
Expected: FAIL with "Status undefined" / "ErrUnhealthy undefined" / "StatusReport undefined".

- [ ] **Step 3: Add the new sentinel and implement Status**

Append to `internal/app/errors.go`:

```go
// ErrUnhealthy is returned by Status when the container is not running OR the
// admin API is unreachable. Maps to exit code 1 (generic command failure).
var ErrUnhealthy = errors.New("caddy_shack is unhealthy")
```

Create `internal/app/status.go`:

```go
// internal/app/status.go
package app

import (
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// StatusReport is what `status` returns. Marshalled with --json.
type StatusReport struct {
	ContainerState string `json:"container_state"`
	AdminReachable bool   `json:"admin_reachable"`
	BackendCount   int    `json:"backend_count"`
	Healthy        bool   `json:"healthy"`
}

// Status reports container state, admin API reachability, and backend
// count. Returns ErrUnhealthy when not Healthy so cmd layer maps it to
// exit 1.
func (a *App) Status() (StatusReport, error) {
	rep := StatusReport{}
	if cs, err := a.docker.ContainerState(CaddyContainerName); err == nil {
		rep.ContainerState = cs.String()
	} else {
		rep.ContainerState = docker.StateAbsent.String()
	}

	rep.AdminReachable = a.caddy.Reachable()

	if s, err := state.Load(a.paths.StatePath); err == nil {
		rep.BackendCount = len(s.Backends)
	}

	rep.Healthy = rep.ContainerState == docker.StateRunning.String() && rep.AdminReachable
	if !rep.Healthy {
		return rep, ErrUnhealthy
	}
	return rep, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/ -run TestStatus -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Add the CLI command**

```go
// cmd/status.go
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newStatusCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Report container state, admin API reachability, and backend count",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			rep, runErr := a.Status()
			w := cmd.OutOrStdout()
			if asJSON {
				out, _ := json.MarshalIndent(rep, "", "  ")
				_, _ = fmt.Fprintln(w, string(out))
			} else {
				_, _ = fmt.Fprintf(w, "container:       %s\n", rep.ContainerState)
				_, _ = fmt.Fprintf(w, "admin reachable: %t\n", rep.AdminReachable)
				_, _ = fmt.Fprintf(w, "backends:        %d\n", rep.BackendCount)
				_, _ = fmt.Fprintf(w, "healthy:         %t\n", rep.Healthy)
			}
			if runErr != nil && !errors.Is(runErr, app.ErrUnhealthy) {
				return runErr
			}
			return runErr // ErrUnhealthy -> generic exit 1 via app.ExitCode
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return c
}
```

Register in `cmd/root.go`:

```go
root.AddCommand(newStatusCmd())
```

- [ ] **Step 6: Verify build and tests**

Run: `go build ./... && go test ./...`
Expected: build OK, all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/status.go internal/app/errors.go internal/app/status_test.go cmd/status.go cmd/root.go
git commit -m "Added status command with healthy/unhealthy exit codes"
```

---

## Task 14: Document exit codes in root command long help

**Files:**
- Modify: `cmd/root.go`

- [ ] **Step 1: Update `Long` on the root cobra command**

Edit `newRootCmd` in `cmd/root.go`:

```go
root := &cobra.Command{
	Use:           "caddy_shack",
	Short:         "Manage a local Caddy instance for HTTPS .test URLs",
	Long: `caddy_shack manages a locally-running Caddy container so backend services
can register themselves and get a stable HTTPS URL under the .test TLD.

Designed to be called from project startup and teardown scripts:
  caddy_shack register myapp --upstream 127.0.0.1:3000
  caddy_shack deregister myapp

Exit codes (stable, script-friendly):
  0  success
  1  generic command failure (e.g. Caddy rejected the config; status unhealthy)
  2  invalid arguments
  3  domain conflict (another backend already owns this domain)
  4  docker daemon unreachable
  5  caddy container not running
  6  caddy not installed (no container exists)
  7  state file corrupt - manual remediation required
  8  state lock held by another caddy_shack process

See docs/superpowers/specs/2026-05-27-mvp-vertical-slice-design.md for the
full design.`,
	SilenceUsage:  true,
	SilenceErrors: false,
}
```

- [ ] **Step 2: Verify build and existing tests**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/root.go
git commit -m "Documented exit-code contract in root command long help"
```

---

## Task 15: Integration test scaffolding

**Files:**
- Create: `test/integration/helpers.go`

Integration tests are gated by `//go:build integration` so unit-test runs stay docker-free.

- [ ] **Step 1: Write the helpers**

```go
// test/integration/helpers.go
//go:build integration

// Package integration spins up real CaddyShack-managed Caddy containers
// against a temporary state directory. Tests in this package require the
// `docker` CLI on PATH and a running docker daemon. Run with:
//   go test -tags integration ./test/integration/...
package integration

import (
	"net/http"
	"os/exec"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/docker"
)

const testContainerName = "caddy_shack_it"

// SkipUnlessDocker skips the test if `docker info` doesn't succeed.
func SkipUnlessDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
}

// CleanupContainer ensures the test container is gone before AND after the
// test. The container name is fixed to keep tests isolated from a developer's
// real CaddyShack install.
func CleanupContainer(t *testing.T) {
	t.Helper()
	rm := func() {
		_ = exec.Command("docker", "rm", "-f", testContainerName).Run()
	}
	rm()
	t.Cleanup(rm)
}

// WaitForCaddyAdmin polls the admin API until /config/ returns 2xx or the
// deadline expires.
func WaitForCaddyAdmin(t *testing.T, baseURL string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/config/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("Caddy admin API never came up at %s", baseURL)
}

// MustAppWithTempState builds an App pointed at a tempdir state file. The
// caller is responsible for ensuring Caddy is running on the admin port the
// App was built with.
func MustAppWithTempState(t *testing.T, statePath string) *app.App {
	t.Helper()
	return app.NewWithPaths(app.Paths{
		StatePath: statePath,
		LockPath:  statePath + ".lock",
	}, nil, nil)
}

// fakeRunningDocker is a stub Client for integration tests that own the
// real container lifecycle themselves (so they don't want App to touch
// docker for inspections). Always reports the container as running.
type fakeRunningDocker struct{}

func (fakeRunningDocker) DaemonReachable() error                                { return nil }
func (fakeRunningDocker) EnsureVolume(string) error                             { return nil }
func (fakeRunningDocker) PullImage(string) error                                { return nil }
func (fakeRunningDocker) ContainerState(string) (docker.ContainerState, error)  { return docker.StateRunning, nil }
func (fakeRunningDocker) CreateAndStart(docker.ContainerSpec) error             { return nil }
func (fakeRunningDocker) Start(string) error                                    { return nil }
func (fakeRunningDocker) Stop(string) error                                     { return nil }
```

- [ ] **Step 2: Verify it builds with the integration tag**

Run: `go build -tags integration ./test/integration/`
Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add test/integration/helpers.go
git commit -m "Added integration test scaffolding with docker skip helpers"
```

---

## Task 16: Integration test - register E2E against real Caddy

**Files:**
- Create: `test/integration/register_e2e_test.go`

This test bypasses `App.Install` (which uses the production container name) and starts its own container under `caddy_shack_it` so it doesn't trash a developer's existing install. The App is then pointed at the test container's admin port.

- [ ] **Step 1: Write the test**

```go
// test/integration/register_e2e_test.go
//go:build integration

package integration

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/caddyapi"
)

func TestRegisterE2EReverseProxiesOverHTTPS(t *testing.T) {
	SkipUnlessDocker(t)
	CleanupContainer(t)

	// Start a real Caddy container on a free set of host ports so we don't
	// clash with anything else listening on 80/443/2019.
	adminPort := freePort(t)
	httpsPort := freePort(t)
	httpPort := freePort(t)
	cmd := exec.Command("docker", "run", "-d", "--name", testContainerName,
		"-p", fmt.Sprintf("127.0.0.1:%d:2019", adminPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:443", httpsPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:80", httpPort),
		"caddy:2",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}

	adminURL := fmt.Sprintf("http://127.0.0.1:%d", adminPort)
	WaitForCaddyAdmin(t, adminURL, 15*time.Second)

	// Spin up a tiny upstream listening on a random loopback port.
	upstream, upstreamAddr := startUpstream(t)
	defer upstream.Close()

	// Build an App that talks to OUR test Caddy and skip docker entirely
	// (we already started the container above).
	statePath := filepath.Join(t.TempDir(), "state.json")
	a := app.NewWithPaths(
		app.Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(adminURL, 5*time.Second),
		fakeRunningDocker{},
	)

	if err := a.Register(context.Background(), app.RegisterInput{
		Name:     "smoke",
		Upstream: upstreamAddr,
		Domain:   "smoke.test",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Hit https://smoke.test via Caddy's HTTPS port. We have to override the
	// dialer to resolve smoke.test -> 127.0.0.1:<httpsPort>, and skip cert
	// verification (Caddy's internal CA isn't trusted by the test process).
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", httpsPort))
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 5 * time.Second,
	}
	resp, err := client.Get("https://smoke.test/")
	if err != nil {
		t.Fatalf("GET smoke.test: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Errorf("status: %d body=%s", resp.StatusCode, body)
	}
	if string(body) != "hello from upstream" {
		t.Errorf("body: %s", body)
	}
}

func startUpstream(t *testing.T) (*http.Server, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("hello from upstream"))
		}),
		ReadHeaderTimeout: time.Second,
	}
	go srv.Serve(l)
	return srv, l.Addr().String()
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
```

- [ ] **Step 2: Run the integration test locally**

Run: `go test -tags integration ./test/integration/ -run TestRegisterE2E -v`
Expected: PASS (skipped if docker unavailable). `fakeRunningDocker` is already in `helpers.go` from Task 15.

- [ ] **Step 3: Commit**

```bash
git add test/integration/register_e2e_test.go
git commit -m "Added integration test for end-to-end HTTPS reverse-proxy"
```

---

## Task 17: Integration test - concurrent register lock contention

**Files:**
- Create: `test/integration/lock_race_test.go`

Two `register` calls in parallel must serialise; the second call sees the first's effect on shared state. This validates the `flock` plumbing under real concurrency.

- [ ] **Step 1: Write the test**

```go
// test/integration/lock_race_test.go
//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/state"
)

func TestRegisterIsSerialisedByFileLock(t *testing.T) {
	// httptest Caddy stub. Slow handler so the race is visible.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	statePath := filepath.Join(t.TempDir(), "state.json")
	mkApp := func() *app.App {
		return app.NewWithPaths(
			app.Paths{StatePath: statePath, LockPath: statePath + ".lock"},
			caddyapi.NewClient(srv.URL, time.Second),
			fakeRunningDocker{},
		)
	}

	port := freeAddr(t)
	const n = 5
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := mkApp().Register(context.Background(), app.RegisterInput{
				Name:     fmt.Sprintf("svc%d", i),
				Upstream: port,
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Register: %v", err)
		}
	}

	s, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Backends) != n {
		t.Errorf("backends: got %d want %d (lock dropped writes)", len(s.Backends), n)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}
```

- [ ] **Step 2: Run the test**

Run: `go test -tags integration ./test/integration/ -run TestRegisterIsSerialised -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add test/integration/lock_race_test.go
git commit -m "Added concurrency test proving register serialises via flock"
```

---

## Task 18: CI workflow - add integration job

**Files:**
- Modify: `.github/workflows/ci.yml`

- [ ] **Step 1: Add a new job to ci.yml**

After the existing `vuln:` job and before `build:`, add an `integration:` job.
Append to `.github/workflows/ci.yml`:

```yaml
  integration:
    name: Integration Tests
    runs-on: ubuntu-latest
    steps:
      - name: Checkout code
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.25.x'
          check-latest: true
          cache: true

      - name: Verify docker
        run: docker info

      - name: Run integration tests
        run: go test -tags integration -v ./test/integration/...
```

(ubuntu-latest has the docker daemon pre-installed; macos-latest does not.)

- [ ] **Step 2: Push and confirm the job runs**

Run: `git status` to confirm the only change is the workflow file.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "Added CI job for docker-backed integration tests"
```

---

## Task 19: End-to-end manual smoke (local sanity check)

This task is not code; it verifies the whole slice works on the developer's
machine before declaring MVP done.

- [ ] **Step 1: Confirm DNS prerequisite is set up**

Per the README "Prerequisites" section, run:

```bash
dscacheutil -q host -a name anything.test
```

Expected: an `ip_address:` line containing `127.0.0.1`. If not, follow the
README dnsmasq + /etc/resolver setup before proceeding.

- [ ] **Step 2: Build and install caddy_shack**

```bash
make install
```

- [ ] **Step 3: Install and start Caddy**

```bash
caddy_shack install
caddy_shack status
```

Expected: `status` exits 0, prints `container: running`, `admin reachable: true`.

- [ ] **Step 4: Trust Caddy's local root CA (one-off)**

```bash
docker exec caddy_shack cat /data/caddy/pki/authorities/local/root.crt > /tmp/caddy_root.crt
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain /tmp/caddy_root.crt
```

This makes Safari/Chrome trust `.test` HTTPS without a warning. Browsers may
need a restart.

- [ ] **Step 5: Run a dummy upstream and register it**

```bash
python3 -m http.server 3000 &
caddy_shack register hello --upstream 127.0.0.1:3000
caddy_shack list
curl -sSf https://hello.test/
```

Expected: `list` shows the row; `curl` returns a 200 with the Python server's
directory listing.

- [ ] **Step 6: Idempotency check**

```bash
caddy_shack register hello --upstream 127.0.0.1:3000   # should be no-op
caddy_shack register hello --upstream 127.0.0.1:3001   # should update
caddy_shack list                                       # upstream now :3001
caddy_shack deregister hello
caddy_shack list                                       # empty
caddy_shack deregister hello                           # exit 0 with stderr note
```

- [ ] **Step 7: Clean up**

```bash
kill %1    # python upstream
caddy_shack stop
```

- [ ] **Step 8: Commit the marker file** (optional, only if anything in the
  smoke test surfaced bugs you fixed)

Otherwise nothing to commit for this task.

---

## Plan complete.

# Subdomain Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every registered backend automatically serve `*.<domain>` from the same upstream, while letting any single subdomain be overridden to a different backend and cascade-removed with its parent.

**Architecture:** Wildcards are *derived* at Caddy-config-build time, never stored. State stays the flat `map[name]Backend`. An override is an ordinary backend whose name (and domain) contains dots; its exact route wins over the parent's wildcard because `caddyapi.Build` emits all exact routes ahead of all wildcard routes. `deregister` gains a `--cascade` flag that removes child backends found by domain suffix.

**Tech Stack:** Go, Cobra CLI, Caddy admin API (JSON config), standard library testing, golden-file JSON fixtures.

**Spec:** `docs/superpowers/specs/2026-06-06-subdomain-support-design.md`

---

## File Structure

- `internal/app/register.go` — relax the name-validation regex (Task 1). Sole responsibility: register/validate a backend.
- `internal/caddyapi/config.go` — emit exact + wildcard routes with ordering, derive TLS subjects (Task 2). Sole responsibility: render Caddy JSON from state.
- `internal/caddyapi/testdata/*.json` — golden config fixtures, updated/added (Task 2).
- `internal/caddyapi/config_test.go` — new build tests (Task 2).
- `internal/app/deregister.go` — cascade-aware removal + `DeregisterResult` (Task 3). Sole responsibility: deregister one backend and optionally its children.
- `internal/app/register_test.go` — holds the App-level register *and* deregister tests (existing layout); updated for the new signature + new cases (Tasks 1, 3).
- `cmd/deregister.go` — wire `--cascade` flag and user-facing messages (Task 4).
- `cmd/deregister_test.go` — new; flag-wiring test (Task 4).
- `README.md` + `cmd/register.go` + `cmd/deregister.go` long help — docs (Task 5).
- `test/integration/subdomain_e2e_test.go` — new; real-Caddy wildcard + override + wildcard-TLS (Task 6).

---

## Task 1: Relax name validation to allow dotted subdomain names

**Files:**
- Modify: `internal/app/register.go:30`
- Test: `internal/app/register_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/app/register_test.go` (the fixture `newFixture` and `caddyapi`/`context`/`errors` imports already exist in this file):

`register_test.go` is `package app` (an internal test), so symbols are used unqualified (`RegisterInput`, `ErrInvalidArgs`), matching the existing style around line 64:

```go
func TestRegisterAcceptsDottedSubdomainName(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	err := f.app.Register(context.Background(), RegisterInput{Name: "bar.foo", Port: 3000})
	if err != nil {
		t.Fatalf("Register dotted name: %v", err)
	}
	if !strings.Contains(string(f.lastBody), "bar.foo.test") {
		t.Errorf("Caddy not given the dotted route: %s", f.lastBody)
	}
}

func TestRegisterRejectsMalformedDottedName(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	for _, bad := range []string{"foo..bar", ".foo", "foo.", "-bad.foo", "foo.-bad"} {
		err := f.app.Register(context.Background(), RegisterInput{Name: bad, Port: 3000})
		if !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("name %q: want ErrInvalidArgs, got %v", bad, err)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/app/ -run 'TestRegisterAcceptsDottedSubdomainName|TestRegisterRejectsMalformedDottedName' -v`
Expected: FAIL — `TestRegisterAcceptsDottedSubdomainName` fails because the current regex rejects `bar.foo` (returns `ErrInvalidArgs`).

- [ ] **Step 3: Relax the regex**

In `internal/app/register.go`, replace line 30:

```go
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
```

with:

```go
// nameRE accepts one or more dot-separated DNS labels. A single label (foo)
// maps to foo.test; a dotted name (bar.foo) maps to bar.foo.test and acts as
// a subdomain override. Each label keeps the original single-label rules.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}(\.[a-z0-9][a-z0-9-]{0,30})*$`)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/app/ -run 'TestRegister' -v`
Expected: PASS for all register tests, including the two new ones and the existing `TestRegisterInvalidNameReturnsErrInvalidArgs`.

- [ ] **Step 5: Commit**

```bash
git add internal/app/register.go internal/app/register_test.go
git commit -m "Allowed dotted subdomain names in register"
```

---

## Task 2: Emit exact + wildcard routes and derive TLS subjects

**Files:**
- Modify: `internal/caddyapi/config.go:68-109`
- Modify: `internal/caddyapi/testdata/empty.json`, `one_backend.json`, `two_backends.json`
- Create: `internal/caddyapi/testdata/override.json`
- Test: `internal/caddyapi/config_test.go`

- [ ] **Step 1: Update the golden fixtures**

The golden comparison normalises JSON (unmarshal then re-marshal), so exact whitespace does not matter — only structure does.

Overwrite `internal/caddyapi/testdata/empty.json`:

```json
{
  "admin": { "listen": "0.0.0.0:2019" },
  "apps": {
    "http": {
      "servers": {
        "srv0": {
          "listen": [":443"],
          "routes": [],
          "automatic_https": { "disable_redirects": false }
        }
      }
    },
    "tls": {
      "automation": {
        "policies": [
          { "subjects": [], "issuers": [{ "module": "internal" }] }
        ]
      }
    }
  }
}
```

Overwrite `internal/caddyapi/testdata/one_backend.json` (backend `myapp` → `myapp.test` → `127.0.0.1:3000`):

```json
{
  "admin": { "listen": "0.0.0.0:2019" },
  "apps": {
    "http": {
      "servers": {
        "srv0": {
          "listen": [":443"],
          "routes": [
            {
              "match": [{ "host": ["myapp.test"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "127.0.0.1:3000" }] }]
            },
            {
              "match": [{ "host": ["*.myapp.test"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "127.0.0.1:3000" }] }]
            }
          ],
          "automatic_https": { "disable_redirects": false }
        }
      }
    },
    "tls": {
      "automation": {
        "policies": [
          { "subjects": ["*.myapp.test", "myapp.test"], "issuers": [{ "module": "internal" }] }
        ]
      }
    }
  }
}
```

Overwrite `internal/caddyapi/testdata/two_backends.json` (backends `web` → `web.test` → `127.0.0.1:3000`, `api` → `api.test` → `127.0.0.1:4000`). Exact routes sort by domain (`api.test`, `web.test`), then wildcard routes (`*.api.test`, `*.web.test`). TLS subjects are the sorted union (`*` sorts before letters):

```json
{
  "admin": { "listen": "0.0.0.0:2019" },
  "apps": {
    "http": {
      "servers": {
        "srv0": {
          "listen": [":443"],
          "routes": [
            {
              "match": [{ "host": ["api.test"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "127.0.0.1:4000" }] }]
            },
            {
              "match": [{ "host": ["web.test"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "127.0.0.1:3000" }] }]
            },
            {
              "match": [{ "host": ["*.api.test"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "127.0.0.1:4000" }] }]
            },
            {
              "match": [{ "host": ["*.web.test"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "127.0.0.1:3000" }] }]
            }
          ],
          "automatic_https": { "disable_redirects": false }
        }
      }
    },
    "tls": {
      "automation": {
        "policies": [
          {
            "subjects": ["*.api.test", "*.web.test", "api.test", "web.test"],
            "issuers": [{ "module": "internal" }]
          }
        ]
      }
    }
  }
}
```

Create `internal/caddyapi/testdata/override.json` (backends `foo` → `foo.test` → `127.0.0.1:2000`, `bar.foo` → `bar.foo.test` → `127.0.0.1:3000`). Exact routes sort `bar.foo.test`, `foo.test`; wildcards `*.bar.foo.test`, `*.foo.test`:

```json
{
  "admin": { "listen": "0.0.0.0:2019" },
  "apps": {
    "http": {
      "servers": {
        "srv0": {
          "listen": [":443"],
          "routes": [
            {
              "match": [{ "host": ["bar.foo.test"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "127.0.0.1:3000" }] }]
            },
            {
              "match": [{ "host": ["foo.test"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "127.0.0.1:2000" }] }]
            },
            {
              "match": [{ "host": ["*.bar.foo.test"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "127.0.0.1:3000" }] }]
            },
            {
              "match": [{ "host": ["*.foo.test"] }],
              "handle": [{ "handler": "reverse_proxy", "upstreams": [{ "dial": "127.0.0.1:2000" }] }]
            }
          ],
          "automatic_https": { "disable_redirects": false }
        }
      }
    },
    "tls": {
      "automation": {
        "policies": [
          {
            "subjects": ["*.bar.foo.test", "*.foo.test", "bar.foo.test", "foo.test"],
            "issuers": [{ "module": "internal" }]
          }
        ]
      }
    }
  }
}
```

- [ ] **Step 2: Add the new build tests**

Append to `internal/caddyapi/config_test.go`:

```go
func TestBuildConfigOverrideExactBeatsWildcard(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"foo":     {Domain: "foo.test", Upstream: "127.0.0.1:2000"},
			"bar.foo": {Domain: "bar.foo.test", Upstream: "127.0.0.1:3000"},
		},
	}
	got := Build(s)
	assertJSONMatchesGolden(t, got, "override.json")
}

// TestBuildOrdersExactRoutesBeforeWildcards pins the invariant that an exact
// override route appears before the parent's wildcard route. Caddy evaluates
// routes in order and reverse_proxy is terminal, so a request to
// bar.foo.test must hit the exact route (port 3000) before *.foo.test.
func TestBuildOrdersExactRoutesBeforeWildcards(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"foo":     {Domain: "foo.test", Upstream: "127.0.0.1:2000"},
			"bar.foo": {Domain: "bar.foo.test", Upstream: "127.0.0.1:3000"},
		},
	}
	got := Build(s)

	var doc struct {
		Apps struct {
			HTTP struct {
				Servers struct {
					Srv0 struct {
						Routes []struct {
							Match []struct {
								Host []string `json:"host"`
							} `json:"match"`
						} `json:"routes"`
					} `json:"srv0"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	indexOf := func(host string) int {
		for i, r := range doc.Apps.HTTP.Servers.Srv0.Routes {
			if len(r.Match) == 1 && len(r.Match[0].Host) == 1 && r.Match[0].Host[0] == host {
				return i
			}
		}
		return -1
	}
	exact := indexOf("bar.foo.test")
	wildcard := indexOf("*.foo.test")
	if exact == -1 || wildcard == -1 {
		t.Fatalf("missing routes: exact=%d wildcard=%d", exact, wildcard)
	}
	if exact > wildcard {
		t.Errorf("exact bar.foo.test (idx %d) must precede *.foo.test (idx %d)", exact, wildcard)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/caddyapi/ -run TestBuild -v`
Expected: FAIL — existing golden tests (`empty`, `one_backend`, `two_backends`) now mismatch because `Build` still emits single routes and the `*.test` subject; the two new tests fail too.

- [ ] **Step 4: Rewrite the route/subject section of `Build`**

In `internal/caddyapi/config.go`, replace the block from line 68 (`domains := make(...)`) through line 109 (the closing of the `config{...}` literal, ending at `}`) with:

```go
	domains := make([]string, 0, len(s.Backends))
	byDomain := make(map[string]state.Backend, len(s.Backends))
	for _, b := range s.Backends {
		domains = append(domains, b.Domain)
		byDomain[b.Domain] = b
	}
	sort.Strings(domains)

	// Each backend yields an exact route (D) and a wildcard route (*.D) to
	// the same upstream. All exact routes are emitted before all wildcard
	// routes: a Caddy `*` matches exactly one label, so a request like
	// bar.foo.test matches both the exact bar.foo.test route and the
	// *.foo.test wildcard. reverse_proxy is terminal, so ordering every
	// exact ahead of every wildcard guarantees an explicit override wins.
	exactRoutes := make([]route, 0, len(domains))
	wildcardRoutes := make([]route, 0, len(domains))
	subjectSet := make(map[string]struct{}, len(domains)*2)
	for _, d := range domains {
		b := byDomain[d]
		wildcard := "*." + d
		exactRoutes = append(exactRoutes, route{
			Match: []match{{Host: []string{d}}},
			Handle: []handler{{
				Handler:   "reverse_proxy",
				Upstreams: []upstream{{Dial: b.Upstream}},
			}},
		})
		wildcardRoutes = append(wildcardRoutes, route{
			Match: []match{{Host: []string{wildcard}}},
			Handle: []handler{{
				Handler:   "reverse_proxy",
				Upstreams: []upstream{{Dial: b.Upstream}},
			}},
		})
		subjectSet[d] = struct{}{}
		subjectSet[wildcard] = struct{}{}
	}
	routes := append(exactRoutes, wildcardRoutes...)

	// TLS subjects are derived from the registered names, not a static
	// *.test: a single-label wildcard can't cover bar.foo.test or a
	// *.foo.test wildcard cert. We mint certs only for names actually in use.
	subjects := make([]string, 0, len(subjectSet))
	for subj := range subjectSet {
		subjects = append(subjects, subj)
	}
	sort.Strings(subjects)

	cfg := config{
		Admin: adminConfig{Listen: "0.0.0.0:2019"},
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
						Subjects: subjects,
						Issuers:  []issuer{{Module: "internal"}},
					}},
				},
			},
		},
	}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/caddyapi/ -v`
Expected: PASS — all golden tests plus the two new tests. `TestBuildPreservesAdminBinding` still passes (admin block unchanged).

- [ ] **Step 6: Confirm nothing else broke**

Run: `go test ./internal/app/ -v`
Expected: PASS — App register tests assert on substrings like `myapp.test` / `apps`, which remain present.

- [ ] **Step 7: Commit**

```bash
git add internal/caddyapi/config.go internal/caddyapi/config_test.go internal/caddyapi/testdata/
git commit -m "Emitted wildcard routes and derived TLS subjects"
```

---

## Task 3: Cascade-aware Deregister

**Files:**
- Modify: `internal/app/deregister.go` (whole `Deregister` function + new result type)
- Modify: `internal/app/register_test.go` (existing deregister call sites + new tests)

- [ ] **Step 1: Update existing deregister call sites to the new signature (compile-first)**

The signature changes to `Deregister(ctx, name, cascade bool) (DeregisterResult, error)`. In `internal/app/register_test.go` there are three existing call sites (around lines 218, 239, 269). Update each:

Replace `removed, err := f.app.Deregister(context.Background(), "foo")` with:
```go
res, err := f.app.Deregister(context.Background(), "foo", false)
```
and replace later uses of `removed` with `res.Removed`. For the unknown-name test (around line 239) use `"unknown"` and `res.Removed`. For the Caddy-rejection test (around line 269) use `"foo"` and `res.Removed`.

(These will not compile until Step 3 adds the type/signature — that's expected; this step just edits the tests.)

- [ ] **Step 2: Write the new failing tests**

Append to `internal/app/register_test.go` (drop the `app.` prefixes to match the in-package style):

```go
// seedTwo registers a parent and a subdomain override through the fixture.
func seedParentAndChild(t *testing.T, f *fixture) {
	t.Helper()
	if err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Port: 2000}); err != nil {
		t.Fatalf("seed foo: %v", err)
	}
	if err := f.app.Register(context.Background(), RegisterInput{Name: "bar.foo", Port: 3000}); err != nil {
		t.Fatalf("seed bar.foo: %v", err)
	}
}

func TestDeregisterParentLeavesChildByDefault(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	seedParentAndChild(t, f)

	res, err := f.app.Deregister(context.Background(), "foo", false)
	if err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if !res.Removed {
		t.Error("parent should be removed")
	}
	if len(res.ChildrenLeft) != 1 || res.ChildrenLeft[0] != "bar.foo" {
		t.Errorf("ChildrenLeft: got %v want [bar.foo]", res.ChildrenLeft)
	}
	if res.ParentDomain != "foo.test" {
		t.Errorf("ParentDomain: got %q want foo.test", res.ParentDomain)
	}

	// bar.foo must survive in pushed config.
	if !strings.Contains(string(f.lastBody), "bar.foo.test") {
		t.Errorf("child route should survive: %s", f.lastBody)
	}
	if strings.Contains(string(f.lastBody), `"foo.test"`) {
		t.Errorf("parent exact route should be gone: %s", f.lastBody)
	}
}

func TestDeregisterCascadeRemovesChildren(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	seedParentAndChild(t, f)

	res, err := f.app.Deregister(context.Background(), "foo", true)
	if err != nil {
		t.Fatalf("Deregister cascade: %v", err)
	}
	if !res.Removed {
		t.Error("parent should be removed")
	}
	if len(res.Cascaded) != 1 || res.Cascaded[0] != "bar.foo" {
		t.Errorf("Cascaded: got %v want [bar.foo]", res.Cascaded)
	}
	if strings.Contains(string(f.lastBody), "bar.foo.test") {
		t.Errorf("child route should be gone under cascade: %s", f.lastBody)
	}
}

func TestDeregisterCascadeOnAbsentParentStillRemovesChildren(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "bar.foo", Port: 3000}); err != nil {
		t.Fatalf("seed bar.foo: %v", err)
	}
	res, err := f.app.Deregister(context.Background(), "foo", true)
	if err != nil {
		t.Fatalf("Deregister cascade: %v", err)
	}
	if res.Removed {
		t.Error("absent parent should report Removed=false")
	}
	if len(res.Cascaded) != 1 || res.Cascaded[0] != "bar.foo" {
		t.Errorf("Cascaded: got %v want [bar.foo]", res.Cascaded)
	}
}
```

- [ ] **Step 3: Rewrite `internal/app/deregister.go`**

Replace the entire file contents with:

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// DeregisterResult reports what Deregister did so the command layer can render
// the right messages.
type DeregisterResult struct {
	// Removed is true when the named parent backend existed and was removed.
	Removed bool
	// ParentDomain is the domain the children suffix was computed against
	// (the parent's stored domain, or <name>.test when the parent is absent).
	ParentDomain string
	// Cascaded lists child backend names removed because cascade was set
	// (sorted).
	Cascaded []string
	// ChildrenLeft lists child backend names left in place because cascade
	// was not set (sorted).
	ChildrenLeft []string
}

// Deregister removes a backend. With cascade=false it removes only the named
// backend, leaving any subdomain backends under it in place (their names are
// reported in ChildrenLeft). With cascade=true it also removes every backend
// whose domain is a subdomain of the parent's domain.
//
// Idempotent: when nothing matches (unknown name and either cascade=false or
// no children), it makes no Caddy call and returns Removed=false.
func (a *App) Deregister(ctx context.Context, name string, cascade bool) (DeregisterResult, error) {
	var res DeregisterResult

	st, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return res, fmt.Errorf("inspect container: %w", err)
	}
	if st != docker.StateRunning {
		return res, fmt.Errorf("%w (state=%s)", ErrCaddyNotRunning, st)
	}

	lock, err := state.AcquireLock(a.paths.LockPath, 5*time.Second)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return res, fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return res, err
	}
	defer func() {
		_ = lock.Release()
	}()

	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return res, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return res, err
	}

	parent, parentPresent := s.Backends[name]
	parentDomain := name + ".test"
	if parentPresent {
		parentDomain = parent.Domain
	}
	res.ParentDomain = parentDomain
	childSuffix := "." + parentDomain

	children := make([]string, 0)
	for k, v := range s.Backends {
		if k != name && strings.HasSuffix(v.Domain, childSuffix) {
			children = append(children, k)
		}
	}
	sort.Strings(children)

	remove := make(map[string]bool)
	if parentPresent {
		remove[name] = true
	}
	if cascade {
		for _, c := range children {
			remove[c] = true
		}
		res.Cascaded = children
	} else {
		res.ChildrenLeft = children
	}

	if len(remove) == 0 {
		// Nothing to remove: parent absent and (no cascade, or no children).
		return res, nil
	}

	// Work on a copy so a Caddy failure leaves on-disk state intact.
	updated := *s
	updated.Backends = make(map[string]state.Backend, len(s.Backends))
	for k, v := range s.Backends {
		if !remove[k] {
			updated.Backends[k] = v
		}
	}

	cfg := caddyapi.Build(&updated)
	if err := a.caddy.Load(ctx, cfg); err != nil {
		if errors.Is(err, caddyapi.ErrUnreachable) {
			return res, fmt.Errorf("%w: %v", ErrCaddyNotRunning, err)
		}
		return res, err
	}

	if err := state.Save(a.paths.StatePath, &updated); err != nil {
		return res, fmt.Errorf("save state: %w", err)
	}

	res.Removed = parentPresent
	return res, nil
}
```

Note: `ChildrenLeft` and `Cascaded` use `make([]string, 0)` via the `children` slice, so they marshal/compare as empty (non-nil) — but in the absent-parent-no-cascade early return, `ChildrenLeft` is the (possibly empty) children slice, which is fine.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/app/ -run Deregister -v`
Expected: PASS — the three new tests plus the three updated existing ones (`TestDeregisterRemovesBackendAndPushesConfig`, `TestDeregisterUnknownNameIsNoOp`, `TestDeregisterCaddyRejectionLeavesStateIntact`).

- [ ] **Step 5: Commit**

```bash
git add internal/app/deregister.go internal/app/register_test.go
git commit -m "Added cascade option to deregister"
```

---

## Task 4: Wire the `--cascade` flag and messages

**Files:**
- Modify: `cmd/deregister.go`
- Create: `cmd/deregister_test.go`

- [ ] **Step 1: Write the failing flag-wiring test**

Create `cmd/deregister_test.go`:

```go
package cmd

import "testing"

// TestDeregisterHasCascadeFlag confirms the --cascade boolean flag is wired.
func TestDeregisterHasCascadeFlag(t *testing.T) {
	c := newDeregisterCmd()
	f := c.Flags().Lookup("cascade")
	if f == nil {
		t.Fatal("expected --cascade flag on deregister command")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--cascade type: got %q want bool", f.Value.Type())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/ -run TestDeregisterHasCascadeFlag -v`
Expected: FAIL — flag does not exist yet (and the command currently returns a bare `*cobra.Command` so `c.Flags().Lookup` returns nil).

- [ ] **Step 3: Rewrite `cmd/deregister.go`**

Replace the file contents with:

```go
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newDeregisterCmd() *cobra.Command {
	var cascade bool
	c := &cobra.Command{
		Use:   "deregister <name>",
		Short: "Deregister a previously registered backend",
		Long: `Deregister a previously registered backend.

By default only the named backend is removed; any subdomain backends
registered under it (e.g. bar.foo when removing foo) are left in place and
reported on stderr. Pass --cascade to also remove every subdomain backend
under the named one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			res, err := a.Deregister(cmd.Context(), args[0], cascade)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()

			for _, child := range res.Cascaded {
				_, _ = fmt.Fprintln(out, "deregistered:", child)
			}
			if res.Removed {
				_, _ = fmt.Fprintln(out, "deregistered:", args[0])
				if len(res.ChildrenLeft) > 0 {
					_, _ = fmt.Fprintf(errOut,
						"%d subdomain backend(s) still registered under %s (use --cascade to remove)\n",
						len(res.ChildrenLeft), res.ParentDomain)
				}
			} else if len(res.Cascaded) == 0 {
				_, _ = fmt.Fprintln(errOut, "no such backend:", args[0])
			}
			return nil
		},
	}
	c.Flags().BoolVar(&cascade, "cascade", false,
		"Also remove subdomain backends registered under <name>")
	return c
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/ -run TestDeregister -v`
Expected: PASS.

- [ ] **Step 5: Run the full unit suite**

Run: `go test ./...`
Expected: PASS across all packages (integration tests are behind the `integration` build tag and are not run here).

- [ ] **Step 6: Commit**

```bash
git add cmd/deregister.go cmd/deregister_test.go
git commit -m "Wired --cascade flag into deregister command"
```

---

## Task 5: Documentation

**Files:**
- Modify: `cmd/register.go` (long help)
- Modify: `README.md`

- [ ] **Step 1: Extend the register long help**

In `cmd/register.go`, append a paragraph to the existing `Long` string (after the existing text, before the closing backtick):

```
Subdomains: registering a backend automatically serves every subdomain of
its domain from the same upstream, so "register foo --port 2000" makes
foo.test and *.foo.test (bar.foo.test, etc.) all reach port 2000. To send one
subdomain elsewhere, register it explicitly with a dotted name:
"register bar.foo --port 3000".
```

- [ ] **Step 2: Add a Subdomains section to the README**

In `README.md`, after the "Registering a backend" section (the block ending before "### Listing and deregistering" around line 113), insert:

```markdown
### Subdomains

Registering a backend automatically serves all of its subdomains from the same
upstream. After `caddy_shack register foo --port 2000`, both `foo.test` and any
`*.foo.test` (e.g. `bar.foo.test`, `api.foo.test`) reverse-proxy to port 2000 -
no extra commands.

To point one subdomain at a different service, register it with a dotted name:

```
caddy_shack register bar.foo --port 3000
# bar.foo.test now serves port 3000; the rest of *.foo.test still serves 2000
```

The explicit subdomain always wins over the parent's wildcard.
```

Then update the "Listing and deregistering" section to document `--cascade`:

```markdown
caddy_shack deregister myapp            # removes myapp and its *.myapp.test wildcard
caddy_shack deregister myapp --cascade  # also removes subdomain overrides (e.g. bar.myapp)
```

By default, deregistering a parent leaves any explicitly-registered subdomain
backends in place and notes how many remain on stderr. `--cascade` removes them
too.

- [ ] **Step 3: Verify the build still compiles and help renders**

Run: `go build ./... && go run . deregister --help`
Expected: build succeeds; help text shows the `--cascade` flag and the new long description.

- [ ] **Step 4: Commit**

```bash
git add cmd/register.go README.md
git commit -m "Documented subdomain routing and --cascade"
```

---

## Task 6: Integration test (real Caddy, wildcard + override + wildcard TLS)

This task resolves the spec's open question: whether Caddy's internal CA serves the `*.foo.test` wildcard cert cleanly, or whether on-demand TLS is needed.

**Files:**
- Create: `test/integration/subdomain_e2e_test.go`

- [ ] **Step 1: Write the integration test**

Create `test/integration/subdomain_e2e_test.go`. It mirrors `register_e2e_test.go`'s container setup and reuses the existing helpers (`SkipUnlessDocker`, `CleanupContainer`, `WaitForCaddyAdmin`, `getWithRetry`, `freePort`, `fakeRunningDocker`) and the `testContainerName` const, all already in this package:

```go
//go:build integration

package integration

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/caddyapi"
)

func TestSubdomainE2EWildcardAndOverride(t *testing.T) {
	SkipUnlessDocker(t)
	CleanupContainer(t)

	adminPort := freePort(t)
	httpsPort := freePort(t)
	httpPort := freePort(t)

	caddyfile := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(caddyfile, []byte("{\n\tadmin 0.0.0.0:2019\n}\n"), 0o644); err != nil {
		t.Fatalf("write Caddyfile: %v", err)
	}

	run := exec.Command("docker", "run", "-d", "--name", testContainerName,
		"--add-host=host.docker.internal:host-gateway",
		"-p", fmt.Sprintf("127.0.0.1:%d:2019", adminPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:443", httpsPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:80", httpPort),
		"-v", fmt.Sprintf("%s:/etc/caddy/Caddyfile:ro", caddyfile),
		"caddy:2",
	)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}

	adminURL := fmt.Sprintf("http://127.0.0.1:%d", adminPort)
	WaitForCaddyAdmin(t, adminURL, 15*time.Second)

	parentPort := startBodyUpstream(t, "parent")
	childPort := startBodyUpstream(t, "child")

	statePath := filepath.Join(t.TempDir(), "state.json")
	a := app.NewWithPaths(
		app.Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(adminURL, 5*time.Second),
		fakeRunningDocker{},
	)

	if err := a.Register(context.Background(), app.RegisterInput{Name: "foo", Port: parentPort}); err != nil {
		t.Fatalf("Register foo: %v", err)
	}
	if err := a.Register(context.Background(), app.RegisterInput{Name: "bar.foo", Port: childPort}); err != nil {
		t.Fatalf("Register bar.foo: %v", err)
	}

	client := httpsClient(httpsPort)

	// Wildcard: an arbitrary subdomain of foo hits the parent upstream.
	assertBody(t, client, "https://random.foo.test/", "parent")
	// Exact apex still hits the parent.
	assertBody(t, client, "https://foo.test/", "parent")
	// Override: bar.foo.test wins over *.foo.test and hits the child upstream.
	assertBody(t, client, "https://bar.foo.test/", "child")

	// Non-cascade deregister of the parent leaves the override working.
	if _, err := a.Deregister(context.Background(), "foo", false); err != nil {
		t.Fatalf("Deregister foo: %v", err)
	}
	assertBody(t, client, "https://bar.foo.test/", "child")
}

func startBodyUpstream(t *testing.T, body string) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}),
		ReadHeaderTimeout: time.Second,
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	_, portStr, _ := net.SplitHostPort(l.Addr().String())
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse upstream port: %v", err)
	}
	return port
}

func httpsClient(httpsPort int) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", httpsPort))
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 5 * time.Second,
	}
}

func assertBody(t *testing.T, client *http.Client, url, want string) {
	t.Helper()
	resp, body := getWithRetry(t, client, url, 5*time.Second)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("%s status: %d body=%s", url, resp.StatusCode, body)
	}
	if string(body) != want {
		t.Errorf("%s body: got %q want %q", url, body, want)
	}
}
```

- [ ] **Step 2: Run the integration test**

Run: `go test -tags integration ./test/integration/ -run TestSubdomainE2EWildcardAndOverride -v`
Expected: PASS if docker is available (otherwise SKIP). A request to `random.foo.test` returns `parent`, `bar.foo.test` returns `child`, and the override survives non-cascade deregister.

**If `bar.foo.test` or `random.foo.test` fails with a TLS handshake error that never clears within the retry window**, the internal CA is not serving the wildcard cert. In that case, resolve the open question by switching the TLS policy to on-demand: add `OnDemand bool` support to the policy struct in `internal/caddyapi/config.go` (set `"on_demand": true` and an `on_demand_tls` ask-less config under the `tls` app), update the golden fixtures accordingly, and re-run. Document the outcome in the spec's "Open question" section before finishing. Do not change the approach unless this test actually fails.

- [ ] **Step 3: Run the full integration suite to check for regressions**

Run: `go test -tags integration ./test/integration/ -v`
Expected: PASS/SKIP — existing `TestRegisterE2EReverseProxiesOverHTTPS` and the others still pass.

- [ ] **Step 4: Commit**

```bash
git add test/integration/subdomain_e2e_test.go
git commit -m "Added subdomain wildcard and override integration test"
```

---

## Final verification

- [ ] **Run the local pre-CI gauntlet**

Run: `make pre-ci`
Expected: lint + unit tests pass. (Run `make pre-ci-fix` first if formatting needs fixing.)

- [ ] **Update the spec's open question**

If Task 6 confirmed the internal CA serves wildcard certs, edit the "Open question" section of `docs/superpowers/specs/2026-06-06-subdomain-support-design.md` to record the result (resolved: internal CA issues `*.<domain>` certs, no on-demand needed — or the opposite, with what changed). Commit:

```bash
git add docs/superpowers/specs/2026-06-06-subdomain-support-design.md
git commit -m "Recorded wildcard TLS verification outcome"
```

---

## Spec coverage check

- Automatic wildcard routing → Task 2 (exact + `*.D` routes).
- Override via dotted name → Task 1 (regex) + Task 2 (exact-before-wildcard ordering).
- Exact override wins → Task 2 (`TestBuildOrdersExactRoutesBeforeWildcards`) + Task 6 (real Caddy).
- `--cascade` on deregister → Task 3 (logic) + Task 4 (flag/messages).
- Default leaves children, with stderr note → Task 3 (`ChildrenLeft`) + Task 4 (message).
- TLS for deep names + wildcard certs → Task 2 (derived subjects) + Task 6 (verification, on-demand fallback).
- Override without parent allowed → Task 1 accepts the name; no conflict logic added (registers normally).
- Docs (help + README) → Task 5.

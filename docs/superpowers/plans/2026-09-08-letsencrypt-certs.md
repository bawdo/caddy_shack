# Let's Encrypt Certificate Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an optional `caddy_shack cert` command group that obtains a Let's Encrypt wildcard certificate for a public domain via manual DNS-01 and loads it into the running Caddy, while `.test` domains keep using the internal CA unchanged.

**Architecture:** caddy_shack drives the ACME order itself using `go-acme/lego/v4` (Caddy's own automation cannot pause for a human to edit DNS). Certificate material is stored on the host under the config directory and pushed into Caddy as `apps.tls.certificates.load_pem`, with those subjects removed from the `internal` issuer automation policy. Routing is untouched: the existing exact-plus-`*.d` route pair already matches what a wildcard certificate covers.

**Tech Stack:** Go 1.25, cobra, `github.com/go-acme/lego/v4` v4.35.2, Docker CLI, Caddy 2 admin API.

**Spec:** `docs/superpowers/specs/2026-09-08-letsencrypt-certs-design.md`

## Global Constraints

- Australian English in all prose, comments and command help text.
- No em dashes or en dashes anywhere, including code comments and commit messages.
- Commit subjects in past tense, 60 characters or fewer.
- Ask the user for permission before every commit. Sign with gpg (`git commit -S`). If signing fails, stop and ask for the key to be refreshed. Never commit unsigned.
- **Never** put agent attribution in a commit message: no `Co-Authored-By`, no "Generated with", no session URL, no vendor name.
- No hardcoded secrets. Private keys are generated at runtime and written with mode `0600`.
- Every task must leave `make pre-ci` passing (gofmt, go vet, golangci-lint, `go test ./...`).
- Linters enabled: `errcheck`, `govet`, `staticcheck`, `ineffassign`, `gosec`. `errcheck` means every returned error is handled or explicitly discarded with `_ =`.
- New packages under `internal/` follow the existing pattern: exported types documented with a comment starting with the identifier name.
- Do not modify existing tests. If an existing test fails, that is a signal this feature changed behaviour it should not have. Stop and report it.

## File Structure

**Created:**

| File | Responsibility |
|---|---|
| `internal/certstore/certstore.go` | Owns certificate material on disk. Atomic writes, `0600` key mode. |
| `internal/certstore/certstore_test.go` | Unit tests for the above. |
| `internal/acme/acme.go` | `Issuer` interface, `Request`/`Result` types, LE directory constants. |
| `internal/acme/lego.go` | Real issuer wrapping lego. |
| `internal/acme/manual.go` | Custom `challenge.Provider` and the one-shot prompt hook. |
| `internal/acme/manual_test.go` | Unit tests for the provider, no network. |
| `internal/acme/fake.go` | Fake issuer for `app` tests. |
| `internal/app/cert.go` | `CertRequest`, `CertList`, `CertRenew`, `CertRemove`. |
| `internal/app/cert_test.go` | Unit tests for the above against the fake issuer. |
| `internal/app/certload.go` | `loadedCerts` helper shared by every `caddyapi.Build` call site. |
| `cmd/cert.go` | `cert` parent command plus the four subcommands and the CLI prompter. |
| `cmd/cert_test.go` | Rendering and prompt tests driven through cobra streams. |
| `test/integration/cert_e2e_test.go` | Pebble end-to-end proving the hypothesis. |

**Modified:**

| File | Change |
|---|---|
| `internal/state/state.go` | `SchemaVersion` 1 to 2, `Cert` type, `Certs` map, v1 migration. |
| `internal/app/paths.go` | `CertStoreDir` field. |
| `internal/caddyapi/config.go` | `Build` takes certificates, emits `load_pem`, excludes those subjects. |
| `internal/app/register.go:123` | Pass certificates to `Build`. |
| `internal/app/deregister.go:112` | Pass certificates to `Build`. |
| `internal/app/upgrade.go:105` | Pass certificates to `Build`. |
| `internal/app/doctor_checks.go:596,635` | Pass certificates to `Build`. |
| `internal/app/errors.go` | `ErrCertIssuance` sentinel, exit code 9. |
| `internal/app/app.go` | `App` gains an `acme.Issuer` field and a `*certstore.Store`. |
| `cmd/root.go` | Register `newCertCmd()`, document exit code 9. |
| `README.md` | Let's Encrypt section, exit code 9. |
| `go.mod` / `go.sum` | lego dependency. |

**Critical integration point.** There are six `caddyapi.Build` call sites. Any one of them that pushes a config without the certificates will **silently wipe the loaded certificate from the running Caddy**. `applyDriftFix` in `doctor_checks.go` is the least obvious of them. Task 4 exists specifically to close this.

---

### Task 1: State schema v2 with v1 migration

**Files:**
- Modify: `internal/state/state.go`
- Test: `internal/state/state_test.go` (add tests, do not alter existing ones)

**Interfaces:**
- Consumes: nothing.
- Produces: `state.SchemaVersion = 2`; `state.Cert{Domain, Subjects []string, Env, IssuedAt, NotAfter string}`; `state.State.Certs map[string]Cert`. `state.Load` migrates a v1 file instead of returning `*ErrCorrupt`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/state/state_test.go`:

```go
func TestLoadMigratesV1FileToV2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	v1 := `{
  "schema_version": 1,
  "backends": {
    "hello": {
      "domain": "hello.test",
      "upstream": "host.docker.internal:3000",
      "registered_at": "2026-05-27T10:00:00+10:00"
    }
  }
}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatalf("write v1 fixture: %v", err)
	}

	s, err := Load(path)
	if err != nil {
		t.Fatalf("a v1 file must migrate, not error: %v", err)
	}
	if s.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", s.SchemaVersion, SchemaVersion)
	}
	if s.Certs == nil {
		t.Error("Certs must be non-nil after migration")
	}
	if len(s.Certs) != 0 {
		t.Errorf("migrated state must have no certs, got %d", len(s.Certs))
	}
	b, ok := s.Backends["hello"]
	if !ok {
		t.Fatal("migration lost the backends")
	}
	if b.Domain != "hello.test" || b.Upstream != "host.docker.internal:3000" {
		t.Errorf("migration corrupted backend: %+v", b)
	}
}

func TestLoadRejectsUnknownFutureSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"schema_version": 99}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := Load(path); !IsCorrupt(err) {
		t.Errorf("a future schema_version must be corrupt, got %v", err)
	}
}

func TestCertsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	want := Cert{
		Domain:   "alpha.arrix.dev",
		Subjects: []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
		Env:      "staging",
		Email:    "k@example.com",
		IssuedAt: "2026-09-08T10:00:00+10:00",
		NotAfter: "2026-12-07T10:00:00+10:00",
	}
	s := &State{
		SchemaVersion: SchemaVersion,
		Backends:      map[string]Backend{},
		Certs:         map[string]Cert{"alpha.arrix.dev": want},
	}
	if err := Save(path, s); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(got.Certs["alpha.arrix.dev"], want) {
		t.Errorf("cert round trip mismatch\ngot:  %+v\nwant: %+v", got.Certs["alpha.arrix.dev"], want)
	}
}
```

Ensure the test file imports `reflect`, `os` and `path/filepath`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/state/ -run 'Migrates|FutureSchema|CertsRoundTrip' -v`
Expected: FAIL. `TestLoadMigratesV1FileToV2` fails with a corrupt-state error, `TestCertsRoundTrip` fails to compile because `Cert` is undefined.

- [ ] **Step 3: Implement the schema change**

In `internal/state/state.go`, change the constant and add the type:

```go
// SchemaVersion is bumped when the on-disk JSON shape changes in a
// non-backward-compatible way. Version 2 added the certs map; version 1
// files are migrated forward on load.
const SchemaVersion = 2

// Cert is the metadata for one externally-issued certificate. The material
// itself lives in the cert store on disk, not here.
type Cert struct {
	Domain   string   `json:"domain"`
	Subjects []string `json:"subjects"`
	// Env is "staging" or "production", naming the ACME directory the
	// certificate came from. Staging certificates are not publicly trusted.
	Env string `json:"env"`
	// Email is the ACME account contact used for this certificate, kept so
	// renewal does not have to ask for it again.
	Email    string `json:"email"`
	IssuedAt string `json:"issued_at"`
	NotAfter string `json:"not_after"`
}
```

Add the field to `State`:

```go
// State is the on-disk shape of state.json.
type State struct {
	SchemaVersion int                `json:"schema_version"`
	Backends      map[string]Backend `json:"backends"`
	Certs         map[string]Cert    `json:"certs"`
}
```

Replace the version check in `Load` with a forward migration:

```go
	// Version 1 files predate the certs map. They are structurally a subset
	// of version 2, so migrate forward rather than declaring them corrupt:
	// an install in the field must not hit exit code 7 on upgrade.
	switch s.SchemaVersion {
	case 1:
		s.SchemaVersion = SchemaVersion
	case SchemaVersion:
	default:
		return nil, &ErrCorrupt{
			Path:   path,
			Reason: fmt.Errorf("unknown schema_version %d (want %d)", s.SchemaVersion, SchemaVersion),
		}
	}
	if s.Backends == nil {
		s.Backends = map[string]Backend{}
	}
	if s.Certs == nil {
		s.Certs = map[string]Cert{}
	}
	return &s, nil
```

Also update the empty-state return at the top of `Load` so a missing file yields a non-nil `Certs`:

```go
	if errors.Is(err, os.ErrNotExist) {
		return &State{
			SchemaVersion: SchemaVersion,
			Backends:      map[string]Backend{},
			Certs:         map[string]Cert{},
		}, nil
	}
```

- [ ] **Step 4: Run the full state suite**

Run: `go test ./internal/state/ -v`
Expected: PASS, including every pre-existing test.

- [ ] **Step 5: Run the whole suite for migration fallout**

Run: `go test ./...`
Expected: PASS. Any failure here means an existing test hardcoded `schema_version: 1`; report it rather than editing the test.

- [ ] **Step 6: Commit**

```bash
git add internal/state/state.go internal/state/state_test.go
git commit -S -m "Added certs map to state schema version 2"
```

---

### Task 2: Certificate store

**Files:**
- Create: `internal/certstore/certstore.go`
- Create: `internal/certstore/certstore_test.go`
- Modify: `internal/app/paths.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `certstore.New(dir string) *Store`
  - `(*Store) Save(domain string, certPEM, keyPEM []byte) error`
  - `(*Store) Load(domain string) (Material, error)`
  - `(*Store) Remove(domain string) error`
  - `(*Store) Has(domain string) bool`
  - `certstore.Material{Domain string, CertPEM []byte, KeyPEM []byte}`
  - `certstore.ErrNotFound`
  - `app.Paths` gains `CertStoreDir string`.

- [ ] **Step 1: Write the failing test**

Create `internal/certstore/certstore_test.go`:

```go
package certstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const (
	testCertPEM = "-----BEGIN CERTIFICATE-----\nCERT\n-----END CERTIFICATE-----\n"
	testKeyPEM  = "-----BEGIN EC PRIVATE KEY-----\nKEY\n-----END EC PRIVATE KEY-----\n"
)

func TestSaveThenLoadRoundTrips(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save("alpha.arrix.dev", []byte(testCertPEM), []byte(testKeyPEM)); err != nil {
		t.Fatalf("save: %v", err)
	}
	m, err := s.Load("alpha.arrix.dev")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if string(m.CertPEM) != testCertPEM {
		t.Errorf("cert PEM = %q, want %q", m.CertPEM, testCertPEM)
	}
	if string(m.KeyPEM) != testKeyPEM {
		t.Errorf("key PEM = %q, want %q", m.KeyPEM, testKeyPEM)
	}
	if m.Domain != "alpha.arrix.dev" {
		t.Errorf("domain = %q, want alpha.arrix.dev", m.Domain)
	}
}

func TestSaveWritesKeyAsOwnerReadOnly(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := s.Save("alpha.arrix.dev", []byte(testCertPEM), []byte(testKeyPEM)); err != nil {
		t.Fatalf("save: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "certs", "alpha.arrix.dev", "key.pem"))
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("key.pem mode = %#o, want 0600 (a private key must not be group or world readable)", perm)
	}
}

func TestLoadMissingDomainReportsNotFound(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Load("nope.example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSaveOverwritesExistingMaterial(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save("alpha.arrix.dev", []byte("old"), []byte("oldkey")); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := s.Save("alpha.arrix.dev", []byte(testCertPEM), []byte(testKeyPEM)); err != nil {
		t.Fatalf("second save: %v", err)
	}
	m, err := s.Load("alpha.arrix.dev")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if string(m.CertPEM) != testCertPEM {
		t.Errorf("renewal must replace material, got %q", m.CertPEM)
	}
}

func TestRemoveDeletesMaterialAndIsIdempotent(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save("alpha.arrix.dev", []byte(testCertPEM), []byte(testKeyPEM)); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.Remove("alpha.arrix.dev"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if s.Has("alpha.arrix.dev") {
		t.Error("Has must be false after Remove")
	}
	if err := s.Remove("alpha.arrix.dev"); err != nil {
		t.Errorf("Remove must be idempotent, second call returned %v", err)
	}
}

func TestHasReportsPresence(t *testing.T) {
	s := New(t.TempDir())
	if s.Has("alpha.arrix.dev") {
		t.Error("Has must be false on an empty store")
	}
	if err := s.Save("alpha.arrix.dev", []byte(testCertPEM), []byte(testKeyPEM)); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !s.Has("alpha.arrix.dev") {
		t.Error("Has must be true after Save")
	}
}

func TestSaveRejectsDomainWithPathSeparator(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save("../escape", []byte(testCertPEM), []byte(testKeyPEM)); err == nil {
		t.Error("a domain containing a path separator must be rejected, not written outside the store")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/certstore/ -v`
Expected: FAIL to build, package `certstore` does not exist.

- [ ] **Step 3: Implement the store**

Create `internal/certstore/certstore.go`:

```go
// Package certstore owns externally-issued certificate material on the host
// filesystem. Material is kept here rather than inside Caddy's data volume so
// that a container recreate or upgrade can restore certificates by the same
// mechanism that restores backends: re-push from local state.
package certstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound reports that the store holds no material for a domain.
var ErrNotFound = errors.New("certstore: no certificate material for domain")

// Material is one domain's certificate chain and private key, PEM encoded.
type Material struct {
	Domain  string
	CertPEM []byte
	KeyPEM  []byte
}

// Store is a directory holding certificate material, one subdirectory per
// domain, plus ACME account keys.
type Store struct {
	dir string
}

// New returns a Store rooted at dir. The directory is created lazily on the
// first write.
func New(dir string) *Store {
	return &Store{dir: dir}
}

// validDomain rejects anything that could escape the store directory. Domains
// reach here from the CLI, so this is the boundary that keeps a crafted
// argument from writing outside the store.
func validDomain(domain string) error {
	if domain == "" {
		return fmt.Errorf("certstore: empty domain")
	}
	if strings.ContainsAny(domain, `/\`) || strings.Contains(domain, "..") {
		return fmt.Errorf("certstore: invalid domain %q", domain)
	}
	if domain != filepath.Clean(domain) {
		return fmt.Errorf("certstore: invalid domain %q", domain)
	}
	return nil
}

func (s *Store) domainDir(domain string) string {
	return filepath.Join(s.dir, "certs", domain)
}

// CertPath returns the certificate chain path for a domain.
func (s *Store) CertPath(domain string) string {
	return filepath.Join(s.domainDir(domain), "fullchain.pem")
}

// KeyPath returns the private key path for a domain.
func (s *Store) KeyPath(domain string) string {
	return filepath.Join(s.domainDir(domain), "key.pem")
}

// AccountKeyPath returns the ACME account key path for an environment
// ("staging" or "production"). Accounts are per environment because staging
// and production are separate ACME servers with separate account namespaces.
func (s *Store) AccountKeyPath(env string) string {
	return filepath.Join(s.dir, "acme", "account-"+env+".key")
}

// Save writes the chain and key for a domain, replacing any existing
// material. The key is written owner-read-only. Writes are atomic per file.
func (s *Store) Save(domain string, certPEM, keyPEM []byte) error {
	if err := validDomain(domain); err != nil {
		return err
	}
	dir := s.domainDir(domain)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("certstore: mkdir %s: %w", dir, err)
	}
	if err := writeFileAtomic(s.CertPath(domain), certPEM, 0o644); err != nil {
		return err
	}
	return writeFileAtomic(s.KeyPath(domain), keyPEM, 0o600)
}

// Load returns the material for a domain, or ErrNotFound.
func (s *Store) Load(domain string) (Material, error) {
	if err := validDomain(domain); err != nil {
		return Material{}, err
	}
	certPEM, err := os.ReadFile(s.CertPath(domain))
	if errors.Is(err, os.ErrNotExist) {
		return Material{}, fmt.Errorf("%w: %s", ErrNotFound, domain)
	}
	if err != nil {
		return Material{}, fmt.Errorf("certstore: read cert: %w", err)
	}
	keyPEM, err := os.ReadFile(s.KeyPath(domain))
	if errors.Is(err, os.ErrNotExist) {
		return Material{}, fmt.Errorf("%w: %s (key missing)", ErrNotFound, domain)
	}
	if err != nil {
		return Material{}, fmt.Errorf("certstore: read key: %w", err)
	}
	return Material{Domain: domain, CertPEM: certPEM, KeyPEM: keyPEM}, nil
}

// Has reports whether material exists for a domain.
func (s *Store) Has(domain string) bool {
	if err := validDomain(domain); err != nil {
		return false
	}
	_, err := os.Stat(s.CertPath(domain))
	return err == nil
}

// Remove deletes a domain's material. Idempotent: removing material that is
// not there is not an error.
func (s *Store) Remove(domain string) error {
	if err := validDomain(domain); err != nil {
		return err
	}
	if err := os.RemoveAll(s.domainDir(domain)); err != nil {
		return fmt.Errorf("certstore: remove %s: %w", domain, err)
	}
	return nil
}

// writeFileAtomic writes via a temp file and rename so a crash mid-write
// cannot leave a truncated certificate or key behind.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("certstore: open tmp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("certstore: write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("certstore: sync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("certstore: close tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("certstore: rename tmp: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/certstore/ -v`
Expected: PASS, all seven tests.

- [ ] **Step 5: Add the paths**

In `internal/app/paths.go`, extend the struct and constructor:

```go
// Paths bundles every filesystem path the app touches.
type Paths struct {
	StatePath string
	LockPath  string
	// CertStoreDir roots the certificate store: certs/<domain>/ and
	// acme/account-<env>.key live beneath it.
	CertStoreDir string
}
```

In `DefaultPaths`, add the field to the returned struct:

```go
	return Paths{
		StatePath:    filepath.Join(dir, "state.json"),
		LockPath:     filepath.Join(dir, "state.json.lock"),
		CertStoreDir: dir,
	}, nil
```

- [ ] **Step 6: Verify nothing broke**

Run: `go test ./... && go vet ./...`
Expected: PASS. `Paths` is constructed with field names in tests, so adding a field is source compatible. If any test constructs `Paths` positionally, report it rather than editing it.

- [ ] **Step 7: Commit**

```bash
git add internal/certstore/ internal/app/paths.go
git commit -S -m "Added on-host certificate store"
```

---

### Task 3: Build emits loaded certificates

**Files:**
- Modify: `internal/caddyapi/config.go`
- Test: `internal/caddyapi/config_test.go` (add tests, do not alter existing ones)
- Modify: `internal/app/register.go:123`, `internal/app/deregister.go:112`, `internal/app/upgrade.go:105`, `internal/app/doctor_checks.go:596`, `internal/app/doctor_checks.go:635`

**Interfaces:**
- Consumes: `state.State` with `Certs` from Task 1.
- Produces: `caddyapi.Build(s *state.State, certs []LoadedCert) []byte` and `caddyapi.LoadedCert{Subjects []string, CertPEM string, KeyPEM string}`.

This task changes a signature used in six places. Call sites are updated to pass `nil` here; Task 4 gives them real certificates. Passing `nil` reproduces today's output exactly, which is what keeps the existing golden tests green.

- [ ] **Step 1: Write the failing tests**

Append to `internal/caddyapi/config_test.go`:

```go
func TestBuildWithNoCertsIsUnchanged(t *testing.T) {
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends: map[string]state.Backend{
			"hello": {Domain: "hello.test", Upstream: "host.docker.internal:3000"},
		},
		Certs: map[string]state.Cert{},
	}
	got := Build(s, nil)

	var cfg map[string]any
	if err := json.Unmarshal(got, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tlsApp := cfg["apps"].(map[string]any)["tls"].(map[string]any)
	if _, present := tlsApp["certificates"]; present {
		t.Error("with no certs, the config must not carry a certificates block")
	}
}

func TestBuildLoadsCertPEMAndExcludesItsSubjects(t *testing.T) {
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends: map[string]state.Backend{
			"hello": {Domain: "hello.test", Upstream: "host.docker.internal:3000"},
			"web":   {Domain: "alpha.arrix.dev", Upstream: "host.docker.internal:5173"},
		},
		Certs: map[string]state.Cert{},
	}
	certs := []LoadedCert{{
		Subjects: []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
		CertPEM:  "-----BEGIN CERTIFICATE-----\nCERT\n-----END CERTIFICATE-----\n",
		KeyPEM:   "-----BEGIN EC PRIVATE KEY-----\nKEY\n-----END EC PRIVATE KEY-----\n",
	}}
	got := Build(s, certs)

	var cfg map[string]any
	if err := json.Unmarshal(got, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tlsApp := cfg["apps"].(map[string]any)["tls"].(map[string]any)

	loadPEM, ok := tlsApp["certificates"].(map[string]any)["load_pem"].([]any)
	if !ok || len(loadPEM) != 1 {
		t.Fatalf("expected exactly one load_pem entry, got %v", tlsApp["certificates"])
	}
	entry := loadPEM[0].(map[string]any)
	if entry["certificate"] != certs[0].CertPEM {
		t.Errorf("certificate = %q, want the chain PEM verbatim", entry["certificate"])
	}
	if entry["key"] != certs[0].KeyPEM {
		t.Errorf("key = %q, want the key PEM verbatim", entry["key"])
	}

	// The internal issuer must not claim the LE subjects, or Caddy would
	// mint an internal certificate that shadows the loaded one.
	policies := tlsApp["automation"].(map[string]any)["policies"].([]any)
	subjects := policies[0].(map[string]any)["subjects"].([]any)
	for _, sub := range subjects {
		if sub == "alpha.arrix.dev" || sub == "*.alpha.arrix.dev" {
			t.Errorf("internal policy must not claim %q, it has a loaded certificate", sub)
		}
	}
	// The .test subjects must still be there.
	var sawTest bool
	for _, sub := range subjects {
		if sub == "hello.test" {
			sawTest = true
		}
	}
	if !sawTest {
		t.Error("internal policy lost hello.test")
	}
}

func TestBuildKeepsRoutesForCertCoveredDomains(t *testing.T) {
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends: map[string]state.Backend{
			"web": {Domain: "alpha.arrix.dev", Upstream: "host.docker.internal:5173"},
		},
		Certs: map[string]state.Cert{},
	}
	certs := []LoadedCert{{
		Subjects: []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
		CertPEM:  "cert", KeyPEM: "key",
	}}
	got := Build(s, certs)

	var cfg map[string]any
	if err := json.Unmarshal(got, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	routes := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["srv0"].(map[string]any)["routes"].([]any)
	if len(routes) != 2 {
		t.Fatalf("expected exact + wildcard routes, got %d", len(routes))
	}
}

func TestBuildOmitsAutomationPolicyWhenEveryDomainHasACert(t *testing.T) {
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends: map[string]state.Backend{
			"web": {Domain: "alpha.arrix.dev", Upstream: "host.docker.internal:5173"},
		},
		Certs: map[string]state.Cert{},
	}
	certs := []LoadedCert{{
		Subjects: []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
		CertPEM:  "cert", KeyPEM: "key",
	}}
	got := Build(s, certs)

	var cfg map[string]any
	if err := json.Unmarshal(got, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tlsApp := cfg["apps"].(map[string]any)["tls"].(map[string]any)
	automation, present := tlsApp["automation"]
	if present {
		policies := automation.(map[string]any)["policies"]
		if policies != nil && len(policies.([]any)) != 0 {
			t.Errorf("a policy with no subjects would make Caddy manage every hostname; got %v", policies)
		}
	}
}
```

Ensure the test file imports `encoding/json`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/caddyapi/ -v`
Expected: FAIL to compile, `Build` takes one argument and `LoadedCert` is undefined.

- [ ] **Step 3: Change Build**

In `internal/caddyapi/config.go`, add the exported type above `Build`:

```go
// LoadedCert is one externally-issued certificate to hand Caddy directly.
// Subjects are the DNS names it covers; they are withheld from the internal
// issuer's automation policy so Caddy serves this certificate rather than
// minting its own over the top.
type LoadedCert struct {
	Subjects []string
	CertPEM  string
	KeyPEM   string
}
```

Change the signature and the TLS app construction. Replace the `subjects` block and the `cfg` literal with:

```go
	// Subjects covered by a loaded certificate are withheld from the
	// internal issuer. Caddy's automatic HTTPS skips a hostname that already
	// has a manually loaded certificate, but leaving the name in an explicit
	// internal policy would ask Caddy to manage it regardless.
	covered := make(map[string]bool)
	for _, c := range certs {
		for _, sub := range c.Subjects {
			covered[sub] = true
		}
	}

	// TLS subjects are derived from the registered names, not a static
	// *.test: a single-label wildcard can't cover bar.foo.test or a
	// *.foo.test wildcard cert. We mint certs only for names actually in use.
	subjects := make([]string, 0, len(domains)*2)
	for _, d := range domains {
		if !covered[d] {
			subjects = append(subjects, d)
		}
		if w := "*." + d; !covered[w] {
			subjects = append(subjects, w)
		}
	}
	sort.Strings(subjects)

	// An automation policy with an empty subjects list means "every
	// hostname", which would hand every name to the internal issuer. Emit no
	// policy at all in that case.
	policies := []policy{}
	if len(subjects) > 0 {
		policies = append(policies, policy{
			Subjects: subjects,
			Issuers:  []issuer{{Module: "internal"}},
		})
	}

	loadPEM := make([]pemPair, 0, len(certs))
	for _, c := range certs {
		loadPEM = append(loadPEM, pemPair{Certificate: c.CertPEM, Key: c.KeyPEM})
	}

	tls := tlsApp{Automation: automation{Policies: policies}}
	if len(loadPEM) > 0 {
		tls.Certificates = &certificates{LoadPEM: loadPEM}
	}

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
			TLS: tls,
		},
	}
```

Add the two new local types alongside the existing ones, and change `tlsApp`:

```go
	type pemPair struct {
		Certificate string `json:"certificate"`
		Key         string `json:"key"`
	}
	type certificates struct {
		LoadPEM []pemPair `json:"load_pem"`
	}
	type tlsApp struct {
		Automation   automation    `json:"automation"`
		Certificates *certificates `json:"certificates,omitempty"`
	}
```

And the function signature:

```go
// Build renders the full Caddy admin-API JSON config for the given State and
// the certificates to load directly. Routes are sorted by domain for
// deterministic output (important for golden-file tests and predictable Caddy
// /load behaviour). Passing a nil certs slice reproduces the internal-issuer
// only config.
func Build(s *state.State, certs []LoadedCert) []byte {
```

- [ ] **Step 4: Update the five call sites to pass nil**

- `internal/app/register.go:123`: `cfg := caddyapi.Build(s, nil)`
- `internal/app/deregister.go:112`: `cfg := caddyapi.Build(&updated, nil)`
- `internal/app/upgrade.go:105`: `if err := a.caddy.Load(ctx, caddyapi.Build(s, nil)); err != nil {`
- `internal/app/doctor_checks.go:596`: `desired := caddyapi.Build(st, nil)`
- `internal/app/doctor_checks.go:635`: `cfg := caddyapi.Build(st, nil)`

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/caddyapi/ -v && go test ./...`
Expected: PASS, including every pre-existing golden test in `config_test.go`. If a golden test fails, `Build(s, nil)` is not reproducing the old output; fix `Build` rather than the golden file.

- [ ] **Step 6: Commit**

```bash
git add internal/caddyapi/ internal/app/register.go internal/app/deregister.go internal/app/upgrade.go internal/app/doctor_checks.go
git commit -S -m "Taught config builder to load external certificates"
```

---

### Task 4: Wire certificates into every config push

**Files:**
- Create: `internal/app/certload.go`
- Create: `internal/app/certload_test.go`
- Modify: `internal/app/app.go`, `internal/app/register.go`, `internal/app/deregister.go`, `internal/app/upgrade.go`, `internal/app/doctor_checks.go`

**Interfaces:**
- Consumes: `certstore.Store` (Task 2), `caddyapi.LoadedCert` (Task 3), `state.Cert` (Task 1).
- Produces: `(*App) loadedCerts(s *state.State) []caddyapi.LoadedCert`; `App` gains an unexported `certs *certstore.Store` field.

This is the task that stops a `register` or a doctor drift fix from wiping a loaded certificate out of the running Caddy.

- [ ] **Step 1: Write the failing test**

Create `internal/app/certload_test.go`:

```go
package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bawdo/caddy_shack/internal/state"
)

func TestLoadedCertsReturnsMaterialForEachStateCert(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath:    filepath.Join(dir, "state.json"),
		LockPath:     filepath.Join(dir, "state.json.lock"),
		CertStoreDir: dir,
	}, nil, nil)

	if err := a.certs.Save("alpha.arrix.dev", []byte("CERTPEM"), []byte("KEYPEM")); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends:      map[string]state.Backend{},
		Certs: map[string]state.Cert{
			"alpha.arrix.dev": {
				Domain:   "alpha.arrix.dev",
				Subjects: []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
				Env:      "production",
			},
		},
	}

	got := a.loadedCerts(s)
	if len(got) != 1 {
		t.Fatalf("expected one loaded cert, got %d", len(got))
	}
	if got[0].CertPEM != "CERTPEM" || got[0].KeyPEM != "KEYPEM" {
		t.Errorf("material mismatch: %+v", got[0])
	}
	if len(got[0].Subjects) != 2 {
		t.Errorf("subjects = %v, want both the apex and the wildcard", got[0].Subjects)
	}
}

func TestLoadedCertsSkipsStateEntriesWithNoMaterialOnDisk(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath:    filepath.Join(dir, "state.json"),
		LockPath:     filepath.Join(dir, "state.json.lock"),
		CertStoreDir: dir,
	}, nil, nil)

	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends:      map[string]state.Backend{},
		Certs: map[string]state.Cert{
			"gone.example.com": {Domain: "gone.example.com", Subjects: []string{"gone.example.com"}},
		},
	}

	// A state entry whose files were deleted by hand must degrade to "no
	// certificate", not block every config push from then on.
	if got := a.loadedCerts(s); len(got) != 0 {
		t.Errorf("expected no loaded certs, got %d", len(got))
	}
}

func TestLoadedCertsIsDeterministicallyOrdered(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath:    filepath.Join(dir, "state.json"),
		LockPath:     filepath.Join(dir, "state.json.lock"),
		CertStoreDir: dir,
	}, nil, nil)

	for _, d := range []string{"zeta.example.com", "alpha.example.com", "mid.example.com"} {
		if err := a.certs.Save(d, []byte("c-"+d), []byte("k-"+d)); err != nil {
			t.Fatalf("seed %s: %v", d, err)
		}
	}
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends:      map[string]state.Backend{},
		Certs: map[string]state.Cert{
			"zeta.example.com":  {Domain: "zeta.example.com", Subjects: []string{"zeta.example.com"}},
			"alpha.example.com": {Domain: "alpha.example.com", Subjects: []string{"alpha.example.com"}},
			"mid.example.com":   {Domain: "mid.example.com", Subjects: []string{"mid.example.com"}},
		},
	}

	// Map iteration order is random; unstable output would make the doctor
	// drift check flap and golden tests non-deterministic.
	first := a.loadedCerts(s)
	for i := 0; i < 20; i++ {
		if got := a.loadedCerts(s); got[0].CertPEM != first[0].CertPEM || got[2].CertPEM != first[2].CertPEM {
			t.Fatal("loadedCerts must be deterministically ordered across calls")
		}
	}
	if first[0].Subjects[0] != "alpha.example.com" {
		t.Errorf("expected domain-sorted order, first = %v", first[0].Subjects)
	}
	_ = os.Getpid()
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/app/ -run LoadedCerts -v`
Expected: FAIL to compile, `a.certs` and `a.loadedCerts` are undefined.

- [ ] **Step 3: Add the store to App**

In `internal/app/app.go`, add the import for `certstore`, the field, and construct it:

```go
// App is the orchestrator that backs every CLI command.
type App struct {
	paths  Paths
	caddy  *caddyapi.Client
	docker docker.Client
	certs  *certstore.Store
}
```

In `NewWithPaths`, before the return:

```go
	return &App{
		paths:  p,
		caddy:  caddy,
		docker: dock,
		certs:  certstore.New(p.CertStoreDir),
	}
```

- [ ] **Step 4: Implement loadedCerts**

Create `internal/app/certload.go`:

```go
package app

import (
	"sort"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/state"
)

// loadedCerts turns the certificate metadata in state into the material
// caddyapi.Build needs, reading the PEM from the on-host store.
//
// Every caddyapi.Build call site must pass the result of this. A config
// pushed without it would drop the loaded certificates out of the running
// Caddy, and the next request for a covered domain would fall back to the
// internal CA or fail outright.
//
// A state entry whose material has gone missing from disk is skipped rather
// than treated as an error: a hand-deleted file should degrade to "no
// certificate for that domain", not wedge every subsequent register.
func (a *App) loadedCerts(s *state.State) []caddyapi.LoadedCert {
	if s == nil || len(s.Certs) == 0 {
		return nil
	}
	domains := make([]string, 0, len(s.Certs))
	for d := range s.Certs {
		domains = append(domains, d)
	}
	// Sorted so the generated config is byte-stable across calls; the doctor
	// drift check compares generated output.
	sort.Strings(domains)

	out := make([]caddyapi.LoadedCert, 0, len(domains))
	for _, d := range domains {
		m, err := a.certs.Load(d)
		if err != nil {
			continue
		}
		c := s.Certs[d]
		subjects := c.Subjects
		if len(subjects) == 0 {
			subjects = []string{d, "*." + d}
		}
		out = append(out, caddyapi.LoadedCert{
			Subjects: subjects,
			CertPEM:  string(m.CertPEM),
			KeyPEM:   string(m.KeyPEM),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
```

- [ ] **Step 5: Run the new tests**

Run: `go test ./internal/app/ -run LoadedCerts -v`
Expected: PASS, all three tests.

- [ ] **Step 6: Replace every nil at the call sites**

- `internal/app/register.go:123`: `cfg := caddyapi.Build(s, a.loadedCerts(s))`
- `internal/app/deregister.go:112`: `cfg := caddyapi.Build(&updated, a.loadedCerts(&updated))`
- `internal/app/upgrade.go:105`: `if err := a.caddy.Load(ctx, caddyapi.Build(s, a.loadedCerts(s))); err != nil {`
- `internal/app/doctor_checks.go:596`: `desired := caddyapi.Build(st, a.loadedCerts(st))`
- `internal/app/doctor_checks.go:635`: `cfg := caddyapi.Build(st, a.loadedCerts(st))`

- [ ] **Step 7: Verify no call site was missed**

Run: `grep -rn "caddyapi.Build" --include="*.go" . | grep -v "_test.go"`
Expected: five call sites, every one passing `a.loadedCerts(...)`. Any remaining `nil` is a bug that would wipe certificates from the running Caddy.

- [ ] **Step 8: Run the whole suite**

Run: `go test ./... && make lint`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/app/certload.go internal/app/certload_test.go internal/app/app.go internal/app/register.go internal/app/deregister.go internal/app/upgrade.go internal/app/doctor_checks.go
git commit -S -m "Wired stored certificates into every config push"
```

---

### Task 5: Manual DNS-01 challenge provider

**Files:**
- Create: `internal/acme/acme.go`
- Create: `internal/acme/manual.go`
- Create: `internal/acme/manual_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `acme.Record{FQDN string, Value string}`
  - `acme.Prompter` interface with `Confirm(records []Record) error`
  - `acme.newManualProvider(p Prompter) *manualProvider` (unexported, used by Task 6)
  - `(*manualProvider) Present(domain, token, keyAuth string) error`
  - `(*manualProvider) CleanUp(domain, token, keyAuth string) error`
  - `(*manualProvider) Records() []Record`
  - `(*manualProvider) preCheck(domain, fqdn, value string, check dns01.PreCheckFunc) (bool, error)`

Deliberately **not** implementing `Sequential()`: lego's `challenge/resolver/prober.go` routes providers that implement it into `sequentialSolve`, which would prompt once per identifier. Without it, both `Present` calls land before any validation and the operator makes one trip to the DNS provider.

- [ ] **Step 1: Add the dependency**

Run:

```bash
go get github.com/go-acme/lego/v4@v4.35.2
```

- [ ] **Step 2: Write the failing test**

Create `internal/acme/manual_test.go`:

```go
package acme

import (
	"errors"
	"strings"
	"testing"
)

// recordingPrompter captures what the provider asked the operator to do.
type recordingPrompter struct {
	calls   int
	records []Record
	err     error
}

func (p *recordingPrompter) Confirm(records []Record) error {
	p.calls++
	p.records = append([]Record{}, records...)
	return p.err
}

func TestProviderCollectsOneRecordPerIdentifier(t *testing.T) {
	p := &recordingPrompter{}
	prov := newManualProvider(p)

	if err := prov.Present("alpha.arrix.dev", "tok1", "keyauth-one"); err != nil {
		t.Fatalf("present apex: %v", err)
	}
	if err := prov.Present("*.alpha.arrix.dev", "tok2", "keyauth-two"); err != nil {
		t.Fatalf("present wildcard: %v", err)
	}

	recs := prov.Records()
	if len(recs) != 2 {
		t.Fatalf("expected two records, got %d: %+v", len(recs), recs)
	}
	// A wildcard order puts both challenges on the same name. If these ever
	// differ, the operator would be told to create the wrong record.
	if recs[0].FQDN != recs[1].FQDN {
		t.Errorf("both identifiers must share one _acme-challenge name, got %q and %q", recs[0].FQDN, recs[1].FQDN)
	}
	if !strings.HasPrefix(recs[0].FQDN, "_acme-challenge.alpha.arrix.dev") {
		t.Errorf("FQDN = %q, want the _acme-challenge name for the apex", recs[0].FQDN)
	}
	if recs[0].Value == recs[1].Value {
		t.Error("the two identifiers must yield different TXT values")
	}
	if recs[0].Value == "" {
		t.Error("TXT value must not be empty")
	}
}

func TestProviderDoesNotPromptDuringPresent(t *testing.T) {
	p := &recordingPrompter{}
	prov := newManualProvider(p)

	if err := prov.Present("alpha.arrix.dev", "tok1", "keyauth-one"); err != nil {
		t.Fatalf("present: %v", err)
	}
	// Prompting inside Present would ask the operator once per identifier.
	// The prompt belongs in the pre-check, after every Present has run.
	if p.calls != 0 {
		t.Errorf("Present must not prompt, prompter called %d times", p.calls)
	}
}

func TestPreCheckPromptsExactlyOnceWithEveryRecord(t *testing.T) {
	p := &recordingPrompter{}
	prov := newManualProvider(p)

	if err := prov.Present("alpha.arrix.dev", "tok1", "keyauth-one"); err != nil {
		t.Fatalf("present apex: %v", err)
	}
	if err := prov.Present("*.alpha.arrix.dev", "tok2", "keyauth-two"); err != nil {
		t.Fatalf("present wildcard: %v", err)
	}

	var checked int
	check := func(fqdn, value string) (bool, error) {
		checked++
		return true, nil
	}

	for i := 0; i < 3; i++ {
		ok, err := prov.preCheck("alpha.arrix.dev", "_acme-challenge.alpha.arrix.dev.", "v", check)
		if err != nil {
			t.Fatalf("precheck: %v", err)
		}
		if !ok {
			t.Fatal("precheck must delegate its verdict to the wrapped check")
		}
	}

	if p.calls != 1 {
		t.Errorf("operator must be prompted exactly once, got %d prompts", p.calls)
	}
	if len(p.records) != 2 {
		t.Errorf("the single prompt must show both records, showed %d", len(p.records))
	}
	if checked != 3 {
		t.Errorf("every precheck must still reach the real propagation check, got %d", checked)
	}
}

func TestPreCheckPropagatesPrompterError(t *testing.T) {
	sentinel := errors.New("operator cancelled")
	p := &recordingPrompter{err: sentinel}
	prov := newManualProvider(p)

	if err := prov.Present("alpha.arrix.dev", "tok1", "keyauth-one"); err != nil {
		t.Fatalf("present: %v", err)
	}

	check := func(fqdn, value string) (bool, error) { return true, nil }
	_, err := prov.preCheck("alpha.arrix.dev", "_acme-challenge.alpha.arrix.dev.", "v", check)
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the prompter's error", err)
	}
}

func TestCleanUpIsQuietAndSucceeds(t *testing.T) {
	p := &recordingPrompter{}
	prov := newManualProvider(p)
	if err := prov.CleanUp("alpha.arrix.dev", "tok1", "keyauth-one"); err != nil {
		t.Errorf("CleanUp must not fail: %v", err)
	}
	if p.calls != 0 {
		t.Error("CleanUp must not prompt")
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/acme/ -v`
Expected: FAIL to build, package does not exist.

- [ ] **Step 4: Write the shared types**

Create `internal/acme/acme.go`:

```go
// Package acme obtains certificates from an ACME server. It exists so the
// rest of caddy_shack can order a certificate without knowing about lego, and
// so the app layer can be tested against a fake issuer.
//
// Caddy's own ACME automation is all or nothing: it has no mode that pauses
// and asks a human for a DNS record. Since alpha.arrix.dev and domains like it
// resolve to 127.0.0.1, no inbound challenge can reach them and DNS-01 is the
// only option, so caddy_shack drives the order itself and hands Caddy the
// finished certificate.
package acme

import (
	"context"
	"time"
)

// Environment names an ACME directory.
const (
	EnvStaging    = "staging"
	EnvProduction = "production"
)

// Record is one DNS TXT record the operator must publish to satisfy a
// DNS-01 challenge.
type Record struct {
	// FQDN is the fully qualified record name, e.g.
	// "_acme-challenge.alpha.arrix.dev." with its trailing dot.
	FQDN string
	// Value is the TXT record's value.
	Value string
}

// Prompter shows the operator the records to publish and blocks until they
// confirm. Implementations live in the cmd layer so they can write to the
// command's own streams.
type Prompter interface {
	Confirm(records []Record) error
}

// Request is one certificate order.
type Request struct {
	// Domains are the identifiers to include, apex first.
	Domains []string
	// Email is the ACME account contact.
	Email string
	// Env is EnvStaging or EnvProduction.
	Env string
	// AccountKeyPath is where the account key is read from, or written to on
	// first use for this environment.
	AccountKeyPath string
}

// Result is an issued certificate.
type Result struct {
	// CertPEM is the full chain, PEM encoded.
	CertPEM []byte
	// KeyPEM is the private key, PEM encoded.
	KeyPEM []byte
	// NotAfter is the leaf certificate's expiry.
	NotAfter time.Time
}

// Issuer obtains certificates. The real implementation talks to Let's
// Encrypt; tests use the fake.
type Issuer interface {
	Issue(ctx context.Context, req Request, prompt Prompter) (Result, error)
}
```

- [ ] **Step 5: Write the provider**

Create `internal/acme/manual.go`:

```go
package acme

import (
	"sync"

	"github.com/go-acme/lego/v4/challenge/dns01"
)

// manualProvider implements lego's challenge.Provider for an operator who
// edits DNS records by hand.
//
// It deliberately does NOT implement Sequential(). lego's prober splits
// solvers into parallelSolve (PreSolve every authorisation, then Solve every
// authorisation) and sequentialSolve, and a provider only lands in the
// sequential bucket if it implements Sequential(). lego's own manual provider
// does, which makes a wildcard order prompt twice, once per identifier, with a
// validation in between. Staying out of that bucket means both Present calls
// happen before any validation, so every record can be shown in one prompt and
// the operator makes a single trip to their DNS provider.
type manualProvider struct {
	prompt Prompter

	mu      sync.Mutex
	records []Record

	once     sync.Once
	promptErr error
}

func newManualProvider(p Prompter) *manualProvider {
	return &manualProvider{prompt: p}
}

// Present records what needs publishing. It does not prompt: prompting here
// would ask the operator once per identifier. The prompt happens in preCheck,
// by which time every Present has run.
func (m *manualProvider) Present(domain, token, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, Record{FQDN: info.EffectiveFQDN, Value: info.Value})
	return nil
}

// CleanUp is a no-op. The operator removes the records themselves, and the
// command tells them so once the order is complete. Failing here would fail an
// otherwise successful issuance.
func (m *manualProvider) CleanUp(domain, token, keyAuth string) error {
	return nil
}

// Records returns the collected records in the order they were presented.
func (m *manualProvider) Records() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Record{}, m.records...)
}

// preCheck is installed via dns01.WrapPreCheck. It prompts the operator once,
// on the first propagation check, then delegates to lego's real check. Because
// the real check still runs, confirming before the records have actually
// propagated is harmless: lego keeps polling the authoritative nameservers
// until the values resolve.
func (m *manualProvider) preCheck(domain, fqdn, value string, check dns01.PreCheckFunc) (bool, error) {
	m.once.Do(func() {
		m.promptErr = m.prompt.Confirm(m.Records())
	})
	if m.promptErr != nil {
		return false, m.promptErr
	}
	return check(fqdn, value)
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/acme/ -v`
Expected: PASS, all five tests.

- [ ] **Step 7: Verify the provider stays out of the sequential bucket**

Run:

```bash
cat > /tmp/seqcheck_test.go <<'EOF'
package acme

import "testing"

// A Sequential method would route this provider into lego's sequentialSolve,
// prompting once per identifier. Guard against it being added by accident.
func TestProviderDoesNotImplementSequential(t *testing.T) {
	var p any = newManualProvider(nil)
	if _, bad := p.(interface{ Sequential() (bool, any) }); bad {
		t.Error("manualProvider must not implement Sequential")
	}
}
EOF
cp /tmp/seqcheck_test.go internal/acme/seqcheck_test.go && rm /tmp/seqcheck_test.go
go test ./internal/acme/ -run Sequential -v
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/acme/ go.mod go.sum
git commit -S -m "Added manual DNS-01 challenge provider"
```

---

### Task 6: Real issuer backed by lego

**Files:**
- Create: `internal/acme/lego.go`
- Create: `internal/acme/fake.go`
- Test: `internal/acme/lego_test.go`

**Interfaces:**
- Consumes: `Request`, `Result`, `Prompter`, `manualProvider` (Task 5).
- Produces:
  - `acme.NewLegoIssuer() *LegoIssuer` satisfying `Issuer`
  - `acme.DirectoryURL(env string) string`
  - `acme.NewFakeIssuer(result Result, err error) *FakeIssuer` with fields `Calls []Request` and `PromptCalls int`

- [ ] **Step 1: Write the failing test**

Create `internal/acme/lego_test.go`:

```go
package acme

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryURLSelectsByEnvironment(t *testing.T) {
	staging := DirectoryURL(EnvStaging)
	prod := DirectoryURL(EnvProduction)

	if !strings.Contains(staging, "staging") {
		t.Errorf("staging URL = %q, want the staging directory", staging)
	}
	if strings.Contains(prod, "staging") {
		t.Errorf("production URL = %q, must not be the staging directory", prod)
	}
	if staging == prod {
		t.Error("staging and production must be different directories")
	}
}

func TestDirectoryURLDefaultsToStagingForUnknownEnv(t *testing.T) {
	// Defaulting to production on a typo would burn a rate-limited issuance.
	if got := DirectoryURL("typo"); got != DirectoryURL(EnvStaging) {
		t.Errorf("unknown env = %q, want the staging directory", got)
	}
}

func TestAccountKeyIsCreatedOnFirstUseAndReusedAfter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acme", "account-staging.key")

	first, err := loadOrCreateAccountKey(path)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("account key mode = %#o, want 0600", perm)
	}

	second, err := loadOrCreateAccountKey(path)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	// A regenerated account key would orphan the ACME account and lose the
	// rate-limit and registration history attached to it.
	if !first.(*ecdsa.PrivateKey).Equal(second.(*ecdsa.PrivateKey)) {
		t.Error("account key must be reused, not regenerated")
	}
}

func TestLoadOrCreateAccountKeyRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "account-staging.key")
	if err := os.WriteFile(path, []byte("not a pem key"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	if _, err := loadOrCreateAccountKey(path); err == nil {
		t.Error("a corrupt account key must be reported, not silently replaced")
	}
}

func TestNotAfterIsReadFromTheIssuedLeaf(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	certPEM, want := selfSignedForTest(t, key)

	got, err := leafNotAfter(certPEM)
	if err != nil {
		t.Fatalf("leafNotAfter: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("NotAfter = %v, want %v", got, want)
	}
}

func TestLeafNotAfterRejectsNonPEM(t *testing.T) {
	if _, err := leafNotAfter([]byte("nonsense")); err == nil {
		t.Error("non-PEM input must error")
	}
}

func TestFakeIssuerRecordsRequests(t *testing.T) {
	want := Result{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")}
	f := NewFakeIssuer(want, nil)

	got, err := f.Issue(t.Context(), Request{Domains: []string{"a.example.com"}, Env: EnvStaging}, nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if string(got.CertPEM) != "CERT" {
		t.Errorf("CertPEM = %q, want CERT", got.CertPEM)
	}
	if len(f.Calls) != 1 || f.Calls[0].Env != EnvStaging {
		t.Errorf("fake must record the request, got %+v", f.Calls)
	}
}
```

Add the self-signed helper at the bottom of the same file:

```go
// selfSignedForTest builds a throwaway leaf so leafNotAfter can be tested
// without touching the network.
func selfSignedForTest(t *testing.T, key *ecdsa.PrivateKey) ([]byte, time.Time) {
	t.Helper()
	notAfter := time.Now().Add(90 * 24 * time.Hour).UTC().Truncate(time.Second)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "alpha.arrix.dev"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), notAfter
}
```

Add these imports to the test file: `crypto/x509`, `crypto/x509/pkix`, `encoding/pem`, `math/big`, `time`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/acme/ -run 'DirectoryURL|AccountKey|NotAfter|FakeIssuer' -v`
Expected: FAIL to compile, the helpers do not exist.

- [ ] **Step 3: Implement the issuer**

Create `internal/acme/lego.go`:

```go
package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

// DirectoryURL returns the ACME directory for an environment. Anything other
// than EnvProduction resolves to staging: defaulting to production on an
// unrecognised value would spend one of the five weekly issuances Let's
// Encrypt allows for a given name set.
func DirectoryURL(env string) string {
	if env == EnvProduction {
		return lego.LEDirectoryProduction
	}
	return lego.LEDirectoryStaging
}

// legoUser adapts an account key and email to lego's registration.User.
type legoUser struct {
	email string
	reg   *registration.Resource
	key   crypto.PrivateKey
}

func (u *legoUser) GetEmail() string                        { return u.email }
func (u *legoUser) GetRegistration() *registration.Resource { return u.reg }
func (u *legoUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// LegoIssuer obtains certificates from a real ACME server.
type LegoIssuer struct {
	// DirectoryOverride, when set, replaces the environment-derived
	// directory URL. Integration tests point this at Pebble.
	DirectoryOverride string
}

// NewLegoIssuer returns an Issuer backed by Let's Encrypt.
func NewLegoIssuer() *LegoIssuer { return &LegoIssuer{} }

// Issue runs a full ACME order using manual DNS-01, prompting once through
// prompt for every record across every identifier.
func (l *LegoIssuer) Issue(ctx context.Context, req Request, prompt Prompter) (Result, error) {
	if len(req.Domains) == 0 {
		return Result{}, fmt.Errorf("acme: no domains in request")
	}
	if prompt == nil {
		return Result{}, fmt.Errorf("acme: no prompter supplied")
	}

	key, err := loadOrCreateAccountKey(req.AccountKeyPath)
	if err != nil {
		return Result{}, err
	}

	user := &legoUser{email: req.Email, key: key}
	cfg := lego.NewConfig(user)
	cfg.CADirURL = l.DirectoryOverride
	if cfg.CADirURL == "" {
		cfg.CADirURL = DirectoryURL(req.Env)
	}
	cfg.Certificate.KeyType = certcrypto.EC256

	client, err := lego.NewClient(cfg)
	if err != nil {
		return Result{}, fmt.Errorf("acme: new client: %w", err)
	}

	provider := newManualProvider(prompt)
	if err := client.Challenge.SetDNS01Provider(provider, dns01.WrapPreCheck(provider.preCheck)); err != nil {
		return Result{}, fmt.Errorf("acme: set dns-01 provider: %w", err)
	}

	// Registration is idempotent for an existing account key: lego returns
	// the existing account rather than creating a second one.
	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return Result{}, fmt.Errorf("acme: register account: %w", err)
	}
	user.reg = reg

	res, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: req.Domains,
		Bundle:  true,
	})
	if err != nil {
		return Result{}, fmt.Errorf("acme: obtain certificate: %w", err)
	}

	notAfter, err := leafNotAfter(res.Certificate)
	if err != nil {
		return Result{}, err
	}

	return Result{
		CertPEM:  res.Certificate,
		KeyPEM:   res.PrivateKey,
		NotAfter: notAfter,
	}, nil
}

// loadOrCreateAccountKey reads the ACME account key, generating one on first
// use. The key is reused across orders: regenerating it would orphan the
// account and lose its registration and rate-limit history.
func loadOrCreateAccountKey(path string) (crypto.PrivateKey, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path comes from Paths, rooted at os.UserConfigDir
	switch {
	case err == nil:
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("acme: account key at %s is not valid PEM", path)
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("acme: parse account key at %s: %w", path, err)
		}
		return key, nil
	case os.IsNotExist(err):
	default:
		return nil, fmt.Errorf("acme: read account key: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("acme: generate account key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("acme: marshal account key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("acme: mkdir account dir: %w", err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return nil, fmt.Errorf("acme: write account key: %w", err)
	}
	return key, nil
}

// leafNotAfter reads the expiry from the first certificate in a PEM chain.
func leafNotAfter(chainPEM []byte) (time.Time, error) {
	block, _ := pem.Decode(chainPEM)
	if block == nil {
		return time.Time{}, fmt.Errorf("acme: certificate chain is not valid PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("acme: parse leaf certificate: %w", err)
	}
	return leaf.NotAfter, nil
}
```

- [ ] **Step 4: Write the fake**

Create `internal/acme/fake.go`:

```go
package acme

import "context"

// FakeIssuer is a test double for Issuer. It records every request and
// returns a canned result, so the app layer can be tested without a network.
type FakeIssuer struct {
	// Calls records every Issue request in order.
	Calls []Request
	// PromptCalls counts how many times the supplied prompter was invoked.
	PromptCalls int

	result Result
	err    error
}

// NewFakeIssuer returns a FakeIssuer that answers every Issue with result and
// err.
func NewFakeIssuer(result Result, err error) *FakeIssuer {
	return &FakeIssuer{result: result, err: err}
}

// Issue records the request and returns the canned response. When a prompter
// is supplied it is invoked once with a representative record, so tests can
// assert the operator would have been asked.
func (f *FakeIssuer) Issue(ctx context.Context, req Request, prompt Prompter) (Result, error) {
	f.Calls = append(f.Calls, req)
	if prompt != nil {
		f.PromptCalls++
		if err := prompt.Confirm([]Record{{
			FQDN:  "_acme-challenge." + req.Domains[0] + ".",
			Value: "fake-challenge-value",
		}}); err != nil {
			return Result{}, err
		}
	}
	if f.err != nil {
		return Result{}, f.err
	}
	return f.result, nil
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/acme/ -v`
Expected: PASS, every test in the package.

- [ ] **Step 6: Lint**

Run: `make lint`
Expected: PASS. `gosec` may flag G304 on the account key read; the `#nosec` comment above it is already scoped to that line.

- [ ] **Step 7: Commit**

```bash
git add internal/acme/
git commit -S -m "Added lego-backed ACME issuer and test fake"
```

---

### Task 7: App-level cert request, renew, list and remove

**Files:**
- Create: `internal/app/cert.go`
- Create: `internal/app/cert_test.go`
- Modify: `internal/app/errors.go`, `internal/app/app.go`

**Interfaces:**
- Consumes: `acme.Issuer`, `acme.Request`, `acme.Result`, `acme.Prompter` (Tasks 5 and 6); `certstore.Store` (Task 2); `state.Cert` (Task 1); `(*App) loadedCerts` (Task 4).
- Produces:
  - `app.ErrCertIssuance` mapping to exit code 9
  - `app.CertRequestInput{Domain string, Email string, Production bool}`
  - `(*App) CertRequest(ctx context.Context, in CertRequestInput, prompt acme.Prompter) error`
  - `(*App) CertList(ctx context.Context) ([]CertListing, error)`
  - `app.CertListing{Domain, Env string, Subjects []string, NotAfter string, DaysRemaining int}`
  - `(*App) CertRemove(ctx context.Context, domain string) (bool, error)`
  - `(*App) CertRenew(ctx context.Context, domain string, force bool, prompt acme.Prompter) (bool, error)`
  - `App` gains an `issuer acme.Issuer` field; `NewWithPaths` gains no parameter, but `(*App) SetIssuer(acme.Issuer)` allows tests to substitute one.

- [ ] **Step 1: Add the sentinel and exit code**

In `internal/app/errors.go`, add to the var block:

```go
	// ErrCertIssuance covers any failure of an ACME order: the challenge, the
	// DNS validation, or a rate limit. Distinguished from a generic failure
	// because a script may want to back off rather than retry.
	ErrCertIssuance = errors.New("certificate issuance failed")
```

And add the case to `ExitCode`, before `default`:

```go
	case errors.Is(err, ErrCertIssuance):
		return 9
```

- [ ] **Step 2: Write the failing test**

Create `internal/app/cert_test.go`:

```go
package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/acme"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// certTestApp builds an App wired to a fake docker, a fake issuer and a
// temporary state and cert directory.
func certTestApp(t *testing.T, containerState docker.State) (*App, *acme.FakeIssuer) {
	t.Helper()
	dir := t.TempDir()
	fd := docker.NewFake()
	fd.SetContainerState(CaddyContainerName, containerState)

	a := NewWithPaths(Paths{
		StatePath:    filepath.Join(dir, "state.json"),
		LockPath:     filepath.Join(dir, "state.json.lock"),
		CertStoreDir: dir,
	}, nil, fd)

	iss := acme.NewFakeIssuer(acme.Result{
		CertPEM:  []byte("-----BEGIN CERTIFICATE-----\nCERT\n-----END CERTIFICATE-----\n"),
		KeyPEM:   []byte("-----BEGIN EC PRIVATE KEY-----\nKEY\n-----END EC PRIVATE KEY-----\n"),
		NotAfter: time.Now().Add(90 * 24 * time.Hour),
	}, nil)
	a.SetIssuer(iss)
	return a, iss
}

// noopPrompter accepts whatever it is shown.
type noopPrompter struct{ calls int }

func (p *noopPrompter) Confirm(records []acme.Record) error { p.calls++; return nil }

func TestCertRequestRejectsTestDomain(t *testing.T) {
	a, iss := certTestApp(t, docker.StateRunning)
	err := a.CertRequest(t.Context(), CertRequestInput{Domain: "hello.test", Email: "k@example.com"}, &noopPrompter{})
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs", err)
	}
	if len(iss.Calls) != 0 {
		t.Error("a .test domain belongs to the internal issuer; no ACME order may be attempted")
	}
}

func TestCertRequestRejectsEmptyEmail(t *testing.T) {
	a, iss := certTestApp(t, docker.StateRunning)
	err := a.CertRequest(t.Context(), CertRequestInput{Domain: "alpha.arrix.dev"}, &noopPrompter{})
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs", err)
	}
	if len(iss.Calls) != 0 {
		t.Error("no ACME order may be attempted without an account email")
	}
}

func TestCertRequestChecksContainerBeforeOrdering(t *testing.T) {
	a, iss := certTestApp(t, docker.StateStopped)
	err := a.CertRequest(t.Context(), CertRequestInput{Domain: "alpha.arrix.dev", Email: "k@example.com"}, &noopPrompter{})
	if !errors.Is(err, ErrCaddyNotRunning) {
		t.Errorf("err = %v, want ErrCaddyNotRunning", err)
	}
	// Issuance is rate limited. Discovering the container is down after
	// spending one would be a wasted week.
	if len(iss.Calls) != 0 {
		t.Error("the container check must precede the ACME order")
	}
}

func TestCertRequestDefaultsToStaging(t *testing.T) {
	a, iss := certTestApp(t, docker.StateRunning)
	if err := a.CertRequest(t.Context(), CertRequestInput{Domain: "alpha.arrix.dev", Email: "k@example.com"}, &noopPrompter{}); err != nil {
		t.Fatalf("request: %v", err)
	}
	if len(iss.Calls) != 1 {
		t.Fatalf("expected one order, got %d", len(iss.Calls))
	}
	if iss.Calls[0].Env != acme.EnvStaging {
		t.Errorf("env = %q, want staging by default", iss.Calls[0].Env)
	}
}

func TestCertRequestProductionFlagSelectsProduction(t *testing.T) {
	a, iss := certTestApp(t, docker.StateRunning)
	if err := a.CertRequest(t.Context(), CertRequestInput{
		Domain: "alpha.arrix.dev", Email: "k@example.com", Production: true,
	}, &noopPrompter{}); err != nil {
		t.Fatalf("request: %v", err)
	}
	if iss.Calls[0].Env != acme.EnvProduction {
		t.Errorf("env = %q, want production", iss.Calls[0].Env)
	}
}

func TestCertRequestOrdersApexAndWildcard(t *testing.T) {
	a, iss := certTestApp(t, docker.StateRunning)
	if err := a.CertRequest(t.Context(), CertRequestInput{Domain: "alpha.arrix.dev", Email: "k@example.com"}, &noopPrompter{}); err != nil {
		t.Fatalf("request: %v", err)
	}
	got := iss.Calls[0].Domains
	if len(got) != 2 || got[0] != "alpha.arrix.dev" || got[1] != "*.alpha.arrix.dev" {
		t.Errorf("domains = %v, want [alpha.arrix.dev *.alpha.arrix.dev]", got)
	}
}

func TestCertRequestPersistsMaterialAndState(t *testing.T) {
	a, _ := certTestApp(t, docker.StateRunning)
	if err := a.CertRequest(t.Context(), CertRequestInput{Domain: "alpha.arrix.dev", Email: "k@example.com"}, &noopPrompter{}); err != nil {
		t.Fatalf("request: %v", err)
	}

	if !a.certs.Has("alpha.arrix.dev") {
		t.Error("certificate material must be written to the store")
	}
	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	c, ok := s.Certs["alpha.arrix.dev"]
	if !ok {
		t.Fatal("state must record the certificate")
	}
	if c.Env != acme.EnvStaging {
		t.Errorf("env = %q, want staging", c.Env)
	}
	if len(c.Subjects) != 2 {
		t.Errorf("subjects = %v, want apex and wildcard", c.Subjects)
	}
	if c.NotAfter == "" {
		t.Error("state must record the expiry")
	}
	// Renewal reads the contact back rather than asking again.
	if c.Email != "k@example.com" {
		t.Errorf("email = %q, want the contact the order was placed with", c.Email)
	}
}

func TestCertRequestWrapsIssuerFailureAsCertIssuance(t *testing.T) {
	a, _ := certTestApp(t, docker.StateRunning)
	a.SetIssuer(acme.NewFakeIssuer(acme.Result{}, errors.New("rate limit exceeded")))

	err := a.CertRequest(t.Context(), CertRequestInput{Domain: "alpha.arrix.dev", Email: "k@example.com"}, &noopPrompter{})
	if !errors.Is(err, ErrCertIssuance) {
		t.Errorf("err = %v, want ErrCertIssuance (exit 9)", err)
	}
	if ExitCode(err) != 9 {
		t.Errorf("exit code = %d, want 9", ExitCode(err))
	}
	// A failed order must not leave half-written material behind.
	if a.certs.Has("alpha.arrix.dev") {
		t.Error("a failed order must not write certificate material")
	}
}

func TestCertListReportsDaysRemaining(t *testing.T) {
	a, _ := certTestApp(t, docker.StateRunning)
	if err := a.CertRequest(t.Context(), CertRequestInput{Domain: "alpha.arrix.dev", Email: "k@example.com"}, &noopPrompter{}); err != nil {
		t.Fatalf("request: %v", err)
	}

	rows, err := a.CertList(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %d", len(rows))
	}
	if rows[0].Domain != "alpha.arrix.dev" {
		t.Errorf("domain = %q", rows[0].Domain)
	}
	// The fake issues a 90-day certificate.
	if rows[0].DaysRemaining < 88 || rows[0].DaysRemaining > 90 {
		t.Errorf("days remaining = %d, want about 90", rows[0].DaysRemaining)
	}
}

func TestCertListIsEmptyOnAFreshInstall(t *testing.T) {
	a, _ := certTestApp(t, docker.StateRunning)
	rows, err := a.CertList(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected no rows, got %d", len(rows))
	}
}

func TestCertRemoveDeletesMaterialAndState(t *testing.T) {
	a, _ := certTestApp(t, docker.StateRunning)
	if err := a.CertRequest(t.Context(), CertRequestInput{Domain: "alpha.arrix.dev", Email: "k@example.com"}, &noopPrompter{}); err != nil {
		t.Fatalf("request: %v", err)
	}

	removed, err := a.CertRemove(t.Context(), "alpha.arrix.dev")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed {
		t.Error("removed = false, want true for an existing certificate")
	}
	if a.certs.Has("alpha.arrix.dev") {
		t.Error("material must be gone from the store")
	}
	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if _, present := s.Certs["alpha.arrix.dev"]; present {
		t.Error("state must no longer record the certificate")
	}
}

func TestCertRemoveOnUnknownDomainIsNotAnError(t *testing.T) {
	a, _ := certTestApp(t, docker.StateRunning)
	removed, err := a.CertRemove(t.Context(), "nope.example.com")
	if err != nil {
		t.Errorf("removing an unknown domain must not error, got %v", err)
	}
	if removed {
		t.Error("removed = true, want false for an unknown domain")
	}
}

func TestCertRenewSkipsWhenNotDue(t *testing.T) {
	a, iss := certTestApp(t, docker.StateRunning)
	if err := a.CertRequest(t.Context(), CertRequestInput{Domain: "alpha.arrix.dev", Email: "k@example.com"}, &noopPrompter{}); err != nil {
		t.Fatalf("request: %v", err)
	}
	before := len(iss.Calls)

	renewed, err := a.CertRenew(t.Context(), "alpha.arrix.dev", false, &noopPrompter{})
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if renewed {
		t.Error("a certificate with 90 days left must not renew")
	}
	if len(iss.Calls) != before {
		t.Error("a no-op renew must not place an ACME order")
	}
}

func TestCertRenewForceOrdersAnyway(t *testing.T) {
	a, iss := certTestApp(t, docker.StateRunning)
	if err := a.CertRequest(t.Context(), CertRequestInput{Domain: "alpha.arrix.dev", Email: "k@example.com"}, &noopPrompter{}); err != nil {
		t.Fatalf("request: %v", err)
	}
	before := len(iss.Calls)

	renewed, err := a.CertRenew(t.Context(), "alpha.arrix.dev", true, &noopPrompter{})
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !renewed {
		t.Error("--force must renew regardless of the remaining life")
	}
	if len(iss.Calls) != before+1 {
		t.Error("--force must place an ACME order")
	}
}

func TestCertRenewOnUnknownDomainIsInvalidArgs(t *testing.T) {
	a, _ := certTestApp(t, docker.StateRunning)
	_, err := a.CertRenew(t.Context(), "nope.example.com", false, &noopPrompter{})
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs", err)
	}
}

func TestCertRenewReusesTheOriginalEnvironment(t *testing.T) {
	a, iss := certTestApp(t, docker.StateRunning)
	if err := a.CertRequest(t.Context(), CertRequestInput{
		Domain: "alpha.arrix.dev", Email: "k@example.com", Production: true,
	}, &noopPrompter{}); err != nil {
		t.Fatalf("request: %v", err)
	}

	if _, err := a.CertRenew(t.Context(), "alpha.arrix.dev", true, &noopPrompter{}); err != nil {
		t.Fatalf("renew: %v", err)
	}
	last := iss.Calls[len(iss.Calls)-1]
	// Silently renewing a production certificate against staging would swap
	// a trusted certificate for an untrusted one.
	if last.Env != acme.EnvProduction {
		t.Errorf("renew env = %q, want the original production", last.Env)
	}
}

func TestCertRequestRejectsDomainWithLeadingWildcard(t *testing.T) {
	a, _ := certTestApp(t, docker.StateRunning)
	err := a.CertRequest(t.Context(), CertRequestInput{Domain: "*.alpha.arrix.dev", Email: "k@example.com"}, &noopPrompter{})
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs (the wildcard is implied, not typed)", err)
	}
	if !strings.Contains(err.Error(), "wildcard") {
		t.Errorf("the error should explain the wildcard is automatic, got %q", err)
	}
}
```

Check `internal/docker/fake.go` for the exact constructor and state-setting method names before running; if they differ from `docker.NewFake()` and `SetContainerState`, use the real ones and keep the test intent identical.

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/app/ -run Cert -v`
Expected: FAIL to compile, none of the cert methods exist.

- [ ] **Step 4: Implement the commands**

Create `internal/app/cert.go`:

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bawdo/caddy_shack/internal/acme"
	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// renewWindow is how much remaining life makes a certificate due for renewal.
// Let's Encrypt issues for 90 days and recommends renewing at 30.
const renewWindow = 30 * 24 * time.Hour

// certDomainRE accepts a public FQDN of two or more dot-separated labels.
var certDomainRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// CertRequestInput holds the fields the cert request command accepts.
type CertRequestInput struct {
	Domain string
	Email  string
	// Production selects the real Let's Encrypt directory. The default is
	// staging, because production allows only five duplicate certificates per
	// week for a given name set and this flow has a human in it.
	Production bool
}

// CertListing is one row in the output of CertList.
type CertListing struct {
	Domain        string   `json:"domain"`
	Env           string   `json:"env"`
	Subjects      []string `json:"subjects"`
	NotAfter      string   `json:"not_after"`
	DaysRemaining int      `json:"days_remaining"`
}

// SetIssuer substitutes the ACME issuer. Tests use it to avoid the network.
func (a *App) SetIssuer(iss acme.Issuer) { a.issuer = iss }

// validateCertDomain rejects anything the ACME path cannot or should not
// handle.
func validateCertDomain(domain string) error {
	if strings.HasPrefix(domain, "*.") {
		return fmt.Errorf("%w: pass the apex domain; the wildcard is requested automatically (got %q)", ErrInvalidArgs, domain)
	}
	if strings.HasSuffix(domain, ".test") {
		return fmt.Errorf("%w: .test domains are served by the internal CA, not Let's Encrypt (got %q)", ErrInvalidArgs, domain)
	}
	if !certDomainRE.MatchString(domain) {
		return fmt.Errorf("%w: not a valid public domain name (got %q)", ErrInvalidArgs, domain)
	}
	return nil
}

// CertRequest obtains a certificate covering the domain and its single-label
// subdomains, stores it, and pushes it into the running Caddy.
//
// Every cheap failure is checked before any ACME traffic: an issuance is rate
// limited, so spending one only to discover the container is down would cost a
// week.
func (a *App) CertRequest(ctx context.Context, in CertRequestInput, prompt acme.Prompter) error {
	if err := validateCertDomain(in.Domain); err != nil {
		return err
	}
	if strings.TrimSpace(in.Email) == "" {
		return fmt.Errorf("%w: --email is required; Let's Encrypt needs an account contact", ErrInvalidArgs)
	}

	st, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	if st == docker.StateAbsent {
		return fmt.Errorf("%w: run 'caddy_shack install' first", ErrNotInstalled)
	}
	if st != docker.StateRunning {
		return fmt.Errorf("%w (state=%s)", ErrCaddyNotRunning, st)
	}

	env := acme.EnvStaging
	if in.Production {
		env = acme.EnvProduction
	}
	return a.issueAndInstall(ctx, in.Domain, in.Email, env, prompt)
}

// issueAndInstall runs the order and, only on success, writes material, updates
// state and pushes the config. Shared by CertRequest and CertRenew.
func (a *App) issueAndInstall(ctx context.Context, domain, email, env string, prompt acme.Prompter) error {
	subjects := []string{domain, "*." + domain}

	res, err := a.issuer.Issue(ctx, acme.Request{
		Domains:        subjects,
		Email:          email,
		Env:            env,
		AccountKeyPath: a.certs.AccountKeyPath(env),
	}, prompt)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCertIssuance, err)
	}

	lock, err := state.AcquireLock(a.paths.LockPath, 5*time.Second)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return err
	}
	defer func() { _ = lock.Release() }()

	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return err
	}

	if err := a.certs.Save(domain, res.CertPEM, res.KeyPEM); err != nil {
		return err
	}

	s.Certs[domain] = state.Cert{
		Domain:   domain,
		Subjects: subjects,
		Env:      env,
		Email:    email,
		IssuedAt: time.Now().Format(time.RFC3339),
		NotAfter: res.NotAfter.Format(time.RFC3339),
	}

	cfg := caddyapi.Build(s, a.loadedCerts(s))
	if err := a.caddy.Load(ctx, cfg); err != nil {
		// Roll the material back so the store and the running Caddy do not
		// disagree about what is installed.
		_ = a.certs.Remove(domain)
		if errors.Is(err, caddyapi.ErrUnreachable) {
			return fmt.Errorf("%w: %v", ErrCaddyNotRunning, err)
		}
		return err
	}

	if err := state.Save(a.paths.StatePath, s); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

// CertList returns the stored certificates, sorted by domain.
func (a *App) CertList(ctx context.Context) ([]CertListing, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return nil, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return nil, err
	}
	domains := make([]string, 0, len(s.Certs))
	for d := range s.Certs {
		domains = append(domains, d)
	}
	sort.Strings(domains)

	out := make([]CertListing, 0, len(domains))
	for _, d := range domains {
		c := s.Certs[d]
		row := CertListing{
			Domain:   c.Domain,
			Env:      c.Env,
			Subjects: c.Subjects,
			NotAfter: c.NotAfter,
		}
		if t, err := time.Parse(time.RFC3339, c.NotAfter); err == nil {
			row.DaysRemaining = int(time.Until(t).Hours() / 24)
		}
		out = append(out, row)
	}
	return out, nil
}

// CertRemove deletes a certificate and pushes a config without it. The bool
// reports whether anything was removed; an unknown domain is not an error,
// matching deregister.
func (a *App) CertRemove(ctx context.Context, domain string) (bool, error) {
	lock, err := state.AcquireLock(a.paths.LockPath, 5*time.Second)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return false, fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return false, err
	}
	defer func() { _ = lock.Release() }()

	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return false, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return false, err
	}
	if _, present := s.Certs[domain]; !present {
		return false, nil
	}

	delete(s.Certs, domain)
	if err := a.certs.Remove(domain); err != nil {
		return false, err
	}

	// Push only when Caddy is up. Removing while it is stopped still updates
	// state; the next start rebuilds the config from state anyway.
	if st, err := a.docker.ContainerState(CaddyContainerName); err == nil && st == docker.StateRunning {
		cfg := caddyapi.Build(s, a.loadedCerts(s))
		if err := a.caddy.Load(ctx, cfg); err != nil && !errors.Is(err, caddyapi.ErrUnreachable) {
			return false, err
		}
	}

	if err := state.Save(a.paths.StatePath, s); err != nil {
		return false, fmt.Errorf("save state: %w", err)
	}
	return true, nil
}

// CertRenew re-orders a certificate when it is within the renewal window, or
// unconditionally when force is set. The bool reports whether an order was
// placed.
func (a *App) CertRenew(ctx context.Context, domain string, force bool, prompt acme.Prompter) (bool, error) {
	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return false, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return false, err
	}
	c, present := s.Certs[domain]
	if !present {
		return false, fmt.Errorf("%w: no certificate for %q; run 'caddy_shack cert request %s' first", ErrInvalidArgs, domain, domain)
	}

	if !force {
		notAfter, err := time.Parse(time.RFC3339, c.NotAfter)
		if err != nil {
			return false, fmt.Errorf("%w: stored expiry for %q is unreadable: %v", ErrStateCorrupt, domain, err)
		}
		if time.Until(notAfter) > renewWindow {
			return false, nil
		}
	}

	st, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return false, fmt.Errorf("inspect container: %w", err)
	}
	if st != docker.StateRunning {
		return false, fmt.Errorf("%w (state=%s)", ErrCaddyNotRunning, st)
	}

	// Renew into the environment the certificate came from. Silently renewing
	// a production certificate against staging would replace a trusted
	// certificate with an untrusted one.
	env := c.Env
	if env == "" {
		env = acme.EnvStaging
	}

	// The contact was stored at issuance so renewal never has to ask again.
	// A certificate written before this field existed has none; the account
	// key is what identifies the account to the server, so fall back rather
	// than refusing to renew.
	email := c.Email
	if email == "" {
		email = "renewal@" + domain
	}
	if err := a.issueAndInstall(ctx, domain, email, env, prompt); err != nil {
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 5: Add the issuer field to App**

In `internal/app/app.go`, add the field and default:

```go
// App is the orchestrator that backs every CLI command.
type App struct {
	paths  Paths
	caddy  *caddyapi.Client
	docker docker.Client
	certs  *certstore.Store
	issuer acme.Issuer
}
```

In `NewWithPaths`, set it alongside the store:

```go
	return &App{
		paths:  p,
		caddy:  caddy,
		docker: dock,
		certs:  certstore.New(p.CertStoreDir),
		issuer: acme.NewLegoIssuer(),
	}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/app/ -run Cert -v`
Expected: PASS, all seventeen tests.

- [ ] **Step 7: Run the whole suite and lint**

Run: `go test ./... && make lint`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/app/cert.go internal/app/cert_test.go internal/app/errors.go internal/app/app.go
git commit -S -m "Added cert request, list, renew and remove to app"
```

---

### Task 8: The cert command group

**Files:**
- Create: `cmd/cert.go`
- Create: `cmd/cert_test.go`
- Modify: `cmd/root.go`

**Interfaces:**
- Consumes: `app.CertRequestInput`, `app.CertListing`, `(*App) CertRequest/CertList/CertRenew/CertRemove` (Task 7); `acme.Record`, `acme.Prompter` (Task 5).
- Produces: `newCertCmd() *cobra.Command`; `cliPrompter` implementing `acme.Prompter`; `renderCertList(w io.Writer, rows []app.CertListing, asJSON bool) error`.

- [ ] **Step 1: Write the failing test**

Create `cmd/cert_test.go`:

```go
package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/acme"
	"github.com/bawdo/caddy_shack/internal/app"
)

func TestCLIPrompterShowsEveryRecordAndWaitsForConfirmation(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("\n")
	p := &cliPrompter{in: in, out: &out}

	err := p.Confirm([]acme.Record{
		{FQDN: "_acme-challenge.alpha.arrix.dev.", Value: "value-one"},
		{FQDN: "_acme-challenge.alpha.arrix.dev.", Value: "value-two"},
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}

	got := out.String()
	for _, want := range []string{"_acme-challenge.alpha.arrix.dev", "value-one", "value-two", "TXT"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt must mention %q, got:\n%s", want, got)
		}
	}
	// Both records live on one name and must coexist. An operator who
	// replaces the first with the second fails validation.
	if !strings.Contains(strings.ToLower(got), "both") {
		t.Errorf("prompt must say both records are needed at once, got:\n%s", got)
	}
}

func TestCLIPrompterErrorsWhenInputClosesWithoutConfirmation(t *testing.T) {
	var out bytes.Buffer
	p := &cliPrompter{in: strings.NewReader(""), out: &out}
	if err := p.Confirm([]acme.Record{{FQDN: "a.", Value: "v"}}); err == nil {
		t.Error("a closed input must error rather than proceed as if confirmed")
	}
}

func TestRenderCertListTable(t *testing.T) {
	var buf bytes.Buffer
	rows := []app.CertListing{{
		Domain:        "alpha.arrix.dev",
		Env:           "production",
		Subjects:      []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
		NotAfter:      "2026-12-07T10:00:00+10:00",
		DaysRemaining: 90,
	}}
	if err := renderCertList(&buf, rows, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	got := buf.String()
	for _, want := range []string{"alpha.arrix.dev", "production", "90"} {
		if !strings.Contains(got, want) {
			t.Errorf("table must contain %q, got:\n%s", want, got)
		}
	}
}

func TestRenderCertListJSON(t *testing.T) {
	var buf bytes.Buffer
	rows := []app.CertListing{{
		Domain:        "alpha.arrix.dev",
		Env:           "staging",
		Subjects:      []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
		NotAfter:      "2026-12-07T10:00:00+10:00",
		DaysRemaining: 90,
	}}
	if err := renderCertList(&buf, rows, true); err != nil {
		t.Fatalf("render: %v", err)
	}
	var back []app.CertListing
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("output must be valid JSON: %v\n%s", err, buf.String())
	}
	if len(back) != 1 || back[0].Domain != "alpha.arrix.dev" {
		t.Errorf("round trip mismatch: %+v", back)
	}
}

func TestRenderCertListEmptyJSONIsAnArrayNotNull(t *testing.T) {
	var buf bytes.Buffer
	if err := renderCertList(&buf, nil, true); err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Errorf("empty JSON = %q, want [] so scripts can iterate without a nil check", got)
	}
}

func TestCertCommandHasTheFourSubcommands(t *testing.T) {
	c := newCertCmd()
	want := map[string]bool{"request": false, "list": false, "renew": false, "remove": false}
	for _, sub := range c.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("cert is missing the %q subcommand", name)
		}
	}
}

func TestCertRequestRequiresEmailFlag(t *testing.T) {
	c := newCertCmd()
	for _, sub := range c.Commands() {
		if sub.Name() == "request" {
			if sub.Flags().Lookup("email") == nil {
				t.Error("request must expose --email")
			}
			if sub.Flags().Lookup("production") == nil {
				t.Error("request must expose --production")
			}
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/ -run Cert -v`
Expected: FAIL to compile, `newCertCmd`, `cliPrompter` and `renderCertList` do not exist.

- [ ] **Step 3: Implement the commands**

Create `cmd/cert.go`:

```go
package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/acme"
	"github.com/bawdo/caddy_shack/internal/app"
)

// cliPrompter shows the DNS records on the command's own streams and waits for
// the operator. It takes its reader and writer as fields rather than reaching
// for os.Stdin so the prompt can be driven in tests through cmd.SetIn.
type cliPrompter struct {
	in  io.Reader
	out io.Writer
}

// Confirm prints every record in one block and blocks until the operator
// presses Enter. Every record is shown at once because a wildcard order puts
// two challenges on the same name and both must exist together.
func (p *cliPrompter) Confirm(records []acme.Record) error {
	fmt.Fprintln(p.out, "\nAdd the following DNS records at your provider:")
	fmt.Fprintln(p.out)
	for _, r := range records {
		fmt.Fprintf(p.out, "  %s  60  IN  TXT  %q\n", strings.TrimSuffix(r.FQDN, "."), r.Value)
	}
	fmt.Fprintln(p.out)
	if len(records) > 1 {
		fmt.Fprintln(p.out, "Both records share one name and must exist at the same time.")
		fmt.Fprintln(p.out, "Do not replace the first when you add the second.")
		fmt.Fprintln(p.out)
	}
	fmt.Fprint(p.out, "Press Enter once the records are saved: ")

	reader := bufio.NewReader(p.in)
	if _, err := reader.ReadString('\n'); err != nil {
		return fmt.Errorf("no confirmation received: %w", err)
	}
	fmt.Fprintln(p.out, "Checking the authoritative nameservers, this can take a minute.")
	return nil
}

func newCertCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "cert",
		Short: "Manage Let's Encrypt certificates for public domains",
		Long: `Obtain and manage publicly-trusted Let's Encrypt certificates for domains
you control, alongside the internal CA that serves .test.

Certificates are requested with a manual DNS-01 challenge: caddy_shack shows
you the TXT records to add at your DNS provider and waits while you add them.
That is the only challenge type available when a domain's public A record
points at 127.0.0.1, because Let's Encrypt validates from its own servers and
cannot reach a loopback address.

Each request covers the domain and its single-label subdomains, so a
certificate for alpha.arrix.dev also serves vite.alpha.arrix.dev.`,
	}
	c.AddCommand(newCertRequestCmd())
	c.AddCommand(newCertListCmd())
	c.AddCommand(newCertRenewCmd())
	c.AddCommand(newCertRemoveCmd())
	return c
}

func newCertRequestCmd() *cobra.Command {
	var (
		email      string
		production bool
	)
	c := &cobra.Command{
		Use:   "request <domain>",
		Short: "Obtain a certificate for a domain and its subdomains",
		Long: `Obtain a Let's Encrypt certificate covering <domain> and *.<domain>, then
load it into the running Caddy.

Requests go to the Let's Encrypt staging environment by default. Staging
certificates are not trusted by browsers, so use them to prove the DNS flow
works, then re-run with --production. Production allows only five duplicate
certificates per week for the same set of names, so a failed attempt against it
is expensive.

The certificate and key are stored on this machine and re-pushed after an
upgrade or a container recreate, so they survive both.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			if !production {
				fmt.Fprintln(cmd.ErrOrStderr(),
					"Using the Let's Encrypt staging environment. The certificate will not be\n"+
						"trusted by browsers. Re-run with --production for a trusted certificate.")
			}
			prompt := &cliPrompter{in: cmd.InOrStdin(), out: cmd.ErrOrStderr()}
			if err := a.CertRequest(cmd.Context(), app.CertRequestInput{
				Domain:     args[0],
				Email:      email,
				Production: production,
			}, prompt); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(),
				"\nCertificate installed for %s and *.%s.\nYou can now remove the _acme-challenge TXT records.\n",
				args[0], args[0])
			return nil
		},
	}
	c.Flags().StringVar(&email, "email", "", "ACME account contact address (required)")
	c.Flags().BoolVar(&production, "production", false,
		"Use the real Let's Encrypt directory instead of staging")
	_ = c.MarkFlagRequired("email")
	return c
}

func newCertListCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List the stored certificates and when they expire",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			rows, err := a.CertList(cmd.Context())
			if err != nil {
				return err
			}
			return renderCertList(cmd.OutOrStdout(), rows, asJSON)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of a table")
	return c
}

func newCertRenewCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "renew <domain>",
		Short: "Renew a certificate that is within 30 days of expiry",
		Long: `Renew a stored certificate. A no-op unless the certificate has fewer than
30 days left, so it is safe to run repeatedly. Pass --force to renew anyway.

Renewal uses the same DNS-01 prompt as request, and renews into whichever
environment the certificate came from.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			prompt := &cliPrompter{in: cmd.InOrStdin(), out: cmd.ErrOrStderr()}
			renewed, err := a.CertRenew(cmd.Context(), args[0], force, prompt)
			if err != nil {
				return err
			}
			if !renewed {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"%s is not due for renewal. Use --force to renew anyway.\n", args[0])
				return nil
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Renewed %s.\n", args[0])
			return nil
		},
	}
	c.Flags().BoolVar(&force, "force", false, "Renew even when the certificate is not due")
	return c
}

func newCertRemoveCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "remove <domain>",
		Short: "Delete a stored certificate and stop serving it",
		Long: `Delete a stored certificate and push a config without it.

Idempotent: removing a domain with no certificate exits 0 with a note on
stderr. The domain's routes are untouched; use deregister for those.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			removed, err := a.CertRemove(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !removed {
				fmt.Fprintf(cmd.ErrOrStderr(), "no certificate for %s\n", args[0])
				return nil
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Removed the certificate for %s.\n", args[0])
			return nil
		},
	}
	return c
}

// renderCertList writes the listing as a table, or as JSON when asJSON.
func renderCertList(w io.Writer, rows []app.CertListing, asJSON bool) error {
	if asJSON {
		if rows == nil {
			rows = []app.CertListing{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			return fmt.Errorf("encode json: %w", err)
		}
		return nil
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "no certificates")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DOMAIN\tENV\tEXPIRES\tDAYS LEFT\tSUBJECTS")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			r.Domain, r.Env, r.NotAfter, r.DaysRemaining, strings.Join(r.Subjects, ", "))
	}
	return tw.Flush()
}
```

- [ ] **Step 4: Register the command and document exit code 9**

In `cmd/root.go`, add to the `AddCommand` block after `newCACmd()`:

```go
	root.AddCommand(newCertCmd())
```

And in the root `Long` string, add the exit code line after the code 8 line:

```
  9  certificate issuance failed (ACME order, DNS validation, or rate limit)
```

- [ ] **Step 5: Run the tests**

Run: `go test ./cmd/ -run Cert -v`
Expected: PASS.

- [ ] **Step 6: Exercise the CLI by hand**

Run:

```bash
go build -o /tmp/caddy_shack . && /tmp/caddy_shack cert --help && /tmp/caddy_shack cert request --help && /tmp/caddy_shack --help | grep -A1 '  8  '
```

Expected: the cert help lists all four subcommands, `request` shows `--email` and `--production`, and the root help lists exit code 9.

- [ ] **Step 7: Run the whole suite and lint**

Run: `go test ./... && make lint`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add cmd/cert.go cmd/cert_test.go cmd/root.go
git commit -S -m "Added cert command group to the CLI"
```

---

### Task 9: End-to-end test against Pebble

**Files:**
- Create: `test/integration/cert_e2e_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1 to 8.
- Produces: nothing consumed by later tasks.

This is where the hypothesis is actually tested. The assertion is on the **issuer of the certificate Caddy presents**, not merely on a successful request: a passing request would not distinguish "Caddy served our loaded certificate" from "Caddy minted an internal one over the top", and telling those apart is the whole point.

Read `test/integration/helpers.go` first and reuse its fixtures rather than inventing new ones.

- [ ] **Step 1: Write the test**

Create `test/integration/cert_e2e_test.go`:

```go
//go:build integration

package integration

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/acme"
	"github.com/bawdo/caddy_shack/internal/app"
)

const (
	pebbleImage    = "ghcr.io/letsencrypt/pebble:latest"
	challtestImage = "ghcr.io/letsencrypt/pebble-challtestsrv:latest"
	pebbleDirEnv   = "https://127.0.0.1:14000/dir"
	certTestDomain = "cert-e2e.example.com"
)

// autoPrompter answers the DNS-01 prompt by publishing the TXT records into
// pebble-challtestsrv, standing in for a human editing DNS.
type autoPrompter struct {
	t     *testing.T
	calls int
}

func (p *autoPrompter) Confirm(records []acme.Record) error {
	p.calls++
	for _, r := range records {
		host := strings.TrimSuffix(r.FQDN, ".")
		body := `{"host":"` + host + `","value":"` + r.Value + `"}`
		out, err := exec.Command("curl", "-sS", "-X", "POST",
			"--data", body, "http://127.0.0.1:8055/set-txt").CombinedOutput()
		if err != nil {
			p.t.Fatalf("publish TXT %s: %v (%s)", host, err, out)
		}
	}
	return nil
}

func TestCertIssuedByPebbleIsServedAndInternalCAStillWorks(t *testing.T) {
	requireDocker(t)
	startPebble(t)

	env := newTestEnv(t)
	env.install(t)
	t.Cleanup(func() { env.teardown(t) })

	// A .test backend on the internal CA, alongside the Pebble-covered one.
	env.run(t, "register", "hello", "--port", "3000")
	env.run(t, "register", "web", "--port", "3001", "--domain", certTestDomain)

	a := env.app(t)
	iss := acme.NewLegoIssuer()
	iss.DirectoryOverride = pebbleDirEnv
	a.SetIssuer(iss)

	prompt := &autoPrompter{t: t}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := a.CertRequest(ctx, app.CertRequestInput{
		Domain: certTestDomain,
		Email:  "e2e@example.com",
	}, prompt); err != nil {
		t.Fatalf("cert request against pebble: %v", err)
	}

	// One prompt, not one per identifier. This is the parallelSolve
	// behaviour the custom provider exists to get.
	if prompt.calls != 1 {
		t.Errorf("operator prompted %d times, want exactly 1 for a wildcard order", prompt.calls)
	}

	pebbleIssuer := presentedIssuer(t, certTestDomain)
	if !strings.Contains(strings.ToLower(pebbleIssuer), "pebble") {
		t.Errorf("issuer for %s = %q, want the Pebble CA. Caddy is not serving the loaded certificate",
			certTestDomain, pebbleIssuer)
	}

	// The wildcard must cover a subdomain with no route of its own, which is
	// what makes vite.alpha.arrix.dev work in real use.
	subIssuer := presentedIssuer(t, "sub."+certTestDomain)
	if !strings.Contains(strings.ToLower(subIssuer), "pebble") {
		t.Errorf("issuer for sub.%s = %q, want the Pebble CA. The wildcard does not cover subdomains",
			certTestDomain, subIssuer)
	}

	// The internal CA must be untouched for .test.
	internalIssuer := presentedIssuer(t, "hello.test")
	if strings.Contains(strings.ToLower(internalIssuer), "pebble") {
		t.Errorf("issuer for hello.test = %q, want Caddy's internal CA", internalIssuer)
	}
}

func TestLoadedCertSurvivesUpgrade(t *testing.T) {
	requireDocker(t)
	startPebble(t)

	env := newTestEnv(t)
	env.install(t)
	t.Cleanup(func() { env.teardown(t) })

	env.run(t, "register", "web", "--port", "3001", "--domain", certTestDomain)

	a := env.app(t)
	iss := acme.NewLegoIssuer()
	iss.DirectoryOverride = pebbleDirEnv
	a.SetIssuer(iss)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := a.CertRequest(ctx, app.CertRequestInput{
		Domain: certTestDomain, Email: "e2e@example.com",
	}, &autoPrompter{t: t}); err != nil {
		t.Fatalf("cert request: %v", err)
	}

	env.run(t, "upgrade")

	// Certificates live on the host and are re-pushed from state, so an
	// upgrade that recreates the container must not lose them.
	got := presentedIssuer(t, certTestDomain)
	if !strings.Contains(strings.ToLower(got), "pebble") {
		t.Errorf("issuer after upgrade = %q, want the Pebble CA. The certificate did not survive", got)
	}
}

func TestRegisterAfterCertRequestDoesNotDropTheCertificate(t *testing.T) {
	requireDocker(t)
	startPebble(t)

	env := newTestEnv(t)
	env.install(t)
	t.Cleanup(func() { env.teardown(t) })

	env.run(t, "register", "web", "--port", "3001", "--domain", certTestDomain)

	a := env.app(t)
	iss := acme.NewLegoIssuer()
	iss.DirectoryOverride = pebbleDirEnv
	a.SetIssuer(iss)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := a.CertRequest(ctx, app.CertRequestInput{
		Domain: certTestDomain, Email: "e2e@example.com",
	}, &autoPrompter{t: t}); err != nil {
		t.Fatalf("cert request: %v", err)
	}

	// Every config push must carry the certificates forward. A register that
	// rebuilt the config without them would silently drop the certificate
	// out of the running Caddy.
	env.run(t, "register", "other", "--port", "3002")

	got := presentedIssuer(t, certTestDomain)
	if !strings.Contains(strings.ToLower(got), "pebble") {
		t.Errorf("issuer after an unrelated register = %q, want the Pebble CA. "+
			"A caddyapi.Build call site is not passing loadedCerts", got)
	}
}

// presentedIssuer opens a TLS connection to the local Caddy with SNI set to
// host and returns the issuer common name of the leaf it presents.
// Verification is skipped deliberately: the identity of the issuer is what is
// under test, not its trustworthiness.
func presentedIssuer(t *testing.T, host string) string {
	t.Helper()
	var lastErr error
	for i := 0; i < 10; i++ {
		conn, err := tls.Dial("tcp", "127.0.0.1:443", &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: true, // #nosec G402 -- inspecting the presented chain is the test
		})
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		defer func() { _ = conn.Close() }()
		chain := conn.ConnectionState().PeerCertificates
		if len(chain) == 0 {
			t.Fatalf("no certificate presented for %s", host)
		}
		return chain[0].Issuer.CommonName
	}
	t.Fatalf("could not complete a TLS handshake for %s: %v", host, lastErr)
	return ""
}

// startPebble runs Pebble and its challenge DNS server, and registers cleanup.
func startPebble(t *testing.T) {
	t.Helper()
	stop := func(name string) {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	}
	stop("caddy_shack_pebble")
	stop("caddy_shack_challtestsrv")

	// challtestsrv answers the _acme-challenge TXT lookups Pebble makes.
	if out, err := exec.Command("docker", "run", "-d", "--name", "caddy_shack_challtestsrv",
		"-p", "8055:8055", "-p", "8053:8053/udp",
		challtestImage, "-defaultIPv6", "", "-defaultIPv4", "127.0.0.1",
	).CombinedOutput(); err != nil {
		t.Fatalf("start challtestsrv: %v (%s)", err, out)
	}
	t.Cleanup(func() { stop("caddy_shack_challtestsrv") })

	if out, err := exec.Command("docker", "run", "-d", "--name", "caddy_shack_pebble",
		"-p", "14000:14000", "-p", "15000:15000",
		"-e", "PEBBLE_VA_NOSLEEP=1",
		"--add-host", "host.docker.internal:host-gateway",
		pebbleImage,
		"-dnsserver", "host.docker.internal:8053",
	).CombinedOutput(); err != nil {
		t.Fatalf("start pebble: %v (%s)", err, out)
	}
	t.Cleanup(func() { stop("caddy_shack_pebble") })

	waitForPort(t, "127.0.0.1:14000", 30*time.Second)
	waitForPort(t, "127.0.0.1:8055", 30*time.Second)
}

func waitForPort(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true}) // #nosec G402 -- readiness probe
		if err == nil {
			_ = conn.Close()
			return
		}
		if c, err := netDial(addr); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("%s did not come up within %s", addr, timeout)
}
```

Add a small `netDial` helper and its `net` import, plus `x509` if the compiler asks for it:

```go
func netDial(addr string) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, 2*time.Second)
}
```

- [ ] **Step 2: Reconcile with the existing helpers**

Read `test/integration/helpers.go`. The test above assumes helpers named `requireDocker`, `newTestEnv`, `env.install`, `env.run`, `env.teardown` and `env.app`. Some will exist under different names and `env.app` almost certainly does not exist yet.

Where a helper is missing, add it to `helpers.go` following the file's established style. `env.app` must build an `*app.App` against the same state directory the CLI fixture uses, so `app.NewWithPaths` is the constructor to reach for. Do not rename or change any existing helper; other tests depend on them.

Also confirm `LegoIssuer` needs to skip verification of Pebble's own TLS. Pebble serves its directory over HTTPS with a self-signed certificate. If the order fails with an x509 error, add to `LegoIssuer`:

```go
	// InsecureSkipTLSVerify allows an integration test to talk to Pebble,
	// which serves its directory with a self-signed certificate. Never set
	// this for a real ACME server.
	InsecureSkipTLSVerify bool
```

and in `Issue`, when it is set, give `cfg.HTTPClient` a transport with `InsecureSkipVerify: true`. Set it from the test alongside `DirectoryOverride`.

- [ ] **Step 3: Run the integration suite**

Run: `go test -tags integration -v ./test/integration/ -run Cert`
Expected: PASS. Expect this to need iteration: Pebble's DNS wiring and the container's view of `host.docker.internal` are the usual sticking points. If `TestCertIssuedByPebble...` fails on the issuer assertion rather than on plumbing, **stop and report it**: that is the hypothesis being disproven, and it is a result worth recording rather than a bug to code around.

- [ ] **Step 4: Run the full integration suite for regressions**

Run: `go test -tags integration -v ./test/integration/`
Expected: PASS, including every pre-existing test.

- [ ] **Step 5: Commit**

```bash
git add test/integration/cert_e2e_test.go test/integration/helpers.go
git commit -S -m "Added Pebble end-to-end test for issued certificates"
```

---

### Task 10: Documentation

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: the CLI surface from Task 8.
- Produces: nothing.

- [ ] **Step 1: Add the Let's Encrypt section**

In `README.md`, insert a new section after the "Trusting the CA" section and before "Exit codes":

````markdown
### Let's Encrypt certificates for a public domain

`.test` domains are served by Caddy's internal CA. For a domain you actually
own, `caddy_shack cert` obtains a publicly-trusted Let's Encrypt certificate
and loads it into the same running Caddy.

This is useful for local development against a real domain whose public DNS
points at `127.0.0.1`: you get real HTTPS with no trust-store fiddling, and
anything that only trusts public CAs works without extra setup.

Because the domain resolves to loopback, Let's Encrypt cannot reach it to
validate over HTTP. DNS-01 is the only challenge that works, and caddy_shack
runs it manually: it prints the TXT records and waits while you add them at your
DNS provider.

```
caddy_shack cert request alpha.arrix.dev --email you@example.com
```

Requests go to the **staging** environment by default. Staging certificates are
not trusted by browsers, which is the point: prove the DNS flow works without
spending quota. Production allows only five duplicate certificates per week for
the same set of names.

```
caddy_shack cert request alpha.arrix.dev --email you@example.com --production
```

Each certificate covers the domain and its single-label subdomains, so one
request serves both `alpha.arrix.dev` and `vite.alpha.arrix.dev`. That matches
how `register` already routes: registering a backend serves its wildcard too.

```
caddy_shack register web --port 5173 --domain alpha.arrix.dev
# https://alpha.arrix.dev and https://vite.alpha.arrix.dev are both live
```

A wildcard order produces **two** TXT records on the same name, one per
identifier. Both must exist at the same time; do not replace the first when you
add the second. caddy_shack shows both at once and then polls your authoritative
nameservers, so pressing Enter early is harmless.

Managing what you have:

```
caddy_shack cert list           # domains, environment, expiry, days left
caddy_shack cert list --json    # for scripts
caddy_shack cert renew alpha.arrix.dev          # no-op unless within 30 days
caddy_shack cert renew alpha.arrix.dev --force  # renew regardless
caddy_shack cert remove alpha.arrix.dev         # stop serving it
```

Certificates last 90 days. Because the challenge is manual, **renewal is manual
too**: `cert renew` prompts for the TXT records the same way `cert request`
does. `cert list` shows the days remaining so you can see one coming.

Certificate material is stored on this machine, not inside Caddy's volume, so it
survives `caddy_shack upgrade` and container recreates the same way registered
backends do.
````

- [ ] **Step 2: Document exit code 9**

In the "Exit codes" block in `README.md`, add after the code 8 line:

```
9  certificate issuance failed (ACME order, DNS validation, or rate limit)
```

- [ ] **Step 3: Check the prose**

Run:

```bash
grep -n '—\|–' README.md || echo "no em or en dashes"
```

Expected: no em or en dashes.

- [ ] **Step 4: Verify the documented commands actually exist**

Run:

```bash
go build -o /tmp/caddy_shack . \
  && /tmp/caddy_shack cert list --help \
  && /tmp/caddy_shack cert renew --help \
  && /tmp/caddy_shack cert remove --help
```

Expected: each resolves, with flags matching what the README claims.

- [ ] **Step 5: Full pre-CI gauntlet**

Run: `make pre-ci`
Expected: every check passes.

- [ ] **Step 6: Commit**

```bash
git add README.md
git commit -S -m "Documented Let's Encrypt certificate support"
```

---

## After the plan

Once every task is done, the branch is ready for the closing half of the R&D workflow. Do not skip it: the branch carries a `Wombat-RnD: opened` trailer, and the closeout is what answers it.

- Manual acceptance against the real thing: `caddy_shack cert request alpha.arrix.dev --email <addr>` against staging first, then `--production`, then confirm `curl https://alpha.arrix.dev` and `curl https://vite.alpha.arrix.dev` both succeed with no `-k`.
- Then invoke `superpowers:finishing-a-development-branch`, which fires the R&D closing gate. That produces the statement of fit (FIT, PROVEN, DISPROVEN, DIVERGENCE) and a `2026-09-08-letsencrypt-certs-closeout.md` alongside the design document.

If the hypothesis was disproven at any point, say so plainly in the closeout. A disproven hypothesis is a result, not a failure.

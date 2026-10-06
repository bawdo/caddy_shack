package app

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

	got, err := a.loadedCerts(s)
	if err != nil {
		t.Fatalf("loadedCerts: %v", err)
	}
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
	got, err := a.loadedCerts(s)
	if err != nil {
		t.Fatalf("loadedCerts: %v", err)
	}
	if len(got) != 0 {
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
	first, err := a.loadedCerts(s)
	if err != nil {
		t.Fatalf("loadedCerts: %v", err)
	}
	for i := 0; i < 20; i++ {
		got, err := a.loadedCerts(s)
		if err != nil {
			t.Fatalf("loadedCerts: %v", err)
		}
		if got[0].CertPEM != first[0].CertPEM || got[2].CertPEM != first[2].CertPEM {
			t.Fatal("loadedCerts must be deterministically ordered across calls")
		}
	}
	if first[0].Subjects[0] != "alpha.example.com" {
		t.Errorf("expected domain-sorted order, first = %v", first[0].Subjects)
	}
	_ = os.Getpid()
}

// TestLoadedCertsPropagatesNonNotFoundErrors proves a real store fault (a
// permissions error, here) is surfaced to the caller rather than being
// silently treated the same as a hand-deleted file. Only certstore.ErrNotFound
// may be swallowed.
func TestLoadedCertsPropagatesNonNotFoundErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced the same way on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses file permissions, so this test cannot force a permission error")
	}

	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath:    filepath.Join(dir, "state.json"),
		LockPath:     filepath.Join(dir, "state.json.lock"),
		CertStoreDir: dir,
	}, nil, nil)

	if err := a.certs.Save("broken.example.com", []byte("CERTPEM"), []byte("KEYPEM")); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	certPath := a.certs.CertPath("broken.example.com")
	if err := os.Chmod(certPath, 0o000); err != nil {
		t.Fatalf("chmod cert unreadable: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(certPath, 0o644)
	})

	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends:      map[string]state.Backend{},
		Certs: map[string]state.Cert{
			"broken.example.com": {Domain: "broken.example.com", Subjects: []string{"broken.example.com"}},
		},
	}

	got, err := a.loadedCerts(s)
	if err == nil {
		t.Fatalf("expected an error for unreadable cert material, got certs %+v", got)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected a permission-style error, not a not-exist error: %v", err)
	}
}

// TestLoadedCertsSkipsOnlyTheMissingDomain proves certstore.ErrNotFound skips
// just the affected domain, leaving material for other domains in the same
// state intact.
func TestLoadedCertsSkipsOnlyTheMissingDomain(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath:    filepath.Join(dir, "state.json"),
		LockPath:     filepath.Join(dir, "state.json.lock"),
		CertStoreDir: dir,
	}, nil, nil)

	if err := a.certs.Save("present.example.com", []byte("CERTPEM"), []byte("KEYPEM")); err != nil {
		t.Fatalf("seed present: %v", err)
	}
	if err := a.certs.Save("missing.example.com", []byte("CERTPEM2"), []byte("KEYPEM2")); err != nil {
		t.Fatalf("seed missing: %v", err)
	}
	if err := a.certs.Remove("missing.example.com"); err != nil {
		t.Fatalf("remove missing: %v", err)
	}

	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends:      map[string]state.Backend{},
		Certs: map[string]state.Cert{
			"present.example.com": {Domain: "present.example.com", Subjects: []string{"present.example.com"}},
			"missing.example.com": {Domain: "missing.example.com", Subjects: []string{"missing.example.com"}},
		},
	}

	got, err := a.loadedCerts(s)
	if err != nil {
		t.Fatalf("loadedCerts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly the present domain's cert, got %d: %+v", len(got), got)
	}
	if got[0].CertPEM != "CERTPEM" {
		t.Errorf("material mismatch: %+v", got[0])
	}
}

// TestLoadedCertsDoesNotSynthesiseSubjects proves an entry with no Subjects
// yields a LoadedCert with no subjects rather than inventing an apex+wildcard
// pair the certificate may not actually cover. Synthesising a wildcard here
// would claim coverage the certificate does not have; leaving it empty is
// the safe failure direction (no coverage claimed, internal issuer keeps the
// domain).
func TestLoadedCertsDoesNotSynthesiseSubjects(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath:    filepath.Join(dir, "state.json"),
		LockPath:     filepath.Join(dir, "state.json.lock"),
		CertStoreDir: dir,
	}, nil, nil)

	if err := a.certs.Save("apexonly.example.com", []byte("CERTPEM"), []byte("KEYPEM")); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends:      map[string]state.Backend{},
		Certs: map[string]state.Cert{
			"apexonly.example.com": {Domain: "apexonly.example.com"},
		},
	}

	got, err := a.loadedCerts(s)
	if err != nil {
		t.Fatalf("loadedCerts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one loaded cert, got %d", len(got))
	}
	if len(got[0].Subjects) != 0 {
		t.Errorf("expected no synthesised subjects, got %v", got[0].Subjects)
	}
}

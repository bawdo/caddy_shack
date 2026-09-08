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

func TestInvalidDomainsRejected(t *testing.T) {
	invalidDomains := []string{
		".",
		"..",
		"...",
		"/abs/path",
		"a/b",
		"a\\b",
		"./x",
		"x/..",
	}

	for _, domain := range invalidDomains {
		t.Run("Save_"+domain, func(t *testing.T) {
			dir := t.TempDir()
			s := New(dir)

			// Save a valid domain first to ensure it survives rejection of invalid domain.
			if err := s.Save("valid.example.com", []byte(testCertPEM), []byte(testKeyPEM)); err != nil {
				t.Fatalf("save valid domain: %v", err)
			}

			// Try to save with invalid domain.
			err := s.Save(domain, []byte(testCertPEM), []byte(testKeyPEM))
			if err == nil {
				t.Errorf("Save(%q) must be rejected, got nil error", domain)
			}

			// Verify valid domain material is still intact.
			if !s.Has("valid.example.com") {
				t.Errorf("Save(%q) corrupted unrelated domain", domain)
			}
		})

		t.Run("Remove_"+domain, func(t *testing.T) {
			dir := t.TempDir()
			s := New(dir)

			// Save a valid domain first.
			if err := s.Save("valid.example.com", []byte(testCertPEM), []byte(testKeyPEM)); err != nil {
				t.Fatalf("save valid domain: %v", err)
			}

			// Try to remove with invalid domain.
			err := s.Remove(domain)
			if err == nil {
				t.Errorf("Remove(%q) must be rejected, got nil error", domain)
			}

			// Verify valid domain material is still intact.
			if !s.Has("valid.example.com") {
				t.Errorf("Remove(%q) corrupted unrelated domain", domain)
			}
		})
	}
}

func TestStaleTmpFileChmodIsHonoured(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	// Create a stale key.pem.tmp with wrong permissions.
	keyPath := s.KeyPath("test.example.com")
	tmpPath := keyPath + ".tmp"

	if err := os.MkdirAll(filepath.Dir(tmpPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Plant a stale tmp file with incorrect mode.
	if err := os.WriteFile(tmpPath, []byte("olddata"), 0o666); err != nil {
		t.Fatalf("plant stale tmp: %v", err)
	}

	// Save should overwrite with correct permissions.
	if err := s.Save("test.example.com", []byte(testCertPEM), []byte(testKeyPEM)); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Verify key file has 0600 permissions, not 0666 from stale tmp.
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("key.pem mode = %#o, want 0600 (stale tmp mode not overridden)", perm)
	}
}

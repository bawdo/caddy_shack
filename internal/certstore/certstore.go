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

// ErrNotFound reports that the store holds no certificate material for a domain.
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
	clean := filepath.Clean(domain)
	if domain != clean {
		return fmt.Errorf("certstore: invalid domain %q", domain)
	}
	if clean == "." || clean == ".." {
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
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm) //nolint:gosec // path derived from validated user input (domain)
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
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("certstore: chmod tmp: %w", err)
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

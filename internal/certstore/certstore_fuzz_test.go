package certstore

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzValidDomain checks that anything validDomain accepts is a single local
// path element, so CertPath can never point outside the store directory.
func FuzzValidDomain(f *testing.F) {
	// Seeds from TestInvalidDomainsRejected, plus names the store accepts.
	for _, seed := range []string{
		".", "..", "...", "/abs/path", "a/b", `a\b`, "./x", "x/..",
		"valid.example.com", "alpha.arrix.dev", "../escape", "",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, domain string) {
		if validDomain(domain) != nil {
			return
		}
		if !filepath.IsLocal(domain) {
			t.Fatalf("validDomain accepted %q, which is not a local path", domain)
		}
		if strings.ContainsAny(domain, `/\`) {
			t.Fatalf("validDomain accepted %q, which contains a path separator", domain)
		}
		dir := t.TempDir()
		got := New(dir).CertPath(domain)
		if !strings.HasPrefix(got, dir+string(filepath.Separator)) {
			t.Fatalf("CertPath(%q) = %q, which escapes %q", domain, got, dir)
		}
	})
}

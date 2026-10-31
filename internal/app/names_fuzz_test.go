package app

import (
	"strings"
	"testing"
)

// FuzzValidLANName checks the invariants of a DNS name that validLANName
// accepts: bounded length, lower case, not in the .test space, and sane labels.
func FuzzValidLANName(f *testing.F) {
	// Seeds from TestLANEnableRejectsTestSuffix and the LAN tests.
	for _, seed := range []string{
		"arrix.test", "test", "Bad_Name", "-x", "lan", "arrix.home.lan",
		"a", "a-b", "a.b.c", "x-", "a..b", ".a", "a.", "",
		strings.Repeat("a", 63), strings.Repeat("a", 64),
		strings.Repeat("a.", 126) + "a",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if validLANName(name) != nil {
			return
		}
		if len(name) > 253 {
			t.Fatalf("validLANName accepted %q, longer than 253 bytes", name)
		}
		if name != strings.ToLower(name) {
			t.Fatalf("validLANName accepted %q, which is not lower case", name)
		}
		if name == "test" || strings.HasSuffix(name, ".test") {
			t.Fatalf("validLANName accepted %q, which is in the .test space", name)
		}
		for _, label := range strings.Split(name, ".") {
			if len(label) < 1 || len(label) > 63 {
				t.Fatalf("validLANName accepted %q with a label of %d bytes", name, len(label))
			}
			if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				t.Fatalf("validLANName accepted %q with label %q starting or ending in a hyphen", name, label)
			}
		}
	})
}

// FuzzValidateCertDomain checks that a domain validateCertDomain accepts is a
// plain public name: no wildcard, no .test, no path characters.
func FuzzValidateCertDomain(f *testing.F) {
	// Seeds from the cert request tests.
	for _, seed := range []string{
		"alpha.arrix.dev", "Alpha.arrix.dev", "*.alpha.arrix.dev", "hello.test",
		"a.b", "a", "../x.dev", "a/b.dev", `a\b.dev`, "a..b.dev", "",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, domain string) {
		if validateCertDomain(domain) != nil {
			return
		}
		if strings.HasPrefix(domain, "*.") {
			t.Fatalf("validateCertDomain accepted wildcard %q", domain)
		}
		if strings.HasSuffix(domain, ".test") {
			t.Fatalf("validateCertDomain accepted %q, which is in the .test space", domain)
		}
		if strings.ContainsAny(domain, `/\`) {
			t.Fatalf("validateCertDomain accepted %q, which contains a path separator", domain)
		}
		if strings.Contains(domain, "..") {
			t.Fatalf("validateCertDomain accepted %q, which contains ..", domain)
		}
	})
}

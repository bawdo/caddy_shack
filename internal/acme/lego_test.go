package acme

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	got, err := f.Issue(t.Context(), Request{Domains: []string{"a.example.com"}, Env: EnvStaging}, &recordingPrompter{})
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

// TestFakeIssuerRejectsNilPrompter pins the fake's fidelity to LegoIssuer:
// the real issuer refuses a nil prompter, and an app-layer bug that forgets
// to pass one must fail here too, not only in production against a real
// ACME server.
func TestFakeIssuerRejectsNilPrompter(t *testing.T) {
	f := NewFakeIssuer(Result{CertPEM: []byte("CERT")}, nil)

	_, err := f.Issue(t.Context(), Request{Domains: []string{"a.example.com"}, Env: EnvStaging}, nil)
	if err == nil {
		t.Fatal("expected an error for a nil prompter")
	}
	if len(f.Calls) != 0 {
		t.Errorf("a rejected request must not be recorded, got %+v", f.Calls)
	}
}

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

package app

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/certstore"
	"github.com/bawdo/caddy_shack/internal/state"
)

// These tests cover the seam between the registry commands (register,
// deregister, sync) and Let's Encrypt certificates: every one of them
// rebuilds the config from state, so each must keep the certs map in
// state.json and keep the certificate loaded in Caddy.

const testCertPEM = "-----BEGIN CERTIFICATE-----\nREGISTRYCERT\n-----END CERTIFICATE-----\n"

// withCert seeds f's registry with one backend and one Let's Encrypt
// certificate, backed by material in a private cert store.
func withCert(t *testing.T, f *fixture) {
	t.Helper()
	f.app.certs = certstore.New(filepath.Join(t.TempDir(), "store"))
	if err := f.app.certs.Save("alpha.arrix.dev", []byte(testCertPEM),
		[]byte("-----BEGIN EC PRIVATE KEY-----\nK\n-----END EC PRIVATE KEY-----\n")); err != nil {
		t.Fatal(err)
	}
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends: map[string]state.Backend{
			"web": {Domain: "alpha.arrix.dev", Upstream: "host.docker.internal:5173"},
		},
		Certs: map[string]state.Cert{
			"alpha.arrix.dev": {
				Domain:   "alpha.arrix.dev",
				Subjects: []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
				Env:      "staging",
			},
		},
	}
	if err := state.Save(f.statePath, s); err != nil {
		t.Fatal(err)
	}
}

// assertCertKept checks that state.json still records the certificate and
// that the last config pushed to Caddy still loads it.
func assertCertKept(t *testing.T, f *fixture) {
	t.Helper()
	s, err := state.Load(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Certs["alpha.arrix.dev"]; !ok {
		t.Errorf("state.json lost the certificate record: %+v", s.Certs)
	}
	f.mu.Lock()
	body := string(f.lastBody)
	f.mu.Unlock()
	if !strings.Contains(body, "REGISTRYCERT") {
		t.Errorf("pushed config no longer loads the certificate:\n%s", body)
	}
}

func TestRegisterKeepsCertificates(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	withCert(t, f)

	if err := f.app.Register(context.Background(), RegisterInput{Name: "api", Port: 4000}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	assertCertKept(t, f)
}

func TestDeregisterKeepsCertificates(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	withCert(t, f)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "api", Port: 4000}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := f.app.Deregister(context.Background(), "api", false); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	assertCertKept(t, f)
}

func TestSyncRestoresCertificatesAfterCaddyRestart(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	withCert(t, f)
	f.setLive(bareCaddyConfig)

	rep, err := f.app.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !rep.Pushed {
		t.Fatal("a Caddy that lost its config must be re-pushed")
	}
	assertCertKept(t, f)

	// Caddy now holds the certificate, so the registry must read as in
	// sync: a drift check that ignored load_pem, or one that left it out of
	// the desired config, would push on every command.
	before := f.loads()
	rep, err = f.app.Sync(context.Background())
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if rep.Pushed || f.loads() != before {
		t.Errorf("second sync pushed again; drift: %+v", rep.Before)
	}
}

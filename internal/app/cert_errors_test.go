package app

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/acme"
	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

const certDomain = "alpha.arrix.dev"

// hookPrompter runs fn when the issuer asks the operator to confirm the DNS
// records, which is after the cheap pre-order checks and before the state
// lock is taken: the window a concurrent command can change things in.
type hookPrompter struct{ fn func() }

func (p hookPrompter) Confirm([]acme.Record) error {
	p.fn()
	return nil
}

// requestedCert issues certDomain through a, so a test starts from stored
// material and a state record.
func requestedCert(t *testing.T, a *App) {
	t.Helper()
	if err := a.CertRequest(t.Context(), CertRequestInput{Domain: certDomain, Email: "k@example.com"}, &noopPrompter{}); err != nil {
		t.Fatalf("request: %v", err)
	}
}

// recordPushes points a at a recording admin server and returns its bodies.
func recordPushes(t *testing.T, a *App) *[][]byte {
	t.Helper()
	srv, bodies := recordingCaddyServer(t)
	a.caddy = caddyapi.NewClient(srv.URL, time.Second)
	return bodies
}

// readOnly makes dir unwritable for the rest of the test.
func readOnly(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

func TestCertRequestContainerFailuresPlaceNoOrder(t *testing.T) {
	cases := map[string]struct {
		st       docker.ContainerState
		inject   func(d *docker.Fake)
		wantIs   error
		wantExit int
	}{
		"inspect": {docker.StateRunning, func(d *docker.Fake) {
			d.ContainerStateErrs = map[string]error{CaddyContainerName: errInjected}
		}, errInjected, 1},
		"absent": {docker.StateAbsent, func(*docker.Fake) {}, ErrNotInstalled, 6},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a, iss := certTestApp(t, tc.st)
			d := a.docker.(*docker.Fake)
			if tc.st == docker.StateAbsent {
				delete(d.Containers, CaddyContainerName)
			}
			tc.inject(d)
			err := a.CertRequest(t.Context(), CertRequestInput{Domain: certDomain, Email: "k@example.com"}, &noopPrompter{})
			if !errors.Is(err, tc.wantIs) || ExitCode(err) != tc.wantExit {
				t.Fatalf("err = %v (exit %d), want %v (exit %d)", err, ExitCode(err), tc.wantIs, tc.wantExit)
			}
			if len(iss.Calls) != 0 {
				t.Error("an ACME order was placed before the container check")
			}
		})
	}
}

// TestIssueAndInstallFailuresAfterTheOrder: once the order is in, a failure
// before the commit writes nothing: no material, no state record, no push,
// and the state lock is released.
func TestIssueAndInstallFailuresAfterTheOrder(t *testing.T) {
	cases := []struct {
		name string
		// during runs while the operator confirms the DNS records.
		during   func(t *testing.T, a *App)
		wantIs   error
		wantErr  string
		lockHeld bool
	}{
		{
			name: "lock taken",
			during: func(t *testing.T, a *App) {
				shortLockTimeout(t)
				l, err := state.AcquireLock(a.paths.LockPath, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = l.Release() })
			},
			wantIs:   ErrLockContention,
			lockHeld: true,
		},
		{
			name: "state corrupted",
			during: func(t *testing.T, a *App) {
				if err := os.WriteFile(a.paths.StatePath, []byte("{not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantIs: ErrStateCorrupt,
		},
		{
			name: "store unwritable",
			during: func(t *testing.T, a *App) {
				dir := filepath.Dir(a.certs.CertPath(certDomain))
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				readOnly(t, dir)
			},
			wantErr: "certstore: open tmp",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, iss := certTestApp(t, docker.StateRunning)
			bodies := recordPushes(t, a)
			err := a.CertRequest(t.Context(), CertRequestInput{Domain: certDomain, Email: "k@example.com"},
				hookPrompter{fn: func() { tc.during(t, a) }})
			if err == nil {
				t.Fatal("want an error")
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("err = %v, want %v", err, tc.wantIs)
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
			if len(iss.Calls) != 1 {
				t.Fatalf("orders placed: %d", len(iss.Calls))
			}
			if a.certs.Has(certDomain) {
				t.Error("material written after the failure")
			}
			if s, lerr := state.Load(a.paths.StatePath); lerr == nil && len(s.Certs) != 0 {
				t.Errorf("state records %v", s.Certs)
			}
			if len(*bodies) != 0 {
				t.Errorf("caddy pushed %d times", len(*bodies))
			}
			if !tc.lockHeld {
				if held, _ := state.LockHeld(a.paths.LockPath); held {
					t.Error("state lock leaked")
				}
			}
		})
	}
}

// A prior key that turns unreadable between the pre-order gate and the read
// under the lock still stops the install before the store is written, so
// the undo can never RemoveAll material it could not back up.
func TestIssueAndInstallPriorUnreadableUnderLock(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode 000 file")
	}
	a, _ := certTestApp(t, docker.StateRunning)
	requestedCert(t, a)
	before, _ := os.ReadFile(a.certs.CertPath(certDomain))
	key := a.certs.KeyPath(certDomain)
	t.Cleanup(func() { _ = os.Chmod(key, 0o600) })
	_, err := a.CertRenew(t.Context(), certDomain, true, hookPrompter{fn: func() {
		if err := os.Chmod(key, 0); err != nil {
			t.Fatal(err)
		}
	}})
	if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err = %v, want the permission failure", err)
	}
	if after, _ := os.ReadFile(a.certs.CertPath(certDomain)); !bytes.Equal(before, after) {
		t.Error("prior certificate overwritten")
	}
}

// When the push is rejected and putting the prior material back also fails,
// the error says so: the store may now hold material Caddy is not serving.
func TestCertRenewReportsFailedMaterialRestore(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	a, _ := certTestApp(t, docker.StateRunning)
	requestedCert(t, a)
	before, _ := state.Load(a.paths.StatePath)
	domainDir := filepath.Dir(a.certs.CertPath(certDomain))
	t.Cleanup(func() { _ = os.Chmod(domainDir, 0o700) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// Lock the store as the push is rejected, so the undo that
			// follows cannot write the prior material back.
			_ = os.Chmod(domainDir, 0o500)
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"stub rejection"}`))
	}))
	t.Cleanup(srv.Close)
	a.caddy = caddyapi.NewClient(srv.URL, time.Second)

	renewed, err := a.CertRenew(t.Context(), certDomain, true, &noopPrompter{})
	if err == nil || renewed {
		t.Fatalf("renewed=%v err=%v, want a failure", renewed, err)
	}
	if !strings.Contains(err.Error(), "additionally, restoring prior certificate material failed") {
		t.Errorf("err = %v, want the failed restore named", err)
	}
	after, _ := state.Load(a.paths.StatePath)
	if after.Certs[certDomain].IssuedAt != before.Certs[certDomain].IssuedAt {
		t.Error("state recorded the failed renewal")
	}
}

// TestCertRemoveFailures: CertRemove pushes the config without the
// certificate before it deletes anything, and records the removal last.
func TestCertRemoveFailures(t *testing.T) {
	cases := []struct {
		name   string
		inject func(t *testing.T, a *App)
		wantIs error
		// wantMaterial and wantRecord are whether the material and the
		// state record survive the failure.
		wantMaterial, wantRecord bool
	}{
		{
			name: "corrupt state",
			inject: func(t *testing.T, a *App) {
				if err := os.WriteFile(a.paths.StatePath, []byte("{not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantIs:       ErrStateCorrupt,
			wantMaterial: true,
		},
		{
			name: "another certificate unreadable",
			inject: func(t *testing.T, a *App) {
				if os.Geteuid() == 0 {
					t.Skip("root reads a mode 000 file")
				}
				const other = "beta.arrix.dev"
				if err := a.certs.Save(other, []byte("CERT"), []byte("KEY")); err != nil {
					t.Fatal(err)
				}
				key := a.certs.KeyPath(other)
				if err := os.Chmod(key, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(key, 0o600) })
				s, _ := state.Load(a.paths.StatePath)
				s.Certs[other] = state.Cert{Domain: other, Subjects: []string{other}}
				saveState(t, a.paths.StatePath, s)
			},
			wantMaterial: true,
			wantRecord:   true,
		},
		{
			name: "material delete refused",
			inject: func(t *testing.T, a *App) {
				readOnly(t, filepath.Dir(a.certs.CertPath(certDomain)))
			},
			wantMaterial: true,
			wantRecord:   true,
		},
		{
			name: "state write refused",
			inject: func(t *testing.T, a *App) {
				readOnlyStateDir(t, a.paths.StatePath)
			},
			wantRecord: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := certTestApp(t, docker.StateRunning)
			requestedCert(t, a)
			tc.inject(t, a)
			removed, err := a.CertRemove(t.Context(), certDomain)
			if err == nil || removed {
				t.Fatalf("removed=%v err=%v, want a failure", removed, err)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("err = %v, want %v", err, tc.wantIs)
			}
			if got := a.certs.Has(certDomain); got != tc.wantMaterial {
				t.Errorf("material present = %v, want %v", got, tc.wantMaterial)
			}
			if tc.wantIs == nil {
				s, lerr := state.Load(a.paths.StatePath)
				if lerr != nil {
					t.Fatal(lerr)
				}
				if _, got := s.Certs[certDomain]; got != tc.wantRecord {
					t.Errorf("state record present = %v, want %v", got, tc.wantRecord)
				}
			}
			if held, _ := state.LockHeld(a.paths.LockPath); held {
				t.Error("state lock leaked")
			}
		})
	}
}

// TestCertRenewRefusesBeforeOrdering: each of these is known before the
// order, so none may spend an issuance, and each maps to its exit code.
func TestCertRenewRefusesBeforeOrdering(t *testing.T) {
	cases := []struct {
		name     string
		inject   func(t *testing.T, a *App)
		force    bool
		wantIs   error
		wantExit int
	}{
		{
			name: "corrupt state",
			inject: func(t *testing.T, a *App) {
				if err := os.WriteFile(a.paths.StatePath, []byte("{not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantIs:   ErrStateCorrupt,
			wantExit: 7,
		},
		{
			name: "unreadable expiry",
			inject: func(t *testing.T, a *App) {
				s, _ := state.Load(a.paths.StatePath)
				c := s.Certs[certDomain]
				c.NotAfter = "next tuesday"
				s.Certs[certDomain] = c
				saveState(t, a.paths.StatePath, s)
			},
			wantIs:   ErrStateCorrupt,
			wantExit: 7,
		},
		{
			name: "inspect",
			inject: func(_ *testing.T, a *App) {
				a.docker.(*docker.Fake).ContainerStateErrs = map[string]error{CaddyContainerName: errInjected}
			},
			force:    true,
			wantIs:   errInjected,
			wantExit: 1,
		},
		{
			name: "caddy stopped",
			inject: func(_ *testing.T, a *App) {
				a.docker.(*docker.Fake).Containers[CaddyContainerName] = docker.StateStopped
			},
			force:    true,
			wantIs:   ErrCaddyNotRunning,
			wantExit: 5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, iss := certTestApp(t, docker.StateRunning)
			requestedCert(t, a)
			orders := len(iss.Calls)
			tc.inject(t, a)
			renewed, err := a.CertRenew(t.Context(), certDomain, tc.force, &noopPrompter{})
			if renewed || !errors.Is(err, tc.wantIs) || ExitCode(err) != tc.wantExit {
				t.Fatalf("renewed=%v err=%v (exit %d), want %v (exit %d)", renewed, err, ExitCode(err), tc.wantIs, tc.wantExit)
			}
			if len(iss.Calls) != orders {
				t.Error("an ACME order was placed")
			}
		})
	}
}

// A record written before env and email were stored renews against staging
// (never silently against production) with a placeholder contact.
func TestCertRenewLegacyRecordFallsBack(t *testing.T) {
	a, iss := certTestApp(t, docker.StateRunning)
	requestedCert(t, a)
	s, _ := state.Load(a.paths.StatePath)
	c := s.Certs[certDomain]
	c.Env, c.Email = "", ""
	s.Certs[certDomain] = c
	saveState(t, a.paths.StatePath, s)

	if _, err := a.CertRenew(t.Context(), certDomain, true, &noopPrompter{}); err != nil {
		t.Fatal(err)
	}
	last := iss.Calls[len(iss.Calls)-1]
	if last.Env != acme.EnvStaging || last.Email != "renewal@"+certDomain {
		t.Errorf("env=%q email=%q, want staging and renewal@%s", last.Env, last.Email, certDomain)
	}
}

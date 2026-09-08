package app

import (
	"errors"
	"io"
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

// certTestApp builds an App wired to a fake docker, a fake issuer and a
// temporary state and cert directory.
//
// The caddy client is pointed at an httptest stub that always answers 200,
// never at the zero-value default (real client at 127.0.0.1:2019): a
// developer's own machine can have a real caddy_shack instance listening
// there, and these tests must never be able to push a config to it.
func certTestApp(t *testing.T, containerState docker.ContainerState) (*App, *acme.FakeIssuer) {
	t.Helper()
	dir := t.TempDir()
	fd := docker.NewFake()
	fd.Containers[CaddyContainerName] = containerState

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	a := NewWithPaths(Paths{
		StatePath:    filepath.Join(dir, "state.json"),
		LockPath:     filepath.Join(dir, "state.json.lock"),
		CertStoreDir: dir,
	}, caddyapi.NewClient(srv.URL, time.Second), fd)

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

// rejectPushes swaps a's caddy client for a stub that rejects every push
// with 400, for exercising rollback paths. The prior client's server (from
// certTestApp) is left running but unused; the new one is closed on cleanup.
func rejectPushes(t *testing.T, a *App) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"stub rejection"}`))
	}))
	t.Cleanup(srv.Close)
	a.caddy = caddyapi.NewClient(srv.URL, time.Second)
}

// TestCertRenewFailedPushRestoresOriginalMaterial pins the fix for the
// critical rollback bug: certstore.Save overwrites material in place with no
// backup, so a renewal that writes new material and then fails its config
// push must restore the ORIGINAL bytes, not just remove the new ones. A
// naive "roll back to nothing" here would delete a certificate that was
// still serving traffic seconds earlier.
func TestCertRenewFailedPushRestoresOriginalMaterial(t *testing.T) {
	a, _ := certTestApp(t, docker.StateRunning)
	if err := a.CertRequest(t.Context(), CertRequestInput{
		Domain: "alpha.arrix.dev", Email: "k@example.com", Production: true,
	}, &noopPrompter{}); err != nil {
		t.Fatalf("request: %v", err)
	}

	originalMaterial, err := a.certs.Load("alpha.arrix.dev")
	if err != nil {
		t.Fatalf("load original material: %v", err)
	}
	sBefore, err := state.Load(a.paths.StatePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	certBefore := sBefore.Certs["alpha.arrix.dev"]

	// The renewed order returns different material, so a wrongly-kept new
	// write is distinguishable from a correctly restored original.
	a.SetIssuer(acme.NewFakeIssuer(acme.Result{
		CertPEM:  []byte("-----BEGIN CERTIFICATE-----\nNEWCERT\n-----END CERTIFICATE-----\n"),
		KeyPEM:   []byte("-----BEGIN EC PRIVATE KEY-----\nNEWKEY\n-----END EC PRIVATE KEY-----\n"),
		NotAfter: time.Now().Add(90 * 24 * time.Hour),
	}, nil))
	rejectPushes(t, a)

	renewed, err := a.CertRenew(t.Context(), "alpha.arrix.dev", true, &noopPrompter{})
	if err == nil {
		t.Fatal("expected an error from the rejected push")
	}
	if renewed {
		t.Error("renewed = true, want false when the push failed")
	}

	afterMaterial, err := a.certs.Load("alpha.arrix.dev")
	if err != nil {
		t.Fatalf("load material after failed renewal: %v", err)
	}
	if string(afterMaterial.CertPEM) != string(originalMaterial.CertPEM) {
		t.Errorf("cert PEM after failed renewal = %q, want the original %q", afterMaterial.CertPEM, originalMaterial.CertPEM)
	}
	if string(afterMaterial.KeyPEM) != string(originalMaterial.KeyPEM) {
		t.Errorf("key PEM after failed renewal = %q, want the original %q", afterMaterial.KeyPEM, originalMaterial.KeyPEM)
	}

	sAfter, err := state.Load(a.paths.StatePath)
	if err != nil {
		t.Fatalf("load state after failed renewal: %v", err)
	}
	certAfter, ok := sAfter.Certs["alpha.arrix.dev"]
	if !ok {
		t.Fatal("state must still record the certificate")
	}
	if certAfter.Env != certBefore.Env || certAfter.Email != certBefore.Email ||
		certAfter.IssuedAt != certBefore.IssuedAt || certAfter.NotAfter != certBefore.NotAfter ||
		strings.Join(certAfter.Subjects, ",") != strings.Join(certBefore.Subjects, ",") {
		t.Errorf("state record changed after failed renewal: before=%+v after=%+v", certBefore, certAfter)
	}

	certs, err := a.loadedCerts(sAfter)
	if err != nil {
		t.Fatalf("loadedCerts: %v", err)
	}
	found := false
	for _, c := range certs {
		for _, subj := range c.Subjects {
			if subj == "alpha.arrix.dev" {
				found = true
			}
		}
	}
	if !found {
		t.Error("loadedCerts must still return the certificate after a failed renewal")
	}
}

// TestCertRequestFailedPushStillRemovesFreshMaterial guards against a
// regression from the rollback fix above: a FRESH request (no prior
// material) that fails its push must still end up with no material and no
// state record, exactly as before.
func TestCertRequestFailedPushStillRemovesFreshMaterial(t *testing.T) {
	a, _ := certTestApp(t, docker.StateRunning)
	rejectPushes(t, a)

	err := a.CertRequest(t.Context(), CertRequestInput{Domain: "alpha.arrix.dev", Email: "k@example.com"}, &noopPrompter{})
	if err == nil {
		t.Fatal("expected an error from the rejected push")
	}
	if a.certs.Has("alpha.arrix.dev") {
		t.Error("a fresh request whose push fails must leave no material behind")
	}
	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if _, present := s.Certs["alpha.arrix.dev"]; present {
		t.Error("a fresh request whose push fails must leave no state record")
	}
}

// TestCertRemoveFailedPushLeavesMaterialAndStateIntact pins the fix for the
// important CertRemove ordering bug: nothing must be destroyed until the
// push that reflects the removal has actually succeeded.
func TestCertRemoveFailedPushLeavesMaterialAndStateIntact(t *testing.T) {
	a, _ := certTestApp(t, docker.StateRunning)
	if err := a.CertRequest(t.Context(), CertRequestInput{Domain: "alpha.arrix.dev", Email: "k@example.com"}, &noopPrompter{}); err != nil {
		t.Fatalf("request: %v", err)
	}
	rejectPushes(t, a)

	removed, err := a.CertRemove(t.Context(), "alpha.arrix.dev")
	if err == nil {
		t.Fatal("expected an error from the rejected push")
	}
	if removed {
		t.Error("removed = true, want false when the push failed")
	}
	if !a.certs.Has("alpha.arrix.dev") {
		t.Error("a failed removal push must leave material intact")
	}
	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if _, present := s.Certs["alpha.arrix.dev"]; !present {
		t.Error("a failed removal push must leave the state record intact")
	}
}

// TestCertRemoveSuccessfulPushStillDeletesMaterialAndState guards the happy
// path against a regression from the CertRemove reordering: a successful
// push must still delete both.
func TestCertRemoveSuccessfulPushStillDeletesMaterialAndState(t *testing.T) {
	a, _ := certTestApp(t, docker.StateRunning)
	if err := a.CertRequest(t.Context(), CertRequestInput{Domain: "alpha.arrix.dev", Email: "k@example.com"}, &noopPrompter{}); err != nil {
		t.Fatalf("request: %v", err)
	}

	removed, err := a.CertRemove(t.Context(), "alpha.arrix.dev")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed {
		t.Error("removed = false, want true for a successful push")
	}
	if a.certs.Has("alpha.arrix.dev") {
		t.Error("a successful removal push must delete material")
	}
	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if _, present := s.Certs["alpha.arrix.dev"]; present {
		t.Error("a successful removal push must delete the state record")
	}
}

// recordingCaddyServer starts an httptest server that always answers 200 and
// records every POST body it receives, so a test can inspect what was last
// pushed to Caddy.
func recordingCaddyServer(t *testing.T) (*httptest.Server, *[][]byte) {
	t.Helper()
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			bodies = append(bodies, body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

// lockStateDirForWriting chmods the directory holding the state file to
// read+execute only, so state.Save's tmp-file-then-rename write fails while
// reads still succeed. Cleanup restores it so t.TempDir can remove it.
func lockStateDirForWriting(t *testing.T, statePath string) {
	t.Helper()
	dir := filepath.Dir(statePath)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod state dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// TestCertRenewStateSaveFailureRollsBackConfigAndMaterial pins the fix for
// the crash window between the Caddy push and state.Save in
// issueAndInstall: if the push lands but the state write fails, Caddy must
// not be left serving material that state.json has no record of. The
// rollback must re-push the pre-change config and restore the pre-change
// certificate material, and the returned error must mention both the save
// failure and the rollback outcome.
func TestCertRenewStateSaveFailureRollsBackConfigAndMaterial(t *testing.T) {
	a, _ := certTestApp(t, docker.StateRunning)
	if err := a.CertRequest(t.Context(), CertRequestInput{
		Domain: "alpha.arrix.dev", Email: "k@example.com",
	}, &noopPrompter{}); err != nil {
		t.Fatalf("request: %v", err)
	}

	originalMaterial, err := a.certs.Load("alpha.arrix.dev")
	if err != nil {
		t.Fatalf("load original material: %v", err)
	}
	sBefore, err := state.Load(a.paths.StatePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	certsBefore, err := a.loadedCerts(sBefore)
	if err != nil {
		t.Fatalf("loadedCerts: %v", err)
	}
	wantRollbackCfg := caddyapi.Build(sBefore, certsBefore)

	// Swap in a recording server so the rollback push can be inspected, then
	// make the renewed order return different material so a wrongly-kept
	// new write is distinguishable from a correctly restored original.
	srv, bodies := recordingCaddyServer(t)
	a.caddy = caddyapi.NewClient(srv.URL, time.Second)
	a.SetIssuer(acme.NewFakeIssuer(acme.Result{
		CertPEM:  []byte("-----BEGIN CERTIFICATE-----\nNEWCERT\n-----END CERTIFICATE-----\n"),
		KeyPEM:   []byte("-----BEGIN EC PRIVATE KEY-----\nNEWKEY\n-----END EC PRIVATE KEY-----\n"),
		NotAfter: time.Now().Add(90 * 24 * time.Hour),
	}, nil))

	lockStateDirForWriting(t, a.paths.StatePath)

	renewed, err := a.CertRenew(t.Context(), "alpha.arrix.dev", true, &noopPrompter{})
	if err == nil {
		t.Fatal("expected an error from the failed state.Save")
	}
	if renewed {
		t.Error("renewed = true, want false when state.Save failed")
	}
	if !strings.Contains(err.Error(), "save state") {
		t.Errorf("error must mention the save failure, got %q", err)
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("error must say the rollback succeeded, got %q", err)
	}

	// The last thing pushed to Caddy must be the pre-change config, not one
	// containing the new, unrecorded material.
	if len(*bodies) == 0 {
		t.Fatal("expected the rollback to re-push a config")
	}
	lastPush := (*bodies)[len(*bodies)-1]
	if string(lastPush) != string(wantRollbackCfg) {
		t.Errorf("rollback push = %s, want the pre-change config %s", lastPush, wantRollbackCfg)
	}

	// The store must hold the original material again, not the new order's.
	afterMaterial, err := a.certs.Load("alpha.arrix.dev")
	if err != nil {
		t.Fatalf("load material after rollback: %v", err)
	}
	if string(afterMaterial.CertPEM) != string(originalMaterial.CertPEM) {
		t.Errorf("cert PEM after rollback = %q, want the original %q", afterMaterial.CertPEM, originalMaterial.CertPEM)
	}
	if string(afterMaterial.KeyPEM) != string(originalMaterial.KeyPEM) {
		t.Errorf("key PEM after rollback = %q, want the original %q", afterMaterial.KeyPEM, originalMaterial.KeyPEM)
	}
}

// TestCertRequestLowercasesDomain pins the fix for rejecting mixed-case
// domains: DNS names are case-insensitive, so "Alpha.arrix.dev" must be
// accepted and normalised to lower case throughout, matching what
// downstream state and the cert store see.
func TestCertRequestLowercasesDomain(t *testing.T) {
	a, iss := certTestApp(t, docker.StateRunning)
	if err := a.CertRequest(t.Context(), CertRequestInput{
		Domain: "Alpha.arrix.dev", Email: "k@example.com",
	}, &noopPrompter{}); err != nil {
		t.Fatalf("request: %v", err)
	}

	if len(iss.Calls) != 1 {
		t.Fatalf("expected one order, got %d", len(iss.Calls))
	}
	got := iss.Calls[0].Domains
	if len(got) != 2 || got[0] != "alpha.arrix.dev" || got[1] != "*.alpha.arrix.dev" {
		t.Errorf("domains ordered = %v, want lower-cased [alpha.arrix.dev *.alpha.arrix.dev]", got)
	}

	if !a.certs.Has("alpha.arrix.dev") {
		t.Error("certificate material must be stored under the lower-cased domain")
	}

	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	c, ok := s.Certs["alpha.arrix.dev"]
	if !ok {
		t.Fatal("state must record the certificate under the lower-cased domain")
	}
	if c.Domain != "alpha.arrix.dev" {
		t.Errorf("stored domain = %q, want lower-cased", c.Domain)
	}
}

// TestCertRenewAndRemoveAgreeWithCertRequestOnCase pins the fix for a
// regression the lower-casing fix introduced: CertRequest normalises the
// domain before storing, but CertRenew and CertRemove looked it up with
// whatever case was passed in, verbatim. A request for "Alpha.Arrix.dev"
// then stored under "alpha.arrix.dev", so a renew or remove issued with the
// SAME string the operator had just typed to request it could no longer
// find the record: renew failed with a message telling the operator to run
// the exact request command they had already run, and remove silently
// no-opped (exit 0, nothing removed).
//
// Covers both directions: the exact mixed-case string as typed, and the
// already-lower-cased form, must both find the record CertRequest created.
func TestCertRenewAndRemoveAgreeWithCertRequestOnCase(t *testing.T) {
	for _, lookup := range []string{"Alpha.Arrix.dev", "alpha.arrix.dev"} {
		t.Run(lookup, func(t *testing.T) {
			a, _ := certTestApp(t, docker.StateRunning)
			if err := a.CertRequest(t.Context(), CertRequestInput{
				Domain: "Alpha.Arrix.dev", Email: "k@example.com",
			}, &noopPrompter{}); err != nil {
				t.Fatalf("request: %v", err)
			}

			renewed, err := a.CertRenew(t.Context(), lookup, true, &noopPrompter{})
			if err != nil {
				t.Fatalf("renew %q: %v", lookup, err)
			}
			if !renewed {
				t.Errorf("renewed = false for %q, want true (forced)", lookup)
			}

			removed, err := a.CertRemove(t.Context(), lookup)
			if err != nil {
				t.Fatalf("remove %q: %v", lookup, err)
			}
			if !removed {
				t.Errorf("removed = false for %q, want true; the record must not silently no-op", lookup)
			}
			if a.certs.Has("alpha.arrix.dev") {
				t.Error("material must be gone from the store after removal")
			}
		})
	}
}

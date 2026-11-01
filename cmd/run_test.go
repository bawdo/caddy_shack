package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/acme"
	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
)

// These tests drive the real command tree against a faked install (see
// harness_test.go). They swap the package-level newApp, so none of them may
// call t.Parallel.

func TestInstallCreatesAndStartsCaddy(t *testing.T) {
	e := newTestEnv(t)
	r := e.run(t, "", "install")
	wantCode(t, r, 0, nil)
	if got := e.dock.Containers[app.CaddyContainerName]; got != docker.StateRunning {
		t.Errorf("caddy container state = %v, want running", got)
	}
	if !e.dock.PulledImg[app.CaddyImage] {
		t.Errorf("install must pull %s", app.CaddyImage)
	}
	for _, v := range []string{app.CaddyDataVolume, app.CaddyConfigVolume} {
		if !e.dock.Volumes[v] {
			t.Errorf("install must create volume %s", v)
		}
	}
}

func TestInstallReportsDockerDown(t *testing.T) {
	e := newTestEnv(t)
	e.dock.DaemonErr = docker.ErrDaemonUnreachable
	r := e.run(t, "", "install")
	wantCode(t, r, 4, app.ErrDockerUnreachable)
	if _, ok := e.dock.Containers[app.CaddyContainerName]; ok {
		t.Error("nothing may be created while the daemon is down")
	}
}

func TestStopThenStartRestoresRoutes(t *testing.T) {
	e := newInstalledEnv(t)
	e.mustRun(t, "", "register", "myapp", "--port", "3000")

	wantCode(t, e.run(t, "", "stop"), 0, nil)
	if got := e.dock.Containers[app.CaddyContainerName]; got != docker.StateStopped {
		t.Fatalf("after stop the container is %v, want stopped", got)
	}

	// Caddy came back without its routes; start must put them back.
	e.caddy.wipe()
	wantCode(t, e.run(t, "", "start"), 0, nil)
	if got := e.dock.Containers[app.CaddyContainerName]; got != docker.StateRunning {
		t.Errorf("after start the container is %v, want running", got)
	}
	if hosts := e.caddy.liveHosts(t); !slices.Contains(hosts, "myapp.test") {
		t.Errorf("start must re-push the registry; live hosts = %v", hosts)
	}
}

func TestStartWithoutInstallIsNotInstalled(t *testing.T) {
	e := newTestEnv(t)
	r := e.run(t, "", "start")
	wantCode(t, r, 6, app.ErrNotInstalled)
	if !strings.Contains(r.stderr, "caddy_shack install") {
		t.Errorf("stderr should point at install, got:\n%s", r.stderr)
	}
}

func TestRestartRestoresLostRoutes(t *testing.T) {
	e := newInstalledEnv(t)
	e.mustRun(t, "", "register", "myapp", "--port", "3000")
	e.caddy.wipe()

	wantCode(t, e.run(t, "", "restart"), 0, nil)
	if got := e.dock.Containers[app.CaddyContainerName]; got != docker.StateRunning {
		t.Errorf("after restart the container is %v, want running", got)
	}
	if hosts := e.caddy.liveHosts(t); !slices.Contains(hosts, "myapp.test") {
		t.Errorf("restart must re-push the registry; live hosts = %v", hosts)
	}
}

func TestRegisterThenListShowsLiveBackend(t *testing.T) {
	e := newInstalledEnv(t)
	wantCode(t, e.run(t, "", "register", "myapp", "--port", "3000"), 0, nil)

	r := e.mustRun(t, "", "list")
	if !strings.Contains(r.stdout, "myapp.test") || !strings.Contains(r.stdout, "host.docker.internal:3000") {
		t.Errorf("list should show the backend, got:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "not live") {
		t.Errorf("a freshly registered backend is live, got:\n%s", r.stdout)
	}

	r = e.mustRun(t, "", "list", "--json")
	var rows []app.Listing
	if err := json.Unmarshal([]byte(r.stdout), &rows); err != nil {
		t.Fatalf("list --json is not JSON: %v\n%s", err, r.stdout)
	}
	if len(rows) != 1 || rows[0].Name != "myapp" || rows[0].Live != app.LiveYes {
		t.Errorf("list --json rows = %+v, want one live myapp", rows)
	}
}

func TestListFlagsBackendsCaddyLost(t *testing.T) {
	e := newInstalledEnv(t)
	e.mustRun(t, "", "register", "myapp", "--port", "3000")
	e.caddy.wipe()

	r := e.mustRun(t, "", "list")
	if !strings.Contains(r.stdout, "1 backend(s) registered but not live") {
		t.Errorf("list should flag the lost route, got:\n%s", r.stdout)
	}
}

func TestRegisterRejectedByCaddyLeavesRegistryAlone(t *testing.T) {
	e := newInstalledEnv(t)
	e.caddy.rejectLoad = 400

	r := e.run(t, "", "register", "myapp", "--port", "3000")
	wantCode(t, r, 1, nil)
	var rej *caddyapi.ErrRejected
	if !errors.As(r.err, &rej) {
		t.Errorf("err = %v, want Caddy's rejection passed through", r.err)
	}
	if _, ok := e.loadState(t).Backends["myapp"]; ok {
		t.Error("a config Caddy rejected must not reach state.json")
	}
}

func TestDeregisterRemovesBackend(t *testing.T) {
	e := newInstalledEnv(t)
	e.mustRun(t, "", "register", "myapp", "--port", "3000")

	r := e.run(t, "", "deregister", "myapp")
	wantCode(t, r, 0, nil)
	if !strings.Contains(r.stdout, "deregistered: myapp") {
		t.Errorf("stdout = %q, want the removal reported", r.stdout)
	}
	if _, ok := e.loadState(t).Backends["myapp"]; ok {
		t.Error("myapp is still in the registry")
	}
	if hosts := e.caddy.liveHosts(t); slices.Contains(hosts, "myapp.test") {
		t.Errorf("Caddy still serves myapp.test: %v", hosts)
	}
}

func TestDeregisterUnknownIsIdempotent(t *testing.T) {
	e := newInstalledEnv(t)
	r := e.run(t, "", "deregister", "ghost")
	wantCode(t, r, 0, nil)
	if r.stdout != "" {
		t.Errorf("nothing was removed, so stdout must be empty, got %q", r.stdout)
	}
	if !strings.Contains(r.stderr, "no such backend: ghost") {
		t.Errorf("stderr = %q, want a not-found notice", r.stderr)
	}
}

func TestDeregisterCascadeRemovesSubdomains(t *testing.T) {
	e := newInstalledEnv(t)
	e.mustRun(t, "", "register", "foo", "--port", "3000")
	e.mustRun(t, "", "register", "bar.foo", "--port", "3001")

	r := e.mustRun(t, "", "deregister", "foo")
	if !strings.Contains(r.stderr, "1 subdomain backend(s) still registered under foo.test") {
		t.Errorf("without --cascade the child must be reported, stderr:\n%s", r.stderr)
	}
	if _, ok := e.loadState(t).Backends["bar.foo"]; !ok {
		t.Fatal("without --cascade bar.foo must stay registered")
	}

	e.mustRun(t, "", "register", "foo", "--port", "3000")
	r = e.mustRun(t, "", "deregister", "foo", "--cascade")
	for _, want := range []string{"deregistered: bar.foo", "deregistered: foo"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout should contain %q, got:\n%s", want, r.stdout)
		}
	}
	if n := len(e.loadState(t).Backends); n != 0 {
		t.Errorf("--cascade left %d backend(s)", n)
	}
}

func TestStatusHealthy(t *testing.T) {
	e := newInstalledEnv(t)
	e.mustRun(t, "", "register", "myapp", "--port", "3000")

	r := e.run(t, "", "status")
	wantCode(t, r, 0, nil)
	for _, want := range []string{"container:       running", "backends:        1", "config in sync:  true", "healthy:         true"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("status should contain %q, got:\n%s", want, r.stdout)
		}
	}

	r = e.run(t, "", "status", "--json")
	wantCode(t, r, 0, nil)
	var rep app.StatusReport
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		t.Fatalf("status --json is not JSON: %v\n%s", err, r.stdout)
	}
	if !rep.Healthy || rep.BackendCount != 1 || rep.Drift == nil || !rep.Drift.InSync {
		t.Errorf("status --json = %+v, want healthy, one backend, in sync", rep)
	}
}

func TestStatusReportsDriftThenSyncFixesIt(t *testing.T) {
	e := newInstalledEnv(t)
	e.mustRun(t, "", "register", "myapp", "--port", "3000")
	e.caddy.wipe()

	r := e.run(t, "", "status")
	wantCode(t, r, 9, app.ErrDrift)
	if !strings.Contains(r.stdout, "config in sync:  false") || !strings.Contains(r.stdout, "run 'caddy_shack sync'") {
		t.Errorf("status should show the drift and the fix, got:\n%s", r.stdout)
	}

	r = e.run(t, "", "sync")
	wantCode(t, r, 0, nil)
	if !strings.Contains(r.stdout, "drift:") || !strings.Contains(r.stdout, "synced: re-pushed the registry to Caddy") {
		t.Errorf("sync should report what it fixed, got:\n%s", r.stdout)
	}
	if hosts := e.caddy.liveHosts(t); !slices.Contains(hosts, "myapp.test") {
		t.Errorf("sync must restore the route; live hosts = %v", hosts)
	}

	wantCode(t, e.run(t, "", "status"), 0, nil)
}

func TestStatusUnhealthyWhenStopped(t *testing.T) {
	e := newInstalledEnv(t)
	e.mustRun(t, "", "stop")

	r := e.run(t, "", "status")
	wantCode(t, r, 1, app.ErrUnhealthy)
	if !strings.Contains(r.stdout, "container:       stopped") || !strings.Contains(r.stdout, "healthy:         false") {
		t.Errorf("status must still print the report when unhealthy, got:\n%s", r.stdout)
	}
}

func TestSyncInSyncIsNoop(t *testing.T) {
	e := newInstalledEnv(t)
	e.mustRun(t, "", "register", "myapp", "--port", "3000")
	loads := e.caddy.loadCount()

	r := e.mustRun(t, "", "sync")
	if !strings.Contains(r.stdout, "in sync: live Caddy config matches the registry") {
		t.Errorf("stdout = %q, want the in-sync message", r.stdout)
	}
	if got := e.caddy.loadCount(); got != loads {
		t.Errorf("an in-sync sync pushed to Caddy (%d loads, was %d)", got, loads)
	}

	r = e.mustRun(t, "", "sync", "--json")
	var rep app.SyncReport
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		t.Fatalf("sync --json is not JSON: %v\n%s", err, r.stdout)
	}
	if rep.Pushed || !rep.Before.InSync {
		t.Errorf("sync --json = %+v, want in sync and not pushed", rep)
	}
}

func TestSyncWhenStoppedIsNotRunning(t *testing.T) {
	e := newInstalledEnv(t)
	e.mustRun(t, "", "stop")
	r := e.run(t, "", "sync")
	wantCode(t, r, 5, app.ErrCaddyNotRunning)
	if r.stdout != "" {
		t.Errorf("a failed sync must print no report, got %q", r.stdout)
	}
}

func TestUpgradeAlreadyCurrent(t *testing.T) {
	e := newInstalledEnv(t)
	e.dock.ImageIDs[app.CaddyImage] = "sha256:same"
	e.dock.ContainerImageIDs[app.CaddyContainerName] = "sha256:same"
	e.dock.ExecResponses["caddy_shack:caddy version"] = docker.ExecResponse{Stdout: []byte("v2.8.4 h1:abc=")}

	r := e.run(t, "", "upgrade")
	wantCode(t, r, 0, nil)
	if r.stdout != "already up to date (v2.8.4)\n" {
		t.Errorf("stdout = %q", r.stdout)
	}
	if len(e.dock.Removed) != 0 {
		t.Errorf("an up-to-date upgrade must not touch the container, removed %v", e.dock.Removed)
	}
}

func TestUpgradeRecreatesAndKeepsBackends(t *testing.T) {
	e := newInstalledEnv(t)
	e.mustRun(t, "", "register", "myapp", "--port", "3000")
	e.dock.ImageIDs[app.CaddyImage] = "sha256:new"
	e.dock.ContainerImageIDs[app.CaddyContainerName] = "sha256:old"
	e.dock.ExecResponses["caddy_shack:caddy version"] = docker.ExecResponse{Stdout: []byte("v2.9.0 h1:abc=")}
	e.caddy.wipe() // the new container boots without routes

	r := e.run(t, "", "upgrade")
	wantCode(t, r, 0, nil)
	if r.stdout != "upgraded to v2.9.0\n" {
		t.Errorf("stdout = %q", r.stdout)
	}
	if !slices.Contains(e.dock.Removed, app.CaddyContainerName) {
		t.Errorf("upgrade must recreate the container, removed %v", e.dock.Removed)
	}
	if hosts := e.caddy.liveHosts(t); !slices.Contains(hosts, "myapp.test") {
		t.Errorf("upgrade must restore the registry; live hosts = %v", hosts)
	}
}

func TestUpgradeWithoutInstallIsNotInstalled(t *testing.T) {
	e := newTestEnv(t)
	r := e.run(t, "", "upgrade")
	wantCode(t, r, 6, app.ErrNotInstalled)
	if r.stdout != "" {
		t.Errorf("a failed upgrade must print no outcome, got %q", r.stdout)
	}
}

func TestCAPrintsPEMOnly(t *testing.T) {
	e := newInstalledEnv(t)
	root := "-----BEGIN CERTIFICATE-----\nROOT\n-----END CERTIFICATE-----"
	inter := "-----BEGIN CERTIFICATE-----\nINTER\n-----END CERTIFICATE-----"
	e.caddy.ca = &caddyapi.CAInfo{RootCertificate: root, IntermediateCertificate: inter}

	r := e.run(t, "", "ca")
	wantCode(t, r, 0, nil)
	if r.stdout != root+"\n" {
		t.Errorf("ca stdout = %q, want the root PEM and nothing else", r.stdout)
	}

	r = e.run(t, "", "export", "--intermediate")
	wantCode(t, r, 0, nil)
	if r.stdout != root+"\n"+inter+"\n" {
		t.Errorf("export --intermediate stdout = %q, want root then intermediate", r.stdout)
	}
}

func TestCAWhenStoppedIsNotRunning(t *testing.T) {
	e := newInstalledEnv(t)
	e.mustRun(t, "", "stop")
	r := e.run(t, "", "ca")
	wantCode(t, r, 5, app.ErrCaddyNotRunning)
	if r.stdout != "" {
		t.Errorf("stdout must stay empty on failure so a redirect gets no junk, got %q", r.stdout)
	}
}

func TestLANEnablePromptAccepted(t *testing.T) {
	e := newInstalledEnv(t)

	r := e.run(t, "y\n", "lan", "enable", "--suffix", "mac.home.arpa")
	wantCode(t, r, 0, nil)
	for _, want := range []string{
		"Bind LAN access to 192.168.1.20 (discovered)? [y/N]: ",
		"LAN mode enabled on 192.168.1.20 (discovered), suffix mac.home.arpa",
		"Point a DNS A record for *.mac.home.arpa",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout should contain %q, got:\n%s", want, r.stdout)
		}
	}
	if s := e.loadState(t); !s.LANEnabled() || s.LAN.Suffix != "mac.home.arpa" {
		t.Errorf("LAN state = %+v, want enabled with the suffix", s.LAN)
	}
	if got := e.dock.Containers[app.LANContainerName]; got != docker.StateRunning {
		t.Errorf("forwarder state = %v, want running", got)
	}
}

func TestLANEnablePromptDeclinedChangesNothing(t *testing.T) {
	for name, answer := range map[string]string{"no": "n\n", "default": "\n", "closed stdin": ""} {
		t.Run(name, func(t *testing.T) {
			e := newInstalledEnv(t)
			r := e.run(t, answer, "lan", "enable", "--suffix", "mac.home.arpa")
			wantCode(t, r, 1, nil)
			if !strings.Contains(r.err.Error(), "cancelled") {
				t.Errorf("err = %v, want cancelled", r.err)
			}
			if strings.Contains(r.stdout, "LAN mode enabled") {
				t.Errorf("declined enable must not claim success:\n%s", r.stdout)
			}
			if e.loadState(t).LAN != nil {
				t.Error("declined enable must not write LAN settings")
			}
			if _, ok := e.dock.Containers[app.LANContainerName]; ok {
				t.Error("declined enable must not create the forwarder")
			}
		})
	}
}

func TestLANEnableYesSkipsPrompt(t *testing.T) {
	e := newInstalledEnv(t)
	r := e.run(t, "", "lan", "enable", "--suffix", "mac.home.arpa", "--ip", "192.168.1.20", "--yes")
	wantCode(t, r, 0, nil)
	if strings.Contains(r.stdout, "[y/N]") {
		t.Errorf("--yes must not prompt:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "(pinned)") {
		t.Errorf("--ip pins the address, got:\n%s", r.stdout)
	}
}

func TestLANEnableRejectsTestSuffix(t *testing.T) {
	e := newInstalledEnv(t)
	r := e.run(t, "", "lan", "enable", "--suffix", "lan.test", "--yes")
	wantCode(t, r, 2, app.ErrInvalidArgs)
}

func TestLANDisable(t *testing.T) {
	e := newInstalledEnv(t)
	e.mustRun(t, "", "lan", "enable", "--suffix", "mac.home.arpa", "--yes")

	r := e.run(t, "", "lan", "disable")
	wantCode(t, r, 0, nil)
	if r.stdout != "LAN mode disabled\n" {
		t.Errorf("stdout = %q", r.stdout)
	}
	s := e.loadState(t)
	if s.LANEnabled() {
		t.Error("LAN is still enabled in state")
	}
	if s.LAN == nil || s.LAN.Suffix != "mac.home.arpa" {
		t.Errorf("disable must keep the settings for the next enable, got %+v", s.LAN)
	}
	if _, ok := e.dock.Containers[app.LANContainerName]; ok {
		t.Error("disable must remove the forwarder")
	}
}

func TestLANDisablePropagatesCorruptState(t *testing.T) {
	e := newInstalledEnv(t)
	writeCorruptState(t, e)
	r := e.run(t, "", "lan", "disable")
	wantCode(t, r, 7, app.ErrStateCorrupt)
	if strings.Contains(r.stdout, "LAN mode disabled") {
		t.Errorf("a failed disable must not claim success:\n%s", r.stdout)
	}
}

func TestCertRequestStagingByDefault(t *testing.T) {
	e := newInstalledEnv(t)

	r := e.run(t, "\n", "cert", "request", "alpha.arrix.dev", "--email", "k@example.com")
	wantCode(t, r, 0, nil)
	for _, want := range []string{
		"Let's Encrypt staging environment",
		"_acme-challenge.alpha.arrix.dev  60  IN  TXT",
		"Press Enter once the records are saved",
		"Certificate installed for alpha.arrix.dev and *.alpha.arrix.dev",
	} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr should contain %q, got:\n%s", want, r.stderr)
		}
	}
	if r.stdout != "" {
		t.Errorf("cert request writes only to stderr, stdout = %q", r.stdout)
	}
	if len(e.issuer.Calls) != 1 || e.issuer.Calls[0].Env != acme.EnvStaging {
		t.Fatalf("issuer calls = %+v, want one staging order", e.issuer.Calls)
	}
	if c, ok := e.loadState(t).Certs["alpha.arrix.dev"]; !ok || c.Env != acme.EnvStaging {
		t.Errorf("state cert = %+v, want a staging record", c)
	}
}

func TestCertRequestProductionSkipsStagingWarning(t *testing.T) {
	e := newInstalledEnv(t)
	r := e.run(t, "\n", "cert", "request", "alpha.arrix.dev", "--email", "k@example.com", "--production")
	wantCode(t, r, 0, nil)
	if strings.Contains(r.stderr, "staging") {
		t.Errorf("--production must not print the staging warning:\n%s", r.stderr)
	}
	if len(e.issuer.Calls) != 1 || e.issuer.Calls[0].Env != acme.EnvProduction {
		t.Errorf("issuer calls = %+v, want one production order", e.issuer.Calls)
	}
}

func TestCertRequestUnconfirmedPromptFailsIssuance(t *testing.T) {
	e := newInstalledEnv(t)
	// stdin closes before the operator presses Enter.
	r := e.run(t, "", "cert", "request", "alpha.arrix.dev", "--email", "k@example.com")
	wantCode(t, r, 11, app.ErrCertIssuance)
	if strings.Contains(r.stderr, "Certificate installed") {
		t.Errorf("a failed request must not claim success:\n%s", r.stderr)
	}
	if n := len(e.loadState(t).Certs); n != 0 {
		t.Errorf("a failed request must not record a certificate, got %d", n)
	}
}

func TestCertListAfterRequest(t *testing.T) {
	e := newInstalledEnv(t)

	r := e.mustRun(t, "", "cert", "list")
	if r.stdout != "no certificates\n" {
		t.Errorf("empty cert list stdout = %q", r.stdout)
	}
	r = e.mustRun(t, "", "cert", "list", "--json")
	if strings.TrimSpace(r.stdout) != "[]" {
		t.Errorf("empty cert list --json must be [], got %q", r.stdout)
	}

	e.mustRun(t, "\n", "cert", "request", "alpha.arrix.dev", "--email", "k@example.com")

	r = e.mustRun(t, "", "cert", "list")
	for _, want := range []string{"DOMAIN", "alpha.arrix.dev", "staging", "*.alpha.arrix.dev"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("cert list should contain %q, got:\n%s", want, r.stdout)
		}
	}
	r = e.mustRun(t, "", "cert", "list", "--json")
	var rows []app.CertListing
	if err := json.Unmarshal([]byte(r.stdout), &rows); err != nil {
		t.Fatalf("cert list --json is not JSON: %v\n%s", err, r.stdout)
	}
	if len(rows) != 1 || rows[0].Domain != "alpha.arrix.dev" || rows[0].DaysRemaining < 88 {
		t.Errorf("cert list --json = %+v, want one fresh alpha.arrix.dev", rows)
	}
}

func TestCertRenew(t *testing.T) {
	e := newInstalledEnv(t)
	e.mustRun(t, "\n", "cert", "request", "alpha.arrix.dev", "--email", "k@example.com")

	r := e.run(t, "", "cert", "renew", "alpha.arrix.dev")
	wantCode(t, r, 0, nil)
	if !strings.Contains(r.stderr, "alpha.arrix.dev is not due for renewal. Use --force") {
		t.Errorf("a fresh certificate is not due, stderr:\n%s", r.stderr)
	}
	if len(e.issuer.Calls) != 1 {
		t.Errorf("renew of a fresh certificate placed an order (%d calls)", len(e.issuer.Calls))
	}

	r = e.run(t, "\n", "cert", "renew", "alpha.arrix.dev", "--force")
	wantCode(t, r, 0, nil)
	if !strings.Contains(r.stderr, "Renewed alpha.arrix.dev.") {
		t.Errorf("stderr should confirm the renewal:\n%s", r.stderr)
	}
	if len(e.issuer.Calls) != 2 || e.issuer.Calls[1].Env != acme.EnvStaging {
		t.Errorf("--force must place a second order in the original env, calls = %+v", e.issuer.Calls)
	}
}

func TestCertRenewUnknownDomainIsInvalidArgs(t *testing.T) {
	e := newInstalledEnv(t)
	r := e.run(t, "", "cert", "renew", "nope.arrix.dev")
	wantCode(t, r, 2, app.ErrInvalidArgs)
	if strings.Contains(r.stderr, "Renewed") {
		t.Errorf("stderr must not claim a renewal:\n%s", r.stderr)
	}
}

func TestCertRemove(t *testing.T) {
	e := newInstalledEnv(t)
	e.mustRun(t, "\n", "cert", "request", "alpha.arrix.dev", "--email", "k@example.com")

	r := e.run(t, "", "cert", "remove", "alpha.arrix.dev")
	wantCode(t, r, 0, nil)
	if !strings.Contains(r.stderr, "Removed the certificate for alpha.arrix.dev.") {
		t.Errorf("stderr:\n%s", r.stderr)
	}
	if n := len(e.loadState(t).Certs); n != 0 {
		t.Errorf("remove left %d certificate(s) in state", n)
	}

	r = e.run(t, "", "cert", "remove", "alpha.arrix.dev")
	wantCode(t, r, 0, nil)
	if !strings.Contains(r.stderr, "no certificate for alpha.arrix.dev") {
		t.Errorf("a second remove is a no-op with a note, stderr:\n%s", r.stderr)
	}
}

// TestDoctorCancelledRendersPartialReport drives doctor with a context that
// is already cancelled, so no check runs (doctor's checks shell out to the
// host and are covered in the app package). The command must still render
// what it has in the requested format and propagate the cancellation.
func TestDoctorCancelledRendersPartialReport(t *testing.T) {
	newTestEnv(t)
	root := newRootCmd()
	var out, errOut strings.Builder
	root.SetArgs([]string{"doctor", "--json"})
	root.SetOut(&out)
	root.SetErr(&errOut)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := execute(ctx, root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if code := app.ExitCode(err); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	// app.Severity marshals as a string and has no decoder, so read the
	// fields this test cares about into plain types.
	var rep struct {
		SchemaVersion int               `json:"schema_version"`
		Overall       string            `json:"overall"`
		Interrupted   bool              `json:"interrupted"`
		Checks        []json.RawMessage `json:"checks"`
	}
	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil {
		t.Fatalf("doctor --json must stay valid JSON when cancelled: %v\n%s", err, out.String())
	}
	if rep.SchemaVersion != 1 || len(rep.Checks) != 0 {
		t.Errorf("report = %+v, want schema 1 and no checks", rep)
	}
	if !rep.Interrupted || rep.Overall != "fail" {
		t.Errorf("cancelled report = %+v, want interrupted and overall fail", rep)
	}
}

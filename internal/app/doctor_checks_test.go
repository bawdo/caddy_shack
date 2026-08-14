package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

func TestCheckDockerDaemonPass(t *testing.T) {
	fake := docker.NewFake()
	a := NewWithPaths(Paths{}, nil, fake)
	r := checkDockerDaemon(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	if r.Fix != nil {
		t.Errorf("docker-daemon has no fix")
	}
}

func TestCheckDockerDaemonFail(t *testing.T) {
	fake := docker.NewFake()
	fake.DaemonErr = docker.ErrDaemonUnreachable
	a := NewWithPaths(Paths{}, nil, fake)
	r := checkDockerDaemon(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Name != "docker-daemon" {
		t.Errorf("name: %q", r.Name)
	}
}

// listenLoopback grabs a free loopback port the test holds open until cleanup.
// Returns the port number for the caller to feed back into the check.
func listenLoopback(t *testing.T) (int, *net.TCPListener) {
	t.Helper()
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().(*net.TCPAddr).Port, l
}

func TestCheckPortAvailabilityFailWhenPortHeld(t *testing.T) {
	heldPort, _ := listenLoopback(t)
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateAbsent
	a := NewWithPaths(Paths{}, nil, fake)

	r := checkPortAvailabilityOn(context.Background(), a, []int{heldPort})
	if r.Status != SeverityFail {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	// Detail should mention the port number and the "is in use" message.
	portStr := fmt.Sprintf("%d", heldPort)
	foundPort := false
	foundMsg := false
	for _, d := range r.Detail {
		if contains(d, portStr) {
			foundPort = true
		}
		if contains(d, "is in use") {
			foundMsg = true
		}
	}
	if !foundPort {
		t.Errorf("expected detail to mention port %d; got %v", heldPort, r.Detail)
	}
	if !foundMsg {
		t.Errorf("expected detail to contain 'is in use'; got %v", r.Detail)
	}
}

func TestCheckPortAvailabilityPassWhenAllFreeInjected(t *testing.T) {
	// Use injected freePort set so the test does not race against the OS.
	p, l := listenLoopback(t)
	_ = l.Close() // free it for the probe
	a := NewWithPaths(Paths{}, nil, docker.NewFake())

	r := checkPortAvailabilityOn(context.Background(), a, []int{p})
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestCheckPortAvailabilityPassWhenContainerRunningHoldsPorts(t *testing.T) {
	heldPort, _ := listenLoopback(t)
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{}, nil, fake)

	r := checkPortAvailabilityOn(context.Background(), a, []int{heldPort})
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && (len(haystack) >= len(needle)) && (indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func newDoctorApp(t *testing.T, fake *docker.Fake, caddyURL string) *App {
	t.Helper()
	dir := t.TempDir()
	return NewWithPaths(Paths{
		StatePath: dir + "/state.json",
		LockPath:  dir + "/state.json.lock",
	}, caddyapi.NewClient(caddyURL, time.Second), fake)
}

func TestCheckContainerHealthPassWhenRunningAndReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := newDoctorApp(t, fake, srv.URL)

	r := checkContainerHealth(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	if r.Fix != nil {
		t.Errorf("no fix expected when healthy")
	}
}

func TestCheckContainerHealthFailWhenAbsentOffersInstallFix(t *testing.T) {
	fake := docker.NewFake()
	// container absent
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	r := checkContainerHealth(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Fix == nil {
		t.Fatal("expected install fix")
	}
	if r.Fix.RequiresSudo {
		t.Errorf("install fix must not require sudo")
	}
	// Invoking the fix should call Install via the fake docker.
	if err := r.Fix.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if state := fake.Containers[CaddyContainerName]; state != docker.StateRunning {
		t.Errorf("container state after install fix: %v", state)
	}
}

func TestCheckContainerHealthFailWhenStoppedOffersStartFix(t *testing.T) {
	// checkContainerHealth itself runs against an unreachable admin API (the
	// container is stopped, so nothing is listening yet), but applying the
	// fix starts the container and then restores backends, which needs the
	// admin API to actually come up.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateStopped
	a := newDoctorApp(t, fake, srv.URL)

	r := checkContainerHealth(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Fix == nil {
		t.Fatal("expected start fix")
	}
	if err := r.Fix.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if state := fake.Containers[CaddyContainerName]; state != docker.StateRunning {
		t.Errorf("state after start fix: %v", state)
	}
}

func TestCheckContainerHealthSkipWhenDaemonDown(t *testing.T) {
	fake := docker.NewFake()
	fake.DaemonErr = docker.ErrDaemonUnreachable
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	r := checkContainerHealth(context.Background(), a)
	if r.Status != SeveritySkip {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}

func TestCheckDNSDarwinPassWhenDscacheReturnsLoopback(t *testing.T) {
	a := newDoctorApp(t, docker.NewFake(), "http://127.0.0.1:1")

	// The check should call dscacheutil first; if it returns 127.0.0.1 it
	// passes without invoking any sub-probes.
	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "dscacheutil" {
			return []byte("name: anything.test\nip_address: 127.0.0.1\n"), nil
		}
		// Sub-probes only matter on failure; if invoked, return harmless
		// "found" responses.
		return []byte("ok"), nil
	}

	r := checkDNSDarwin(context.Background(), a, sh, noopShell)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}

func TestCheckDNSDarwinFailWhenDscacheReturnsNothing(t *testing.T) {
	a := newDoctorApp(t, docker.NewFake(), "http://127.0.0.1:1")

	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "dscacheutil":
			return []byte(""), nil
		case "brew":
			if len(args) > 0 && args[0] == "list" {
				return nil, errors.New("not installed")
			}
			return []byte("/opt/homebrew"), nil
		}
		return nil, errors.New("unexpected")
	}

	r := checkDNSDarwin(context.Background(), a, sh, noopShell)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Fix == nil {
		t.Fatal("DNS fail must offer fix")
	}
	if !r.Fix.RequiresSudo {
		t.Errorf("DNS fix requires sudo")
	}
	if strings.Contains(r.Fix.Label, "(requires sudo)") {
		t.Errorf("fix label should not embed '(requires sudo)': %q", r.Fix.Label)
	}
	joined := strings.Join(r.Detail, "\n")
	for _, want := range []string{
		"to fix manually:",
		"brew install dnsmasq",
		"echo 'address=/.test/127.0.0.1' > $(brew --prefix)/etc/dnsmasq.d/caddy_shack.conf",
		"sudo brew services restart dnsmasq",
		"sudo mkdir -p /etc/resolver",
		"echo 'nameserver 127.0.0.1' | sudo tee /etc/resolver/test",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("detail missing %q:\n%s", want, joined)
		}
	}
}

// fakeRootCert returns a PEM-encoded stub "certificate" and the hex
// SHA-256 of the inner DER bytes. The DER content is just a stable byte
// pattern (not a real cert) - we only need a payload whose SHA-256 we
// know, plus PEM framing so checkTrustStore exercises its pem.Decode path.
func fakeRootCert() ([]byte, string) {
	der := bytes.Repeat([]byte{0xAB}, 200)
	sum := sha256.Sum256(der)
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return encoded, strings.ToUpper(hex.EncodeToString(sum[:]))
}

func TestCheckTrustStoreDarwinPassWhenSHAFound(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	cert, sha := fakeRootCert()
	fake.ExecResponses["caddy_shack:cat /data/caddy/pki/authorities/local/root.crt"] = docker.ExecResponse{
		Stdout: cert,
	}
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "security" {
			// Emit the keychain dump containing the SHA we'd expect to find.
			return []byte("SHA-256 hash: " + sha + "\n"), nil
		}
		return nil, errors.New("unexpected")
	}

	r := checkTrustStoreDarwin(context.Background(), a, sh, noopShell)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestCheckTrustStoreDarwinFailWhenSHAMissing(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	cert, _ := fakeRootCert()
	fake.ExecResponses["caddy_shack:cat /data/caddy/pki/authorities/local/root.crt"] = docker.ExecResponse{
		Stdout: cert,
	}
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "security" {
			return []byte("SHA-256 hash: DEADBEEF\n"), nil
		}
		return nil, errors.New("unexpected")
	}

	r := checkTrustStoreDarwin(context.Background(), a, sh, noopShell)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Fix == nil || !r.Fix.RequiresSudo {
		t.Errorf("fix: %+v", r.Fix)
	}
	// Label must not carry the "(requires sudo)" suffix; the human renderer
	// adds that based on RequiresSudo. Embedding it here causes the
	// duplicate "(requires sudo) (requires sudo)" we saw in real output.
	if strings.Contains(r.Fix.Label, "(requires sudo)") {
		t.Errorf("fix label should not embed '(requires sudo)': %q", r.Fix.Label)
	}
	// Detail should explain why, identify the cert source, and include the
	// exact copy-paste commands a user needs to fix it by hand.
	joined := strings.Join(r.Detail, "\n")
	for _, want := range []string{
		"expected SHA-256 not present in System keychain dump",
		"expected SHA-256 (DER):",
		"cert source: " + CaddyContainerName + ":/data/caddy/pki/authorities/local/root.crt",
		"to fix manually:",
		"docker exec " + CaddyContainerName + " cat /data/caddy/pki/authorities/local/root.crt > /tmp/caddy_shack_root.crt",
		"sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain /tmp/caddy_shack_root.crt",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("detail missing %q:\n%s", want, joined)
		}
	}
}

func TestCheckTrustStoreDarwinFailWhenSecurityErrors(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	cert, _ := fakeRootCert()
	fake.ExecResponses["caddy_shack:cat /data/caddy/pki/authorities/local/root.crt"] = docker.ExecResponse{
		Stdout: cert,
	}
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "security" {
			return nil, errors.New("security: command not found")
		}
		return nil, errors.New("unexpected")
	}

	r := checkTrustStoreDarwin(context.Background(), a, sh, noopShell)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	joined := strings.Join(r.Detail, "\n")
	if !strings.Contains(joined, "security find-certificate failed: security: command not found") {
		t.Errorf("detail should surface the security error:\n%s", joined)
	}
}

// noopShell is a do-nothing shellExecutor for tests that exercise paths
// where the shell executor is not consulted (e.g. the SKIP path before any
// shell-out happens).
var noopShell shellExecutor = func(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}

func TestCheckTrustStoreDarwinSkipWhenContainerNotRunning(t *testing.T) {
	fake := docker.NewFake() // absent
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")
	r := checkTrustStoreDarwin(context.Background(), a, noopShell, noopShell)
	if r.Status != SeveritySkip {
		t.Errorf("status: %v", r.Status)
	}
}

func TestCheckTrustStoreDarwinWarnWhenCertNotProvisioned(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	// No ExecResponses entry => Fake.Exec returns "no scripted response" error.
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	r := checkTrustStoreDarwin(context.Background(), a, noopShell, noopShell)
	if r.Status != SeverityWarn {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestCheckDNSLinuxPassWhenGetentReturnsLoopback(t *testing.T) {
	a := newDoctorApp(t, docker.NewFake(), "http://127.0.0.1:1")

	sh := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "getent" {
			return []byte("STREAM 127.0.0.1 anything.test\n"), nil
		}
		return []byte("ok"), nil
	}

	r := checkDNSLinux(context.Background(), a, sh, noopShell)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}

func TestCheckDNSLinuxFailWhenGetentReturnsNothing(t *testing.T) {
	a := newDoctorApp(t, docker.NewFake(), "http://127.0.0.1:1")

	sh := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "getent" {
			return nil, errors.New("not found")
		}
		return nil, errors.New("missing")
	}

	r := checkDNSLinux(context.Background(), a, sh, noopShell)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Fix == nil || !r.Fix.RequiresSudo {
		t.Fatalf("DNS fail must offer a sudo fix: %+v", r.Fix)
	}
	if strings.Contains(r.Fix.Label, "(requires sudo)") {
		t.Errorf("fix label should not embed '(requires sudo)': %q", r.Fix.Label)
	}
	joined := strings.Join(r.Detail, "\n")
	for _, want := range []string{
		"to fix manually (systemd-resolved",
		"address=/.test/127.0.0.1",
		"Domains=~test",
		"systemctl restart systemd-resolved",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("detail missing %q:\n%s", want, joined)
		}
	}
}

func TestApplyDNSFixLinuxRunsScriptWhenResolvedActive(t *testing.T) {
	ctx := context.Background()
	var shCalls, ttyCalls []shellCall
	sh := recordingShell(&shCalls, struct {
		Out []byte
		Err error
	}{nil, nil}) // systemctl is-active systemd-resolved -> success
	tty := recordingShell(&ttyCalls)

	if err := applyDNSFixLinux(ctx, sh, tty); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(shCalls) != 1 || shCalls[0].Name != "systemctl" {
		t.Fatalf("expected one systemctl is-active probe, got %+v", shCalls)
	}
	if len(ttyCalls) != 1 {
		t.Fatalf("tty calls: want 1, got %d", len(ttyCalls))
	}
	c := ttyCalls[0]
	if c.Name != "sudo" || len(c.Args) < 3 || c.Args[0] != "sh" || c.Args[1] != "-c" {
		t.Fatalf("tty[0]: want sudo sh -c <script>, got %v %v", c.Name, c.Args)
	}
	script := c.Args[2]
	for _, want := range []string{
		"address=/.test/127.0.0.1",
		"Domains=~test",
		"systemctl enable --now dnsmasq",
		"systemctl restart systemd-resolved",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
}

func TestApplyDNSFixLinuxRefusesWhenResolvedNotActive(t *testing.T) {
	ctx := context.Background()
	injected := errors.New("unit not found")
	var shCalls, ttyCalls []shellCall
	sh := recordingShell(&shCalls, struct {
		Out []byte
		Err error
	}{nil, injected})
	tty := recordingShell(&ttyCalls)

	err := applyDNSFixLinux(ctx, sh, tty)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, injected) {
		t.Errorf("error does not wrap injected error: %v", err)
	}
	if len(ttyCalls) != 0 {
		t.Errorf("tty should not have been called, got %d calls", len(ttyCalls))
	}
}

func TestCheckTrustStoreLinuxPassWhenBundleContainsCert(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	cert, _ := fakeRootCert()
	fake.ExecResponses["caddy_shack:cat /data/caddy/pki/authorities/local/root.crt"] = docker.ExecResponse{
		Stdout: cert,
	}
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "test" {
			if args[1] == linuxCABundlePaths[0] {
				return nil, nil
			}
			return nil, errors.New("not found")
		}
		if name == "cat" {
			return cert, nil // the bundle IS the cert for this test
		}
		return nil, errors.New("unexpected")
	}

	r := checkTrustStoreLinux(context.Background(), a, sh, noopShell)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestCheckTrustStoreLinuxFailWhenBundleMissingCert(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	cert, _ := fakeRootCert()
	fake.ExecResponses["caddy_shack:cat /data/caddy/pki/authorities/local/root.crt"] = docker.ExecResponse{
		Stdout: cert,
	}
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	otherDER := bytes.Repeat([]byte{0xCD}, 200)
	otherPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: otherDER})

	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "test" {
			if args[1] == linuxCABundlePaths[0] {
				return nil, nil
			}
			return nil, errors.New("not found")
		}
		if name == "cat" {
			return otherPEM, nil
		}
		return nil, errors.New("unexpected")
	}

	r := checkTrustStoreLinux(context.Background(), a, sh, noopShell)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Fix == nil || !r.Fix.RequiresSudo {
		t.Errorf("fix: %+v", r.Fix)
	}
	joined := strings.Join(r.Detail, "\n")
	for _, want := range []string{
		"expected SHA-256 (DER):",
		"cert source: " + CaddyContainerName + ":/data/caddy/pki/authorities/local/root.crt",
		"checked bundles: " + linuxCABundlePaths[0],
		"sudo trust anchor /tmp/caddy_shack_root.crt",
		"sudo update-ca-certificates",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("detail missing %q:\n%s", want, joined)
		}
	}
}

func TestCheckTrustStoreLinuxSkipWhenContainerNotRunning(t *testing.T) {
	fake := docker.NewFake() // absent
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")
	r := checkTrustStoreLinux(context.Background(), a, noopShell, noopShell)
	if r.Status != SeveritySkip {
		t.Errorf("status: %v", r.Status)
	}
}

func TestCheckTrustStoreLinuxWarnWhenCertNotProvisioned(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	r := checkTrustStoreLinux(context.Background(), a, noopShell, noopShell)
	if r.Status != SeverityWarn {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestApplyTrustStoreFixLinuxUsesTrustAnchorWhenAvailable(t *testing.T) {
	ctx := context.Background()
	cert := []byte("fake-cert-data")
	var ttyCalls []shellCall

	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "sh" && len(args) > 1 && strings.Contains(args[1], "trust") {
			return nil, nil // command -v trust succeeds
		}
		return nil, errors.New("not found")
	}
	tty := recordingShell(&ttyCalls)

	if err := applyTrustStoreFixLinux(ctx, sh, tty, cert); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ttyCalls) != 1 {
		t.Fatalf("tty calls: want 1, got %d", len(ttyCalls))
	}
	c := ttyCalls[0]
	wantPrefix := []string{"trust", "anchor"}
	if c.Name != "sudo" || len(c.Args) < len(wantPrefix)+1 || !equalArgs(c.Args[:len(wantPrefix)], wantPrefix) {
		t.Errorf("tty[0]: want sudo %v <tmpfile>, got %v %v", wantPrefix, c.Name, c.Args)
	}
}

func TestApplyTrustStoreFixLinuxFallsBackToUpdateCACertificates(t *testing.T) {
	ctx := context.Background()
	cert := []byte("fake-cert-data")
	var ttyCalls []shellCall

	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "sh" && len(args) > 1 && strings.Contains(args[1], "command -v trust") {
			return nil, errors.New("not found")
		}
		if name == "sh" && len(args) > 1 && strings.Contains(args[1], "update-ca-certificates") {
			return nil, nil
		}
		return nil, errors.New("unexpected")
	}
	tty := recordingShell(&ttyCalls)

	if err := applyTrustStoreFixLinux(ctx, sh, tty, cert); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ttyCalls) != 1 {
		t.Fatalf("tty calls: want 1, got %d", len(ttyCalls))
	}
	c := ttyCalls[0]
	if c.Name != "sudo" || len(c.Args) < 2 || c.Args[0] != "sh" || c.Args[1] != "-c" {
		t.Fatalf("tty[0]: want sudo sh -c ..., got %v %v", c.Name, c.Args)
	}
	if !strings.Contains(c.Args[2], "update-ca-certificates") {
		t.Errorf("tty[0] script missing update-ca-certificates: %q", c.Args[2])
	}
}

func TestApplyTrustStoreFixLinuxErrorsWhenNeitherToolPresent(t *testing.T) {
	ctx := context.Background()
	cert := []byte("fake-cert-data")

	sh := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("not found")
	}
	tty := noopShell

	err := applyTrustStoreFixLinux(ctx, sh, tty, cert)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCheckStateIntegrityPassWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	r := checkStateIntegrity(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}

func TestCheckStateIntegrityFailWhenCorrupt(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"}, nil, docker.NewFake())

	r := checkStateIntegrity(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
}

func TestCheckStateIntegrityWarnWhenLockHeld(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "state.json.lock")
	l, err := state.AcquireLock(lockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Release() }()

	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  lockPath,
	}, nil, docker.NewFake())

	r := checkStateIntegrity(context.Background(), a)
	if r.Status != SeverityWarn {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}

func TestCheckConfigDriftPassWhenRoutesMatch(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "127.0.0.1:1000"},
	}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}
	desired := caddyapi.Build(st)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write(desired) // live config exactly matches generated
	}))
	defer srv.Close()

	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv.URL, time.Second), fake)

	r := checkConfigDrift(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestCheckConfigDriftWarnWhenRoutesDiffer(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "127.0.0.1:1000"},
	}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}

	// Live config: same shape, different upstream port - drift.
	live := map[string]any{
		"apps": map[string]any{
			"http": map[string]any{
				"servers": map[string]any{
					"srv0": map[string]any{
						"routes": []any{
							map[string]any{
								"match": []any{map[string]any{"host": []any{"a.test"}}},
								"handle": []any{map[string]any{
									"handler":   "reverse_proxy",
									"upstreams": []any{map[string]any{"dial": "127.0.0.1:9999"}},
								}},
							},
						},
					},
				},
			},
		},
	}
	liveBytes, _ := json.Marshal(live)

	var loaded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(200)
			_, _ = w.Write(liveBytes)
			return
		}
		// POST /load - record the body for fix-test below.
		loaded, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	_ = loaded // for golangci - real assertion below

	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv.URL, time.Second), fake)

	r := checkConfigDrift(context.Background(), a)
	if r.Status != SeverityWarn {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	if r.Fix == nil {
		t.Fatal("expected re-load fix")
	}
	if err := r.Fix.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Loaded body must have a route pointing at 127.0.0.1:1000 (from state),
	// not 127.0.0.1:9999 (the drifted live value).
	if !strings.Contains(string(loaded), "127.0.0.1:1000") {
		t.Errorf("re-loaded config does not contain state's upstream: %s", loaded)
	}
}

func TestCheckConfigDriftSkipWhenContainerNotRunning(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	r := checkConfigDrift(context.Background(), a)
	if r.Status != SeveritySkip {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}

// TestCheckConfigDriftPassWhenNoBackends verifies that an empty backend
// state does not produce a false-positive WARN due to nil vs []any{} mismatch.
// caddyapi.Build with no backends marshals "routes": null; Caddy normalises
// live config to "routes": []. extractRoutes must treat both as equal.
func TestCheckConfigDriftPassWhenNoBackends(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	// State with zero backends.
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}

	// Simulate Caddy returning routes: [] (normalised from null).
	liveJSON := []byte(`{"apps":{"http":{"servers":{"srv0":{"routes":[]}}}}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(liveJSON)
	}))
	defer srv.Close()

	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv.URL, time.Second), fake)

	r := checkConfigDrift(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
	if r.Fix != nil {
		t.Errorf("expected no fix on a fresh empty-backends install; got fix: %s", r.Fix.Label)
	}
}

func TestCheckBackendsPassWhenAllReachable(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "host.docker.internal:3000"},
	}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	fake.ExecResponses["caddy_shack:nc -z -w 3 host.docker.internal 3000"] = docker.ExecResponse{}
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"}, nil, fake)

	r := checkBackends(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestCheckBackendsFailWhenAnyUnreachable(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "host.docker.internal:3000"},
		"b": {Domain: "b.test", Upstream: "host.docker.internal:4000"},
	}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	fake.ExecResponses["caddy_shack:nc -z -w 3 host.docker.internal 3000"] = docker.ExecResponse{}
	fake.ExecResponses["caddy_shack:nc -z -w 3 host.docker.internal 4000"] = docker.ExecResponse{
		Err: errors.New("connection refused"),
	}
	// Fallback to wget when nc fails - also fails.
	fake.ExecResponses["caddy_shack:wget -q --spider --tries 1 --timeout 3 http://host.docker.internal:4000/"] = docker.ExecResponse{
		Err: errors.New("connection refused"),
	}
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"}, nil, fake)

	r := checkBackends(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	found := false
	for _, d := range r.Detail {
		if strings.Contains(d, "b") || strings.Contains(d, ":4000") {
			found = true
		}
	}
	if !found {
		t.Errorf("detail must identify failing backend: %v", r.Detail)
	}
}

func TestCheckBackendsPassNoneRegistered(t *testing.T) {
	dir := t.TempDir()
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, fake)

	r := checkBackends(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	if !strings.Contains(r.Summary, "no backends") {
		t.Errorf("summary: %q", r.Summary)
	}
}

func TestCheckBackendsSkipWhenContainerNotRunning(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	r := checkBackends(context.Background(), a)
	if r.Status != SeveritySkip {
		t.Errorf("status: %v", r.Status)
	}
}

// ---------------------------------------------------------------------------
// recordingShell + fix-step tests
// ---------------------------------------------------------------------------

type shellCall struct {
	Name string
	Args []string
}

// recordingShell records every shell call in *calls and returns the next
// scripted response from responses. If responses runs out, returns nil/nil.
// Scripted responses are returned in the order calls arrive.
func recordingShell(calls *[]shellCall, responses ...struct {
	Out []byte
	Err error
}) shellExecutor {
	i := 0
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, shellCall{Name: name, Args: append([]string{}, args...)})
		if i >= len(responses) {
			return nil, nil
		}
		r := responses[i]
		i++
		return r.Out, r.Err
	}
}

func TestApplyDNSFixDarwinRunsExpectedSteps(t *testing.T) {
	ctx := context.Background()
	var shCalls, ttyCalls []shellCall
	sh := recordingShell(&shCalls)
	tty := recordingShell(&ttyCalls)

	if err := applyDNSFixDarwin(ctx, sh, tty); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := len(shCalls) + len(ttyCalls); got != 5 {
		t.Fatalf("total calls: want 5, got %d (sh=%d tty=%d)", got, len(shCalls), len(ttyCalls))
	}
	if len(shCalls) != 2 {
		t.Fatalf("sh calls: want 2, got %d", len(shCalls))
	}
	if len(ttyCalls) != 3 {
		t.Fatalf("tty calls: want 3, got %d", len(ttyCalls))
	}

	// sh[0]: brew install dnsmasq
	if shCalls[0].Name != "brew" || len(shCalls[0].Args) < 2 ||
		shCalls[0].Args[0] != "install" || shCalls[0].Args[1] != "dnsmasq" {
		t.Errorf("sh[0]: want [brew install dnsmasq], got %v %v", shCalls[0].Name, shCalls[0].Args)
	}

	// sh[1]: sh -c ... caddy_shack.conf
	if shCalls[1].Name != "sh" || len(shCalls[1].Args) < 2 || shCalls[1].Args[0] != "-c" {
		t.Errorf("sh[1]: want [sh -c ...], got %v %v", shCalls[1].Name, shCalls[1].Args)
	}
	if !strings.Contains(shCalls[1].Args[1], "caddy_shack.conf") {
		t.Errorf("sh[1] arg does not mention caddy_shack.conf: %q", shCalls[1].Args[1])
	}

	// tty[0]: sudo brew services restart dnsmasq
	wantTTY0 := []string{"brew", "services", "restart", "dnsmasq"}
	if ttyCalls[0].Name != "sudo" || !equalArgs(ttyCalls[0].Args, wantTTY0) {
		t.Errorf("tty[0]: want sudo %v, got %v %v", wantTTY0, ttyCalls[0].Name, ttyCalls[0].Args)
	}

	// tty[1]: sudo mkdir -p /etc/resolver
	wantTTY1 := []string{"mkdir", "-p", "/etc/resolver"}
	if ttyCalls[1].Name != "sudo" || !equalArgs(ttyCalls[1].Args, wantTTY1) {
		t.Errorf("tty[1]: want sudo %v, got %v %v", wantTTY1, ttyCalls[1].Name, ttyCalls[1].Args)
	}

	// tty[2]: sudo sh -c ... /etc/resolver/test
	if ttyCalls[2].Name != "sudo" || len(ttyCalls[2].Args) < 3 ||
		ttyCalls[2].Args[0] != "sh" || ttyCalls[2].Args[1] != "-c" {
		t.Errorf("tty[2]: want sudo sh -c ..., got %v %v", ttyCalls[2].Name, ttyCalls[2].Args)
	}
	if !strings.Contains(ttyCalls[2].Args[2], "/etc/resolver/test") {
		t.Errorf("tty[2] arg does not mention /etc/resolver/test: %q", ttyCalls[2].Args[2])
	}
}

func TestApplyDNSFixDarwinStopsOnFirstError(t *testing.T) {
	ctx := context.Background()
	injected := errors.New("brew write failed")
	var shCalls, ttyCalls []shellCall

	// First sh call succeeds, second fails.
	sh := recordingShell(&shCalls,
		struct {
			Out []byte
			Err error
		}{nil, nil},
		struct {
			Out []byte
			Err error
		}{nil, injected},
	)
	tty := recordingShell(&ttyCalls)

	err := applyDNSFixDarwin(ctx, sh, tty)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, injected) {
		t.Errorf("error does not wrap injected error: %v", err)
	}

	total := len(shCalls) + len(ttyCalls)
	if total != 2 {
		t.Errorf("total calls: want 2, got %d (sh=%d tty=%d)", total, len(shCalls), len(ttyCalls))
	}
	if len(ttyCalls) != 0 {
		t.Errorf("tty should not have been called, got %d calls", len(ttyCalls))
	}
}

func TestApplyTrustStoreFixDarwinRunsSecurityAddTrustedCert(t *testing.T) {
	ctx := context.Background()
	cert := []byte("fake-cert-data")

	var ttyCalls []shellCall
	var statErrDuringCall error

	tty := func(_ context.Context, name string, args ...string) ([]byte, error) {
		captured := append([]string{}, args...)
		// Before recording, stat the tmp file path (last arg) to confirm it exists.
		if len(captured) > 0 {
			statErrDuringCall = func() error {
				_, err := os.Stat(captured[len(captured)-1])
				return err
			}()
		}
		ttyCalls = append(ttyCalls, shellCall{Name: name, Args: captured})
		return nil, nil
	}

	if err := applyTrustStoreFixDarwin(ctx, tty, cert); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ttyCalls) != 1 {
		t.Fatalf("tty calls: want 1, got %d", len(ttyCalls))
	}

	c := ttyCalls[0]
	wantPrefix := []string{"security", "add-trusted-cert", "-d", "-r", "trustRoot",
		"-k", "/Library/Keychains/System.keychain"}
	if c.Name != "sudo" {
		t.Errorf("name: want sudo, got %q", c.Name)
	}
	if len(c.Args) < len(wantPrefix)+1 {
		t.Fatalf("args too short: %v", c.Args)
	}
	if !equalArgs(c.Args[:len(wantPrefix)], wantPrefix) {
		t.Errorf("args prefix: want %v, got %v", wantPrefix, c.Args[:len(wantPrefix)])
	}

	// The last arg is the tmp file path; it must have existed during the call.
	if statErrDuringCall != nil {
		t.Errorf("tmp file did not exist during tty call: %v", statErrDuringCall)
	}

	// After return the tmp file must be gone (deferred cleanup ran).
	tmpPath := c.Args[len(c.Args)-1]
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("tmp file %q should have been removed after return", tmpPath)
	}
}

func TestApplyTrustStoreFixDarwinReturnsErrorOnSecurityFailure(t *testing.T) {
	ctx := context.Background()
	cert := []byte("fake-cert-data")
	injected := errors.New("security exit status 1")

	var calls []shellCall
	tty := recordingShell(&calls,
		struct {
			Out []byte
			Err error
		}{nil, injected},
	)

	err := applyTrustStoreFixDarwin(ctx, tty, cert)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, injected) {
		t.Errorf("error does not wrap injected error: %v", err)
	}
}

// equalArgs reports whether two string slices are equal.
func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCheckBackendsUsesWgetFallbackWhenNcMissing(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "host.docker.internal:3000"},
	}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	// nc missing.
	fake.ExecResponses["caddy_shack:nc -z -w 3 host.docker.internal 3000"] = docker.ExecResponse{
		Err: errors.New("nc: not found"),
	}
	// wget succeeds.
	fake.ExecResponses["caddy_shack:wget -q --spider --tries 1 --timeout 3 http://host.docker.internal:3000/"] = docker.ExecResponse{}
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"}, nil, fake)

	r := checkBackends(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/acme"
	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/caddyapi"
)

// certTestDomain is the apex used for every certificate issued in this file.
// ".invalid" is reserved by RFC 2606 and guaranteed never to resolve
// publicly, so validation never has a chance to wander off to a real
// authoritative nameserver the way a resolvable name like "example.com"
// would.
const certTestDomain = "cert-e2e.invalid"

const (
	pebbleImage    = "ghcr.io/letsencrypt/pebble:latest"
	challtestImage = "ghcr.io/letsencrypt/pebble-challtestsrv:latest"

	pebbleNetwork   = "caddy_shack_it_acme"
	pebbleContainer = "caddy_shack_it_pebble"
	challContainer  = "caddy_shack_it_challtestsrv"
)

// TestCertIssuedByPebbleIsServedAndInternalCAStillWorks is the test the whole
// feature rests on. It asks Caddy for the certificate it actually presents on
// a TLS handshake, not merely whether a request succeeds, because a
// successful request cannot tell "Caddy served our loaded certificate" apart
// from "Caddy minted an internal one over the top".
func TestCertIssuedByPebbleIsServedAndInternalCAStillWorks(t *testing.T) {
	SkipUnlessDocker(t)
	CleanupContainer(t)

	directoryURL, managementURL, dnsAddr := startPebbleStack(t)

	adminPort, httpsPort, _ := runTestCaddy(t)
	adminURL := fmt.Sprintf("http://127.0.0.1:%d", adminPort)

	a := certTestApp(t, adminURL)

	upstream, upstreamPort := certTestUpstream(t)
	defer func() { _ = upstream.Close() }()

	// A .test backend on the internal CA, registered alongside the
	// Pebble-covered one, so a single config and a single container carry
	// both certificate sources at once.
	if err := a.Register(context.Background(), app.RegisterInput{Name: "hello", Port: upstreamPort}); err != nil {
		t.Fatalf("register hello.test: %v", err)
	}
	if err := a.Register(context.Background(), app.RegisterInput{
		Name: "web", Port: upstreamPort, Domain: certTestDomain,
	}); err != nil {
		t.Fatalf("register %s: %v", certTestDomain, err)
	}

	issuer := acme.NewLegoIssuer()
	issuer.DirectoryOverride = directoryURL
	issuer.InsecureSkipTLSVerify = true
	issuer.DNSResolvers = []string{dnsAddr}
	a.SetIssuer(issuer)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	prompt := mustIssueCert(t, a, ctx, app.CertRequestInput{
		Domain: certTestDomain,
		Email:  "cert-e2e@example.com",
	}, managementURL)

	// One prompt, not one per identifier. This is the parallelSolve
	// behaviour the custom DNS provider exists to get: PreSolve every
	// authorisation before Solve runs, so the apex and the wildcard land in
	// a single batch of records rather than two round trips to the operator.
	if prompt.calls != 1 {
		t.Errorf("operator prompted %d times, want exactly 1 for a wildcard order", prompt.calls)
	}

	if iss := presentedIssuer(t, httpsPort, certTestDomain); !strings.Contains(strings.ToLower(iss), "pebble") {
		t.Errorf("issuer for %s = %q, want the Pebble CA. Caddy is not serving the loaded certificate",
			certTestDomain, iss)
	}

	// The wildcard must cover a subdomain with no route of its own, the same
	// property that makes a name like vite.alpha.arrix.dev work in real use.
	if iss := presentedIssuer(t, httpsPort, "sub."+certTestDomain); !strings.Contains(strings.ToLower(iss), "pebble") {
		t.Errorf("issuer for sub.%s = %q, want the Pebble CA. The wildcard does not cover subdomains",
			certTestDomain, iss)
	}

	// The internal CA must be untouched for .test.
	if iss := presentedIssuer(t, httpsPort, "hello.test"); strings.Contains(strings.ToLower(iss), "pebble") {
		t.Errorf("issuer for hello.test = %q, want Caddy's internal CA, not Pebble", iss)
	}
}

// TestCertSurvivesContainerRecreate proves the certificate material lives on
// the host, not in the container: recreating the container (the same thing
// App.Upgrade does to move to a new image) must not lose it. This deliberately
// does not call App.Upgrade, which is hardwired to the operator's real
// container name; instead it reproduces the recreate-then-repush sequence by
// hand against the throwaway test container.
func TestCertSurvivesContainerRecreate(t *testing.T) {
	SkipUnlessDocker(t)
	CleanupContainer(t)

	directoryURL, managementURL, dnsAddr := startPebbleStack(t)

	adminPort, httpsPort, httpPort := runTestCaddy(t)
	adminURL := fmt.Sprintf("http://127.0.0.1:%d", adminPort)

	a := certTestApp(t, adminURL)

	upstream, upstreamPort := certTestUpstream(t)
	defer func() { _ = upstream.Close() }()

	if err := a.Register(context.Background(), app.RegisterInput{
		Name: "web", Port: upstreamPort, Domain: certTestDomain,
	}); err != nil {
		t.Fatalf("register %s: %v", certTestDomain, err)
	}

	issuer := acme.NewLegoIssuer()
	issuer.DirectoryOverride = directoryURL
	issuer.InsecureSkipTLSVerify = true
	issuer.DNSResolvers = []string{dnsAddr}
	a.SetIssuer(issuer)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	mustIssueCert(t, a, ctx, app.CertRequestInput{
		Domain: certTestDomain,
		Email:  "cert-e2e@example.com",
	}, managementURL)

	if iss := presentedIssuer(t, httpsPort, certTestDomain); !strings.Contains(strings.ToLower(iss), "pebble") {
		t.Fatalf("issuer before recreate = %q, want Pebble; test setup is broken before the recreate is even tested", iss)
	}

	// Remove the test container and start a fresh one bound to the same host
	// ports, then re-push the config from state. This is exactly what
	// App.Upgrade does after recreating the production container, done here
	// by hand so the test never touches the operator's real container name.
	if out, err := exec.Command("docker", "rm", "-f", testContainerName).CombinedOutput(); err != nil {
		t.Fatalf("docker rm: %v: %s", err, out)
	}
	startTestCaddy(t, adminPort, httpsPort, httpPort)
	WaitForCaddyAdmin(t, adminURL, 15*time.Second)

	// Register is the App's public surface for "rebuild and re-push the
	// config from state": it calls loadedCerts and caddyapi.Build exactly as
	// Upgrade does after a recreate, which is what actually restores the
	// certificate into the fresh container.
	if err := a.Register(context.Background(), app.RegisterInput{Name: "other", Port: upstreamPort}); err != nil {
		t.Fatalf("register other.test after recreate: %v", err)
	}

	if iss := presentedIssuer(t, httpsPort, certTestDomain); !strings.Contains(strings.ToLower(iss), "pebble") {
		t.Errorf("issuer after recreate = %q, want the Pebble CA. The certificate did not survive", iss)
	}
}

// mustIssueCert drives a.CertRequest against Pebble, retrying the whole
// order up to three times on a transient connection failure.
//
// Pebble's own container log confirms every authorisation goes VALID and the
// certificate is issued, but the client occasionally sees the *next* request
// on the same published port come back as a bare EOF: a known class of
// flakiness talking HTTPS through Docker's host-port forwarding rather than
// a real network path, not an ACME or certificate-store problem. Only that
// class of error is retried; anything else fails the test immediately. The
// failing call happens inside LegoIssuer.Issue, before App writes any
// certificate material or state, so retrying the whole request is safe.
//
// Returns the prompter used by the attempt that succeeded, so its Confirm
// call count reflects that attempt, not any retries.
func mustIssueCert(t *testing.T, a *app.App, ctx context.Context, in app.CertRequestInput, managementURL string) *txtPublisher {
	t.Helper()
	const attempts = 3
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		prompt := &txtPublisher{t: t, managementURL: managementURL}
		err := a.CertRequest(ctx, in, prompt)
		if err == nil {
			return prompt
		}
		lastErr = err
		if !strings.Contains(err.Error(), "EOF") && !strings.Contains(err.Error(), "connection reset") {
			t.Fatalf("cert request against pebble: %v", err)
		}
		t.Logf("cert request attempt %d/%d hit a transient connection error against the test Pebble container, retrying: %v",
			attempt, attempts, err)
		time.Sleep(time.Second)
	}
	t.Fatalf("cert request against pebble: %v", lastErr)
	return nil
}

// txtPublisher answers the DNS-01 prompt by publishing TXT records into
// pebble-challtestsrv's management API, standing in for an operator editing
// DNS by hand.
type txtPublisher struct {
	t             *testing.T
	managementURL string
	calls         int
}

func (p *txtPublisher) Confirm(records []acme.Record) error {
	p.calls++
	client := &http.Client{Timeout: 5 * time.Second}
	for _, r := range records {
		host := strings.TrimSuffix(r.FQDN, ".")
		payload, err := json.Marshal(map[string]string{"host": host, "value": r.Value})
		if err != nil {
			return fmt.Errorf("marshal set-txt payload for %s: %w", host, err)
		}
		resp, err := client.Post(p.managementURL+"/set-txt", "application/json", bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("publish TXT for %s: %w", host, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("set-txt for %s: status %d: %s", host, resp.StatusCode, body)
		}
	}
	return nil
}

// certTestApp builds an App wired to the given admin URL and a fresh, private
// certificate store and state directory. It uses fakeRunningDocker so
// App.CertRequest's own container-state check never touches the real docker
// daemon for the production container name.
func certTestApp(t *testing.T, adminURL string) *app.App {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "state.json")
	return app.NewWithPaths(
		app.Paths{
			StatePath:    statePath,
			LockPath:     statePath + ".lock",
			CertStoreDir: t.TempDir(),
		},
		caddyapi.NewClient(adminURL, 5*time.Second),
		fakeRunningDocker{},
	)
}

// certTestUpstream starts a throwaway upstream and returns it along with the
// host port it is listening on, for RegisterInput.Port.
func certTestUpstream(t *testing.T) (*http.Server, int) {
	t.Helper()
	srv, addr := startUpstream(t)
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("parse upstream addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse upstream port: %v", err)
	}
	return srv, port
}

// runTestCaddy starts the shared test Caddy container on fresh ephemeral
// ports and waits for its admin API, returning the chosen ports.
func runTestCaddy(t *testing.T) (adminPort, httpsPort, httpPort int) {
	t.Helper()
	adminPort, httpsPort, httpPort = freePort(t), freePort(t), freePort(t)
	startTestCaddy(t, adminPort, httpsPort, httpPort)
	WaitForCaddyAdmin(t, fmt.Sprintf("http://127.0.0.1:%d", adminPort), 15*time.Second)
	return adminPort, httpsPort, httpPort
}

// startTestCaddy runs the shared test Caddy container bound to the given host
// ports. Callers are responsible for waiting on the admin API afterwards.
func startTestCaddy(t *testing.T, adminPort, httpsPort, httpPort int) {
	t.Helper()
	caddyfile := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(caddyfile, []byte("{\n\tadmin 0.0.0.0:2019\n}\n"), 0o644); err != nil {
		t.Fatalf("write Caddyfile: %v", err)
	}
	cmd := exec.Command("docker", "run", "-d", "--name", testContainerName,
		"--add-host=host.docker.internal:host-gateway",
		"-p", fmt.Sprintf("127.0.0.1:%d:2019", adminPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:443", httpsPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:80", httpPort),
		"-v", fmt.Sprintf("%s:/etc/caddy/Caddyfile:ro", caddyfile),
		"caddy:2",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
}

// presentedIssuer opens a TLS connection to the test Caddy's HTTPS port with
// SNI set to host and returns the issuer common name of the leaf it presents.
// Verification is skipped deliberately: the identity of the issuer is what is
// under test, not its trustworthiness. Retries briefly because Caddy can take
// a moment to pick up a freshly pushed config.
func presentedIssuer(t *testing.T, httpsPort int, host string) string {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", httpsPort)
	var lastErr error
	for i := 0; i < 20; i++ {
		conn, err := tls.Dial("tcp", addr, &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: true, // #nosec G402 -- inspecting the presented chain's issuer is the test
		})
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		chain := conn.ConnectionState().PeerCertificates
		_ = conn.Close()
		if len(chain) == 0 {
			t.Fatalf("no certificate presented for %s", host)
		}
		return chain[0].Issuer.CommonName
	}
	t.Fatalf("could not complete a TLS handshake for %s: %v", host, lastErr)
	return ""
}

// startPebbleStack runs Pebble and pebble-challtestsrv on a private docker
// network unique to this package, and returns Pebble's ACME directory URL,
// challtestsrv's management API URL, and challtestsrv's DNS address, all
// reachable from the host on ephemeral ports. Pebble is told to use
// challtestsrv as its DNS resolver by container name, so validation never
// depends on host.docker.internal or a fixed host port.
//
// challtestsrv's DNS port is also published to the host: LegoIssuer runs in
// this test process, not in a container, so its own DNS-01 propagation check
// (see LegoIssuer.DNSResolvers) needs to reach challtestsrv directly
// too.
//
// Pebble is a real (if throwaway) certificate authority: it issues a genuine
// chain rooted at its own generated CA, which is what lets a test tell its
// certificates apart from Caddy's internal CA by inspecting the issuer.
func startPebbleStack(t *testing.T) (directoryURL, managementURL, dnsAddr string) {
	t.Helper()

	stop := func() {
		_ = exec.Command("docker", "rm", "-f", pebbleContainer, challContainer).Run()
		_ = exec.Command("docker", "network", "rm", pebbleNetwork).Run()
	}
	stop()
	t.Cleanup(stop)

	if out, err := exec.Command("docker", "network", "create", pebbleNetwork).CombinedOutput(); err != nil {
		t.Fatalf("docker network create: %v (%s)", err, out)
	}

	dirPort := freePort(t)
	mgmtPort := freePort(t)
	dnsPort := freePort(t)

	if out, err := exec.Command("docker", "run", "-d", "--name", challContainer,
		"--network", pebbleNetwork,
		"-p", fmt.Sprintf("127.0.0.1:%d:8055", mgmtPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:8053/udp", dnsPort),
		challtestImage,
		"-defaultIPv4", "127.0.0.1", "-defaultIPv6", "",
	).CombinedOutput(); err != nil {
		t.Fatalf("start challtestsrv: %v (%s)", err, out)
	}

	if out, err := exec.Command("docker", "run", "-d", "--name", pebbleContainer,
		"--network", pebbleNetwork,
		"-p", fmt.Sprintf("127.0.0.1:%d:14000", dirPort),
		"-e", "PEBBLE_VA_NOSLEEP=1",
		pebbleImage,
		"-dnsserver", challContainer+":8053",
	).CombinedOutput(); err != nil {
		t.Fatalf("start pebble: %v (%s)", err, out)
	}

	directoryURL = fmt.Sprintf("https://127.0.0.1:%d/dir", dirPort)
	managementURL = fmt.Sprintf("http://127.0.0.1:%d", mgmtPort)
	dnsAddr = fmt.Sprintf("127.0.0.1:%d", dnsPort)

	waitForHTTPS(t, directoryURL, 30*time.Second)
	return directoryURL, managementURL, dnsAddr
}

// waitForHTTPS polls url over TLS (skipping verification; the server's own
// certificate is exactly what is being waited for) until it responds or the
// deadline expires.
func waitForHTTPS(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- readiness probe against our own throwaway test CA
		},
		Timeout: 3 * time.Second,
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return
			}
		} else {
			lastErr = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("%s did not become ready within %s: %v", url, timeout, lastErr)
}

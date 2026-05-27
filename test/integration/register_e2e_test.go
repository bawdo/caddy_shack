//go:build integration

package integration

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/caddyapi"
)

func TestRegisterE2EReverseProxiesOverHTTPS(t *testing.T) {
	SkipUnlessDocker(t)
	CleanupContainer(t)

	// Start a real Caddy container on a free set of host ports so we don't
	// clash with anything else listening on 80/443/2019.
	adminPort := freePort(t)
	httpsPort := freePort(t)
	httpPort := freePort(t)

	// Create a minimal Caddyfile that makes the admin endpoint accessible
	// from the host (Docker's localhost binding prevents external access by default).
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

	adminURL := fmt.Sprintf("http://127.0.0.1:%d", adminPort)
	WaitForCaddyAdmin(t, adminURL, 15*time.Second)

	// Spin up a tiny upstream listening on a random loopback port. App.Register
	// constructs host.docker.internal:<port> internally, which routes back
	// to this listener from inside the Caddy container.
	upstream, upstreamAddr := startUpstream(t)
	defer func() { _ = upstream.Close() }()
	_, portStr, _ := net.SplitHostPort(upstreamAddr)
	upstreamPort, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse upstream port: %v", err)
	}

	// Build an App that talks to OUR test Caddy and skip docker entirely
	// (we already started the container above).
	statePath := filepath.Join(t.TempDir(), "state.json")
	a := app.NewWithPaths(
		app.Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(adminURL, 5*time.Second),
		fakeRunningDocker{},
	)

	if err := a.Register(context.Background(), app.RegisterInput{
		Name:   "smoke",
		Port:   upstreamPort,
		Domain: "smoke.test",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Hit https://smoke.test via Caddy's HTTPS port. We have to override the
	// dialer to resolve smoke.test -> 127.0.0.1:<httpsPort>, and skip cert
	// verification (Caddy's internal CA isn't trusted by the test process).
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", httpsPort))
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 5 * time.Second,
	}
	// Caddy provisions the internal cert for smoke.test asynchronously after
	// the POST /load returns 2xx. The first request can race the cert
	// generation and come back as "tls: internal error", so retry briefly.
	resp, body := getWithRetry(t, client, "https://smoke.test/", 3*time.Second)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("status: %d body=%s", resp.StatusCode, body)
	}
	if string(body) != "hello from upstream" {
		t.Errorf("body: %s", body)
	}
}

// getWithRetry polls url until a successful response or the deadline.
// Retries on any HTTP transport error (covers the brief window after
// Caddy accepts a new route but hasn't finished issuing the internal cert
// for the new host). Fails the test if the deadline expires.
func getWithRetry(t *testing.T, client *http.Client, url string, timeout time.Duration) (*http.Response, []byte) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		resp, err := client.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			return resp, body
		}
		lastErr = err
		if time.Now().After(deadline) {
			t.Fatalf("GET %s: no success within %s, last error: %v", url, timeout, lastErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func startUpstream(t *testing.T) (*http.Server, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("hello from upstream"))
		}),
		ReadHeaderTimeout: time.Second,
	}
	go func() { _ = srv.Serve(l) }()
	return srv, l.Addr().String()
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

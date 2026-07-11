//go:build integration

package integration

import (
	"context"
	"crypto/tls"
	"fmt"
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

func TestSubdomainE2EWildcardAndOverride(t *testing.T) {
	SkipUnlessDocker(t)
	CleanupContainer(t)

	adminPort := freePort(t)
	httpsPort := freePort(t)
	httpPort := freePort(t)

	caddyfile := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(caddyfile, []byte("{\n\tadmin 0.0.0.0:2019\n}\n"), 0o644); err != nil {
		t.Fatalf("write Caddyfile: %v", err)
	}

	run := exec.Command("docker", "run", "-d", "--name", testContainerName,
		"--add-host=host.docker.internal:host-gateway",
		"-p", fmt.Sprintf("127.0.0.1:%d:2019", adminPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:443", httpsPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:80", httpPort),
		"-v", fmt.Sprintf("%s:/etc/caddy/Caddyfile:ro", caddyfile),
		"caddy:2",
	)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}

	adminURL := fmt.Sprintf("http://127.0.0.1:%d", adminPort)
	WaitForCaddyAdmin(t, adminURL, 15*time.Second)

	parentPort := startBodyUpstream(t, "parent")
	childPort := startBodyUpstream(t, "child")

	statePath := filepath.Join(t.TempDir(), "state.json")
	a := app.NewWithPaths(
		app.Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(adminURL, 5*time.Second),
		fakeRunningDocker{},
	)

	if err := a.Register(context.Background(), app.RegisterInput{Name: "foo", Port: parentPort}); err != nil {
		t.Fatalf("Register foo: %v", err)
	}
	if err := a.Register(context.Background(), app.RegisterInput{Name: "bar.foo", Port: childPort}); err != nil {
		t.Fatalf("Register bar.foo: %v", err)
	}

	client := httpsClient(httpsPort)

	// Wildcard: an arbitrary subdomain of foo hits the parent upstream.
	assertBody(t, client, "https://random.foo.test/", "parent")
	// Exact apex still hits the parent.
	assertBody(t, client, "https://foo.test/", "parent")
	// Override: bar.foo.test wins over *.foo.test and hits the child upstream.
	assertBody(t, client, "https://bar.foo.test/", "child")

	// Non-cascade deregister of the parent leaves the override working.
	if _, err := a.Deregister(context.Background(), "foo", false); err != nil {
		t.Fatalf("Deregister foo: %v", err)
	}
	assertBody(t, client, "https://bar.foo.test/", "child")
}

func startBodyUpstream(t *testing.T, body string) int {
	t.Helper()
	// Bind all interfaces, not just loopback. Caddy runs in a container and
	// dials host.docker.internal, which on Linux CI resolves to the Docker
	// bridge gateway (via --add-host=host-gateway) rather than loopback. A
	// 127.0.0.1-only listener is unreachable from the bridge and yields 502.
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}),
		ReadHeaderTimeout: time.Second,
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	_, portStr, _ := net.SplitHostPort(l.Addr().String())
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse upstream port: %v", err)
	}
	return port
}

func httpsClient(httpsPort int) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", httpsPort))
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 5 * time.Second,
	}
}

func assertBody(t *testing.T, client *http.Client, url, want string) {
	t.Helper()
	resp, body := getWithRetry(t, client, url, 5*time.Second)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("%s status: %d body=%s", url, resp.StatusCode, body)
	}
	if string(body) != want {
		t.Errorf("%s body: got %q want %q", url, body, want)
	}
}

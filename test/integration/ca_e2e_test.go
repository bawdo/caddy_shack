//go:build integration

package integration

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
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

// TestCAExportE2EVerifiesServedCert proves the acceptance criterion end to
// end: App.ExportCA returns the local root CA as PEM, and that root verifies
// the leaf certificate Caddy actually serves for a *.test host. It exercises
// the real /pki/ca/local admin endpoint against a running Caddy, the part the
// app-layer unit tests had to fake.
func TestCAExportE2EVerifiesServedCert(t *testing.T) {
	SkipUnlessDocker(t)
	CleanupContainer(t)

	adminPort := freePort(t)
	httpsPort := freePort(t)
	httpPort := freePort(t)

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

	upstream, upstreamAddr := startUpstream(t)
	defer func() { _ = upstream.Close() }()
	_, portStr, _ := net.SplitHostPort(upstreamAddr)
	upstreamPort, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse upstream port: %v", err)
	}

	statePath := filepath.Join(t.TempDir(), "state.json")
	a := app.NewWithPaths(
		app.Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(adminURL, 5*time.Second),
		fakeRunningDocker{},
	)

	// Registering a .test host makes Caddy provision the internal CA and issue
	// a leaf for smoke.test.
	if err := a.Register(context.Background(), app.RegisterInput{
		Name:   "smoke",
		Port:   upstreamPort,
		Domain: "smoke.test",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Force the handshake so the leaf is issued, and capture the chain Caddy
	// serves (leaf + intermediate).
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", httpsPort))
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 5 * time.Second,
	}
	resp, _ := getWithRetry(t, client, "https://smoke.test/", 5*time.Second)
	defer func() { _ = resp.Body.Close() }()
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		t.Fatal("no peer certificates in TLS handshake")
	}

	// Export the root CA and confirm it is a valid PEM certificate block.
	rootPEM, err := a.ExportCA(context.Background(), false)
	if err != nil {
		t.Fatalf("ExportCA: %v", err)
	}
	block, _ := pem.Decode([]byte(rootPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("ExportCA did not return a PEM CERTIFICATE block:\n%s", rootPEM)
	}
	root, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse root cert: %v", err)
	}

	// The exported root must verify the leaf Caddy served for smoke.test.
	roots := x509.NewCertPool()
	roots.AddCert(root)
	intermediates := x509.NewCertPool()
	for _, c := range resp.TLS.PeerCertificates[1:] {
		intermediates.AddCert(c)
	}
	leaf := resp.TLS.PeerCertificates[0]
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName:       "smoke.test",
		Roots:         roots,
		Intermediates: intermediates,
	}); err != nil {
		t.Fatalf("exported root CA does not verify the served leaf: %v", err)
	}
}

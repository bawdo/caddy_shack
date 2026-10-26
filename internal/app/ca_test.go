package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
)

const (
	testRootPEM = "-----BEGIN CERTIFICATE-----\nROOT\n-----END CERTIFICATE-----\n"
	testIntPEM  = "-----BEGIN CERTIFICATE-----\nINT\n-----END CERTIFICATE-----\n"
)

// caTestApp wires an App to a fake docker with a running caddy container and
// an httptest admin server that serves the given /pki/ca/local body.
func caTestApp(t *testing.T, caBody string) *App {
	t.Helper()
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pki/ca/local" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(caBody))
	}))
	t.Cleanup(srv.Close)

	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning

	return NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, caddyapi.NewClient(srv.URL, time.Second), fakeDock)
}

func TestExportCAReturnsRootPEM(t *testing.T) {
	a := caTestApp(t, `{"root_certificate":`+quote(testRootPEM)+`,"intermediate_certificate":`+quote(testIntPEM)+`}`)

	pem, err := a.ExportCA(context.Background(), false)
	if err != nil {
		t.Fatalf("ExportCA: %v", err)
	}
	if !strings.Contains(pem, "ROOT") {
		t.Errorf("expected root cert, got %q", pem)
	}
	if strings.Contains(pem, "INT") {
		t.Errorf("root-only export leaked intermediate: %q", pem)
	}
	if !strings.HasSuffix(pem, "\n") {
		t.Errorf("expected trailing newline, got %q", pem)
	}
}

func TestExportCAWithIntermediateReturnsBoth(t *testing.T) {
	a := caTestApp(t, `{"root_certificate":`+quote(testRootPEM)+`,"intermediate_certificate":`+quote(testIntPEM)+`}`)

	pem, err := a.ExportCA(context.Background(), true)
	if err != nil {
		t.Fatalf("ExportCA: %v", err)
	}
	if !strings.Contains(pem, "ROOT") || !strings.Contains(pem, "INT") {
		t.Errorf("expected root+intermediate, got %q", pem)
	}
	// Root must come before intermediate.
	if strings.Index(pem, "ROOT") > strings.Index(pem, "INT") {
		t.Errorf("root should precede intermediate: %q", pem)
	}
}

func TestExportCANotInstalledWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	fakeDock := docker.NewFake() // no container
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, caddyapi.NewClient("http://127.0.0.1:1", time.Second), fakeDock)

	_, err := a.ExportCA(context.Background(), false)
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("expected ErrNotInstalled, got %v", err)
	}
}

func TestExportCANotRunningWhenStopped(t *testing.T) {
	dir := t.TempDir()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateStopped
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, caddyapi.NewClient("http://127.0.0.1:1", time.Second), fakeDock)

	_, err := a.ExportCA(context.Background(), false)
	if !errors.Is(err, ErrCaddyNotRunning) {
		t.Errorf("expected ErrCaddyNotRunning, got %v", err)
	}
}

func TestExportCADockerUnreachable(t *testing.T) {
	dir := t.TempDir()
	fakeDock := docker.NewFake()
	fakeDock.DaemonErr = docker.ErrDaemonUnreachable
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, caddyapi.NewClient("http://127.0.0.1:1", time.Second), fakeDock)

	_, err := a.ExportCA(context.Background(), false)
	if !errors.Is(err, ErrDockerUnreachable) {
		t.Errorf("expected ErrDockerUnreachable, got %v", err)
	}
}

func TestExportCACaddyNotRunningWhenAdminUnreachable(t *testing.T) {
	dir := t.TempDir()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning
	// Container reports running, but the admin API is down.
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, caddyapi.NewClient("http://127.0.0.1:1", 200*time.Millisecond), fakeDock)

	_, err := a.ExportCA(context.Background(), false)
	if !errors.Is(err, ErrCaddyNotRunning) {
		t.Errorf("expected ErrCaddyNotRunning, got %v", err)
	}
}

func TestExportCAErrorsWhenCANotProvisioned(t *testing.T) {
	a := caTestApp(t, `{"root_certificate":"","intermediate_certificate":""}`)

	_, err := a.ExportCA(context.Background(), false)
	if err == nil {
		t.Fatal("expected error for unprovisioned CA, got nil")
	}
	// Must not be one of the install/run sentinels; it's a distinct condition.
	if errors.Is(err, ErrNotInstalled) || errors.Is(err, ErrCaddyNotRunning) {
		t.Errorf("unprovisioned CA should be generic failure, got %v", err)
	}
}

// quote JSON-escapes a PEM string for embedding in a test payload literal.
func quote(s string) string {
	return `"` + strings.ReplaceAll(s, "\n", `\n`) + `"`
}

// TestExportCANotRunningWhenRestarting: a crash-looping container is not
// serving, and must say so by state rather than by an admin API timeout.
func TestExportCANotRunningWhenRestarting(t *testing.T) {
	dir := t.TempDir()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRestarting
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, caddyapi.NewClient("http://127.0.0.1:1", time.Second), fakeDock)

	_, err := a.ExportCA(context.Background(), false)
	if !errors.Is(err, ErrCaddyNotRunning) || !strings.Contains(err.Error(), "(state=restarting)") ||
		strings.Contains(err.Error(), "unreachable") {
		t.Errorf("want ErrCaddyNotRunning (state=restarting), got %v", err)
	}
}

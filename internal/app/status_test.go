package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

func TestStatusHealthyWhenRunningAndReachable(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning

	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, caddyapi.NewClient(srv.URL, time.Second), fakeDock)

	rep, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !rep.Healthy {
		t.Errorf("expected Healthy=true: %+v", rep)
	}
	if rep.ContainerState != "running" {
		t.Errorf("ContainerState: %s", rep.ContainerState)
	}
	if !rep.AdminReachable {
		t.Errorf("AdminReachable=false")
	}
}

func TestStatusReportsCaddyVersionWhenRunning(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning
	fakeDock.ExecResponses["caddy_shack:caddy version"] = docker.ExecResponse{
		Stdout: []byte("v2.8.4 h1:abcdef="),
	}

	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, caddyapi.NewClient(srv.URL, time.Second), fakeDock)

	rep, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.CaddyVersion != "v2.8.4" {
		t.Errorf("CaddyVersion: got %q want v2.8.4", rep.CaddyVersion)
	}
}

func TestStatusOmitsCaddyVersionWhenStopped(t *testing.T) {
	dir := t.TempDir()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateStopped

	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, caddyapi.NewClient("http://127.0.0.1:1", 200*time.Millisecond), fakeDock)

	rep, _ := a.Status(context.Background())
	if rep.CaddyVersion != "" {
		t.Errorf("CaddyVersion: got %q want empty when not running", rep.CaddyVersion)
	}
}

func TestStatusUnhealthyWhenContainerStopped(t *testing.T) {
	dir := t.TempDir()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateStopped

	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, caddyapi.NewClient("http://127.0.0.1:1", 200*time.Millisecond), fakeDock)

	rep, err := a.Status(context.Background())
	if !errors.Is(err, ErrUnhealthy) {
		t.Errorf("expected ErrUnhealthy, got %v", err)
	}
	if rep.Healthy {
		t.Errorf("expected Healthy=false")
	}
}

func TestStatusReportsBackendCount(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := writeFile(t, statePath, `{"schema_version":1,"backends":{"a":{"domain":"a.test","upstream":"127.0.0.1:1"}}}`); err != nil {
		t.Fatal(err)
	}
	s, _ := state.Load(statePath)
	live := caddyapi.Build(s)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write(live) }))
	defer srv.Close()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning

	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv.URL, time.Second), fakeDock)
	rep, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.BackendCount != 1 {
		t.Errorf("BackendCount: %d", rep.BackendCount)
	}
}

func TestStatusReturnsErrStateCorruptWhenStateFileBroken(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := writeFile(t, statePath, "{not json"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning

	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv.URL, time.Second), fakeDock)

	_, err := a.Status(context.Background())
	if !errors.Is(err, ErrStateCorrupt) {
		t.Errorf("expected ErrStateCorrupt, got %v", err)
	}
}

func TestStatusReportsNoCertificatesWhenNoneStored(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := writeFile(t, statePath, `{"schema_version":2,"backends":{},"certs":{}}`); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("{}")) }))
	defer srv.Close()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning

	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv.URL, time.Second), fakeDock)
	rep, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.CertCount != 0 {
		t.Errorf("CertCount: got %d want 0", rep.CertCount)
	}
	if rep.SoonestCertExpiryDays != nil {
		t.Errorf("SoonestCertExpiryDays: got %v want nil", *rep.SoonestCertExpiryDays)
	}
}

func TestStatusReportsOneCertificate(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	notAfter := time.Now().Add(45 * 24 * time.Hour).Format(time.RFC3339)
	body := `{"schema_version":2,"backends":{},"certs":{"a.example.com":{"domain":"a.example.com","subjects":["a.example.com"],"env":"production","email":"k@example.com","issued_at":"2026-01-01T00:00:00Z","not_after":"` + notAfter + `"}}}`
	if err := writeFile(t, statePath, body); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("{}")) }))
	defer srv.Close()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning

	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv.URL, time.Second), fakeDock)
	rep, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.CertCount != 1 {
		t.Errorf("CertCount: got %d want 1", rep.CertCount)
	}
	if rep.SoonestCertExpiryDays == nil {
		t.Fatal("SoonestCertExpiryDays: got nil, want a value")
	}
	if got := *rep.SoonestCertExpiryDays; got < 43 || got > 45 {
		t.Errorf("SoonestCertExpiryDays: got %d want about 45", got)
	}
}

func TestStatusReportsSoonestExpiryAmongSeveralCertificates(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	far := time.Now().Add(80 * 24 * time.Hour).Format(time.RFC3339)
	soon := time.Now().Add(5 * 24 * time.Hour).Format(time.RFC3339)
	mid := time.Now().Add(40 * 24 * time.Hour).Format(time.RFC3339)
	// "a.example.com" sorts first but is not the soonest expiry; the
	// soonest, "b.example.com", must still win regardless of map order.
	body := `{"schema_version":2,"backends":{},"certs":{
		"a.example.com":{"domain":"a.example.com","subjects":["a.example.com"],"env":"production","email":"k@example.com","issued_at":"2026-01-01T00:00:00Z","not_after":"` + far + `"},
		"b.example.com":{"domain":"b.example.com","subjects":["b.example.com"],"env":"production","email":"k@example.com","issued_at":"2026-01-01T00:00:00Z","not_after":"` + soon + `"},
		"c.example.com":{"domain":"c.example.com","subjects":["c.example.com"],"env":"production","email":"k@example.com","issued_at":"2026-01-01T00:00:00Z","not_after":"` + mid + `"}
	}}`
	if err := writeFile(t, statePath, body); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("{}")) }))
	defer srv.Close()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning

	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv.URL, time.Second), fakeDock)
	rep, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.CertCount != 3 {
		t.Errorf("CertCount: got %d want 3", rep.CertCount)
	}
	if rep.SoonestCertExpiryDays == nil {
		t.Fatal("SoonestCertExpiryDays: got nil, want a value")
	}
	if got := *rep.SoonestCertExpiryDays; got < 3 || got > 5 {
		t.Errorf("SoonestCertExpiryDays: got %d want about 5", got)
	}
}

func TestStatusReportsExpiredCertificateAsNegativeDays(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	notAfter := time.Now().Add(-5 * 24 * time.Hour).Format(time.RFC3339)
	body := `{"schema_version":2,"backends":{},"certs":{"a.example.com":{"domain":"a.example.com","subjects":["a.example.com"],"env":"production","email":"k@example.com","issued_at":"2026-01-01T00:00:00Z","not_after":"` + notAfter + `"}}}`
	if err := writeFile(t, statePath, body); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("{}")) }))
	defer srv.Close()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning

	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv.URL, time.Second), fakeDock)
	rep, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.SoonestCertExpiryDays == nil {
		t.Fatal("SoonestCertExpiryDays: got nil, want a value")
	}
	if got := *rep.SoonestCertExpiryDays; got >= 0 {
		t.Errorf("SoonestCertExpiryDays: got %d want negative for an expired certificate", got)
	}
}

func TestStatusReturnsErrDockerUnreachableWhenDaemonDown(t *testing.T) {
	dir := t.TempDir()
	fakeDock := docker.NewFake()
	fakeDock.DaemonErr = docker.ErrDaemonUnreachable

	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, caddyapi.NewClient("http://127.0.0.1:1", 200*time.Millisecond), fakeDock)

	rep, err := a.Status(context.Background())
	if !errors.Is(err, ErrDockerUnreachable) {
		t.Errorf("expected ErrDockerUnreachable, got %v", err)
	}
	if rep.ContainerState != "docker-down" {
		t.Errorf("ContainerState: got %q want %q", rep.ContainerState, "docker-down")
	}
}

// statusLANApp builds an App with a healthy running Caddy whose registry is
// stateJSON, plus the given LAN resolver and fake docker.
func statusLANApp(t *testing.T, stateJSON string, r LANResolver, fakeDock *docker.Fake) *App {
	t.Helper()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := writeFile(t, statePath, stateJSON); err != nil {
		t.Fatal(err)
	}
	s, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write(caddyapi.Build(s))
	}))
	t.Cleanup(srv.Close)
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning
	return NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv.URL, time.Second), fakeDock).WithLANResolver(r)
}

const lanStateJSON = `{"schema_version":2,"backends":{
"a":{"domain":"a.test","upstream":"127.0.0.1:1","lan_domain":"a.arrix.home.lan"},
"b":{"domain":"b.test","upstream":"127.0.0.1:2"}},
"lan":{"enabled":true,"ip":"192.168.1.12","mode":"discovered","suffix":"arrix.home.lan"}}`

func TestStatusNoLANBlock(t *testing.T) {
	fake := docker.NewFake()
	a := statusLANApp(t, `{"schema_version":2,"backends":{}}`, discovered("192.168.1.12"), fake)
	rep, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.LAN != nil {
		t.Errorf("LAN should be nil: %+v", rep.LAN)
	}
}

func TestStatusLANHealthy(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[LANContainerName] = docker.StateRunning
	a := statusLANApp(t, lanStateJSON, discovered("192.168.1.12"), fake)
	rep, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := LANStatus{Enabled: true, IP: "192.168.1.12", Mode: "discovered", Suffix: "arrix.home.lan",
		IPPresent: true, ForwarderState: "running", LANBackends: 1}
	if rep.LAN == nil || *rep.LAN != want {
		t.Errorf("LAN: got %+v want %+v", rep.LAN, want)
	}
}

func TestStatusLANDownStillHealthy(t *testing.T) {
	fake := docker.NewFake()
	a := statusLANApp(t, lanStateJSON, discoverStub{derr: errors.New("no iface")}, fake)
	rep, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !rep.Healthy {
		t.Errorf("Healthy should stay true: %+v", rep)
	}
	if rep.LAN == nil || rep.LAN.IPPresent || rep.LAN.ForwarderState != "absent" || rep.LAN.DiscoveredIP != "" {
		t.Errorf("LAN: %+v", rep.LAN)
	}
}

func TestStatusLANDrift(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[LANContainerName] = docker.StateRunning
	a := statusLANApp(t, lanStateJSON, discovered("192.168.1.99"), fake)
	rep, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.LAN == nil || rep.LAN.DiscoveredIP != "192.168.1.99" {
		t.Errorf("LAN: %+v", rep.LAN)
	}
	if !rep.Healthy {
		t.Error("drift must not affect Healthy")
	}
}

func TestStatusUnhealthyWhenContainerRestarting(t *testing.T) {
	dir := t.TempDir()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRestarting

	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, caddyapi.NewClient("http://127.0.0.1:1", 200*time.Millisecond), fakeDock)

	rep, err := a.Status(context.Background())
	if !errors.Is(err, ErrUnhealthy) {
		t.Errorf("expected ErrUnhealthy, got %v", err)
	}
	if rep.ContainerState != "restarting" {
		t.Errorf("ContainerState = %q, want restarting", rep.ContainerState)
	}
	if rep.Healthy {
		t.Errorf("expected Healthy=false")
	}
}

//go:build integration

// Package integration spins up real CaddyShack-managed Caddy containers
// against a temporary state directory. Tests in this package require the
// `docker` CLI on PATH and a running docker daemon. Run with:
//
//	go test -tags integration ./test/integration/...
package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
)

const testContainerName = "caddy_shack_it"

// SkipUnlessDocker skips the test if `docker info` doesn't succeed.
func SkipUnlessDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
}

// CleanupContainer ensures the test container is gone before AND after the
// test. The container name is fixed to keep tests isolated from a developer's
// real CaddyShack install.
func CleanupContainer(t *testing.T) {
	t.Helper()
	rm := func() {
		_ = exec.Command("docker", "rm", "-f", testContainerName).Run()
	}
	rm()
	t.Cleanup(rm)
}

// adminGet GETs path on the admin API at baseURL with the Origin header
// caddy_shack's client sends, which Caddy requires once enforce_origin is on
// (every caddy_shack container, and any Caddy after its first config push).
// Like the client, if Caddy refuses that origin (a Caddy still on the
// pre-origins admin block, as restart_drift_e2e_test.go pins) it retries
// once without the header, so raw assertions see what the client sees.
func adminGet(baseURL, path string) (*http.Response, error) {
	get := func(withOrigin bool) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
		if err != nil {
			return nil, err
		}
		if withOrigin {
			req.Header.Set("Origin", "http://"+caddyapi.AdminOrigins[0])
		}
		return http.DefaultClient.Do(req)
	}
	resp, err := get(true)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		return resp, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if bytes.Contains(body, []byte("not allowed to access from origin")) {
		return get(false)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// WaitForCaddyAdmin polls the admin API, through caddy_shack's own client
// (so with its Origin header and its pre-origins fallback), until /config/
// returns 2xx or the deadline expires.
func WaitForCaddyAdmin(t *testing.T, baseURL string, timeout time.Duration) {
	t.Helper()
	client := caddyapi.NewClient(baseURL, 2*time.Second)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if client.Reachable(context.Background()) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("Caddy admin API never came up at %s", baseURL)
}

// adminCaddyfile is the admin-only Caddyfile the hand-rolled test
// containers boot from: the same admin block as caddy_shack's own boot
// Caddyfile, with enforce_origin and origins built from
// caddyapi.AdminOrigins so the two cannot drift.
var adminCaddyfile = "{\n\tadmin 0.0.0.0:2019 {\n\t\tenforce_origin\n\t\torigins " +
	strings.Join(caddyapi.AdminOrigins, " ") + "\n\t}\n}\n"

// MustAppWithTempState builds an App pointed at a tempdir state file. The
// caller is responsible for ensuring Caddy is running on the admin port the
// App was built with.
func MustAppWithTempState(t *testing.T, statePath string) *app.App {
	t.Helper()
	return app.NewWithPaths(app.Paths{
		StatePath: statePath,
		LockPath:  statePath + ".lock",
	}, nil, nil)
}

// fakeRunningDocker is a stub Client for integration tests that own the
// real container lifecycle themselves (so they don't want App to touch
// docker for inspections). Always reports the container as running.
type fakeRunningDocker struct{}

func (fakeRunningDocker) DaemonReachable() error    { return nil }
func (fakeRunningDocker) EnsureVolume(string) error { return nil }
func (fakeRunningDocker) PullImage(string) error    { return nil }
func (fakeRunningDocker) ContainerState(string) (docker.ContainerState, error) {
	return docker.StateRunning, nil
}
func (fakeRunningDocker) CreateAndStart(docker.ContainerSpec) error { return nil }
func (fakeRunningDocker) Start(string) error                        { return nil }
func (fakeRunningDocker) Stop(string) error                         { return nil }
func (fakeRunningDocker) Remove(string) error                       { return nil }
func (fakeRunningDocker) Exec(context.Context, string, []string) ([]byte, error) {
	return nil, nil
}

// Network methods are no-ops: integration tests that exercise container-mode
// routing own the network lifecycle themselves (attaching their throwaway
// caddy container directly via `docker run --network`), so App should not try
// to touch docker networking through this stub.
func (fakeRunningDocker) EnsureNetwork(string) error              { return nil }
func (fakeRunningDocker) ConnectNetwork(string, string) error     { return nil }
func (fakeRunningDocker) ImageID(string) (string, error)          { return "", nil }
func (fakeRunningDocker) ContainerImageID(string) (string, error) { return "", nil }

// EngineInfo reports Docker Desktop so the App keeps routing host-mode
// backends via host.docker.internal: these tests own their Caddy container
// and never create the native-Linux relay.
func (fakeRunningDocker) EngineInfo() (docker.EngineInfo, error) { return docker.DesktopEngine, nil }
func (fakeRunningDocker) BridgeGateway() (string, error)         { return "172.17.0.1", nil }
func (fakeRunningDocker) InspectContainer(string) (docker.ContainerInfo, error) {
	return docker.ContainerInfo{NetworkMode: "bridge", Networks: []string{"bridge"}}, nil
}

// lanTestSuffix marks every Docker object a LAN integration test creates.
const lanTestSuffix = "_it_lan"

// lanTestInstance is a complete CaddyShack install, LAN forwarder included,
// that shares no container, network, volume or port with the default one:
// every name is the default with _it_lan appended, and every port is a high
// port free on all interfaces (the forwarder binds a LAN address, not
// loopback). Pair it with cleanupLANInstance.
func lanTestInstance() app.Instance {
	def := app.DefaultInstance()
	ports := highPorts(5)
	return app.Instance{
		ContainerName:      def.ContainerName + lanTestSuffix,
		RelayContainerName: def.RelayContainerName + lanTestSuffix,
		NetworkName:        def.NetworkName + lanTestSuffix,
		DataVolume:         def.DataVolume + lanTestSuffix,
		ConfigVolume:       def.ConfigVolume + lanTestSuffix,
		RelayVolume:        def.RelayVolume + lanTestSuffix,
		LANContainerName:   def.LANContainerName + lanTestSuffix,
		HTTPPort:           ports[0],
		HTTPSPort:          ports[1],
		AdminPort:          ports[2],
		LANHTTPPort:        ports[3],
		LANHTTPSPort:       ports[4],
	}
}

// highPorts returns n distinct ports that are free on every interface. The
// listeners stay open until all n are picked so no port repeats.
func highPorts(n int) []int {
	out := make([]int, 0, n)
	var ls []net.Listener
	defer func() {
		for _, l := range ls {
			_ = l.Close()
		}
	}()
	for len(out) < n {
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			panic(fmt.Sprintf("pick a free port: %v", err))
		}
		ls = append(ls, l)
		out = append(out, l.Addr().(*net.TCPAddr).Port)
	}
	return out
}

// cleanupLANInstance refuses an instance that overlaps the real install,
// then removes its containers, volumes and network before the test and
// again when it ends.
func cleanupLANInstance(t *testing.T, inst app.Instance) {
	t.Helper()
	def := app.DefaultInstance()
	names := []string{inst.ContainerName, inst.RelayContainerName, inst.NetworkName, inst.DataVolume,
		inst.ConfigVolume, inst.RelayVolume, inst.LANContainerName}
	for _, n := range names {
		if !strings.HasSuffix(n, lanTestSuffix) {
			t.Fatalf("LAN test instance name %q lacks the %s suffix", n, lanTestSuffix)
		}
	}
	for _, p := range []int{inst.HTTPPort, inst.HTTPSPort, inst.AdminPort, inst.LANHTTPPort, inst.LANHTTPSPort} {
		if p < 1024 || p == def.HTTPPort || p == def.HTTPSPort || p == def.AdminPort {
			t.Fatalf("LAN test instance port %d is not a high port: %+v", p, inst)
		}
	}
	rm := func() {
		_ = exec.Command("docker", "rm", "-f", inst.LANContainerName, inst.ContainerName, inst.RelayContainerName).Run()
		_ = exec.Command("docker", "volume", "rm", inst.DataVolume, inst.ConfigVolume, inst.RelayVolume).Run()
		_ = exec.Command("docker", "network", "rm", inst.NetworkName).Run()
	}
	rm()
	t.Cleanup(rm)
}

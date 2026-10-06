//go:build integration

package integration

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/app"
)

// isolatedInstance is a complete CaddyShack install (Caddy container, relay,
// volumes, shared network, loopback ports) that shares nothing with a real
// one, so the real container lifecycle can be driven next to a developer's
// live caddy_shack without touching it.
func isolatedInstance(t *testing.T) app.Instance {
	t.Helper()
	const p = "caddy_shack_it_restart"
	inst := app.Instance{
		ContainerName:      p,
		RelayContainerName: p + "_relay",
		NetworkName:        p + "_net",
		DataVolume:         p + "_data",
		ConfigVolume:       p + "_config",
		RelayVolume:        p + "_relay_socket",
		HTTPPort:           freePort(t),
		HTTPSPort:          freePort(t),
		AdminPort:          freePort(t),
	}
	def := app.DefaultInstance()
	for _, clash := range []bool{
		inst.ContainerName == def.ContainerName, inst.RelayContainerName == def.RelayContainerName,
		inst.NetworkName == def.NetworkName, inst.DataVolume == def.DataVolume,
		inst.ConfigVolume == def.ConfigVolume, inst.RelayVolume == def.RelayVolume,
		inst.HTTPPort == def.HTTPPort, inst.HTTPSPort == def.HTTPSPort, inst.AdminPort == def.AdminPort,
	} {
		if clash {
			t.Fatalf("isolated instance overlaps the real install: %+v", inst)
		}
	}
	rm := func() {
		_ = exec.Command("docker", "rm", "-f", inst.ContainerName, inst.RelayContainerName).Run()
		_ = exec.Command("docker", "volume", "rm", inst.DataVolume, inst.ConfigVolume, inst.RelayVolume).Run()
		_ = exec.Command("docker", "network", "rm", inst.NetworkName).Run()
	}
	rm()
	t.Cleanup(rm)
	return inst
}

func mustDocker(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker %v: %v: %s", args, err, out)
	}
}

// liveConfig returns Caddy's live config once the admin API answers.
func liveConfig(t *testing.T, inst app.Instance) string {
	t.Helper()
	WaitForCaddyAdmin(t, inst.AdminURL(), 30*time.Second)
	resp, err := http.Get(inst.AdminURL() + "/config/")
	if err != nil {
		t.Fatalf("GET /config/: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(b))
}

// serves asserts https://<host>/ through the instance's Caddy reaches the
// test upstream.
func serves(t *testing.T, inst app.Instance, host string) {
	t.Helper()
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", inst.HTTPSPort))
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: host},
		},
		Timeout: 5 * time.Second,
	}
	resp, body := getWithRetry(t, client, "https://"+host+"/", 15*time.Second)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || string(body) != "hello from upstream" {
		t.Fatalf("https://%s: %d %q", host, resp.StatusCode, body)
	}
}

// legacyRunArgs is the `docker run` argv caddy_shack used before this fix:
// the same container, except Caddy boots from the bare admin-only
// Caddyfile without --resume.
func legacyRunArgs(inst app.Instance) []string {
	return []string{
		"run", "-d", "--name", inst.ContainerName, "--restart", "unless-stopped",
		"--add-host", app.HostGatewayEntry,
		"-p", fmt.Sprintf("127.0.0.1:%d:80", inst.HTTPPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:443", inst.HTTPSPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:2019", inst.AdminPort),
		"-v", inst.DataVolume + ":/data", "-v", inst.ConfigVolume + ":/config",
		"-v", inst.RelayVolume + ":" + app.RelaySocketDir,
		app.CaddyImage,
		"sh", "-c", "printf '{\\n\\tadmin 0.0.0.0:2019\\n}\\n' > /etc/caddy/Caddyfile && " +
			"exec caddy run --config /etc/caddy/Caddyfile --adapter caddyfile",
	}
}

const bareConfig = `{"admin":{"listen":"0.0.0.0:2019"}}`

// TestRestartDriftE2E reproduces the 2026-10-05 incident against real
// docker and proves the fix:
//
//  1. On a container created the old way (no --resume), register a route,
//     then restart the container out of band: Caddy comes back with only
//     its admin config while the registry still lists the route.
//  2. status detects that (exit 9, not "healthy"), list shows the route as
//     not live, and reconciliation (sync, and start) restores it.
//  3. install recreates the outdated container; after that an out-of-band
//     restart no longer loses anything, with no caddy_shack command run.
//  4. caddy_shack's own restart, and a corrupt autosave, also end with the
//     registry served.
func TestRestartDriftE2E(t *testing.T) {
	SkipUnlessDocker(t)
	ctx := context.Background()
	inst := isolatedInstance(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	a := app.NewForInstance(app.Paths{StatePath: statePath, LockPath: statePath + ".lock"}, inst, nil, nil)

	upstream, addr := startUpstream(t)
	defer func() { _ = upstream.Close() }()
	_, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	// --- 1. the bug, on a legacy container -------------------------------
	mustDocker(t, "pull", app.CaddyImage)
	for _, v := range []string{inst.DataVolume, inst.ConfigVolume, inst.RelayVolume} {
		mustDocker(t, "volume", "create", v)
	}
	mustDocker(t, legacyRunArgs(inst)...)
	WaitForCaddyAdmin(t, inst.AdminURL(), 30*time.Second)
	if err := a.Start(ctx); err != nil { // relay + network, as a user's start would
		t.Fatalf("Start: %v", err)
	}
	if err := a.Register(ctx, app.RegisterInput{Name: "itapp", Port: port}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	serves(t, inst, "itapp.test")
	if _, err := a.Status(ctx); err != nil {
		t.Fatalf("status before restart: %v", err)
	}

	mustDocker(t, "restart", inst.ContainerName)
	if got := liveConfig(t, inst); got != bareConfig {
		t.Fatalf("legacy container should come back bare (the bug), got %s", got)
	}

	// --- 2. detection and reconciliation ----------------------------------
	rep, err := a.Status(ctx)
	if !errors.Is(err, app.ErrDrift) || app.ExitCode(err) != 9 || rep.Healthy {
		t.Fatalf("status after out-of-band restart: healthy=%v err=%v; want drift (exit 9)", rep.Healthy, err)
	}
	if rep.Drift.HTTPApp || rep.Drift.TLSApp || strings.Join(rep.Drift.Missing, ",") != "itapp" {
		t.Errorf("drift: %+v", rep.Drift)
	}
	items, err := a.List(ctx)
	if err != nil || len(items) != 1 || items[0].Live != app.LiveNo {
		t.Errorf("list should show itapp registered but not live: %+v %v", items, err)
	}
	sync, err := a.Sync(ctx)
	if err != nil || !sync.Pushed {
		t.Fatalf("Sync: %+v %v", sync, err)
	}
	if _, err := a.Status(ctx); err != nil {
		t.Fatalf("status after sync: %v", err)
	}
	serves(t, inst, "itapp.test")

	// start on a running, drifted Caddy (what a user runs after a reboot)
	// must restore too, not no-op.
	mustDocker(t, "restart", inst.ContainerName)
	if got := liveConfig(t, inst); got != bareConfig {
		t.Fatalf("expected bare config again, got %s", got)
	}
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := a.Status(ctx); err != nil {
		t.Fatalf("status after start: %v", err)
	}

	// --- 3. install migrates; restarts no longer lose routes ---------------
	if err := a.Install(ctx); err != nil {
		t.Fatalf("Install (recreate): %v", err)
	}
	if _, err := a.Status(ctx); err != nil {
		t.Fatalf("status after install: %v", err)
	}
	for i := 0; i < 2; i++ {
		mustDocker(t, "restart", inst.ContainerName)
		WaitForCaddyAdmin(t, inst.AdminURL(), 30*time.Second)
		if _, err := a.Status(ctx); err != nil {
			t.Fatalf("status after out-of-band restart %d on the fixed container: %v", i+1, err)
		}
		serves(t, inst, "itapp.test")
	}
	mustDocker(t, "stop", inst.ContainerName)
	mustDocker(t, "start", inst.ContainerName)
	WaitForCaddyAdmin(t, inst.AdminURL(), 30*time.Second)
	if _, err := a.Status(ctx); err != nil {
		t.Fatalf("status after out-of-band stop/start: %v", err)
	}

	// --- 4. caddy_shack restart, and a corrupt autosave --------------------
	if err := a.Restart(ctx); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if _, err := a.Status(ctx); err != nil {
		t.Fatalf("status after caddy_shack restart: %v", err)
	}
	serves(t, inst, "itapp.test")

	mustDocker(t, "exec", inst.ContainerName, "sh", "-c", `echo '{"apps":{"nope":{}}}' > /config/caddy/autosave.json`)
	mustDocker(t, "restart", inst.ContainerName)
	if got := liveConfig(t, inst); got != bareConfig {
		t.Fatalf("an invalid autosave should fall back to the bare config, got %s", got)
	}
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start after corrupt autosave: %v", err)
	}
	if _, err := a.Status(ctx); err != nil {
		t.Fatalf("status after recovering from a corrupt autosave: %v", err)
	}
	serves(t, inst, "itapp.test")
}

package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// stubUFW points the ufw seam at a temp dir holding conf as ufw.conf ("" =
// ufw not installed).
func stubUFW(t *testing.T, conf string) {
	t.Helper()
	orig := ufwDir
	ufwDir = t.TempDir()
	if conf != "" {
		if err := os.WriteFile(filepath.Join(ufwDir, "ufw.conf"), []byte(conf), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { ufwDir = orig })
}

// shortLookups shrinks lookupTimeout so hang tests finish quickly.
func shortLookups(t *testing.T) {
	t.Helper()
	orig := lookupTimeout
	lookupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { lookupTimeout = orig })
}

func TestReadUFWStatus(t *testing.T) {
	cases := map[string]UFWStatus{
		"":                         {},
		"ENABLED=yes\n":            {Installed: true, Active: true},
		"# c\nENABLED=\"yes\"\n":   {Installed: true, Active: true},
		"ENABLED=no\nLOGLEVEL=low": {Installed: true},
	}
	for conf, want := range cases {
		stubUFW(t, conf)
		got, err := readUFWStatus()
		if err != nil || got != want {
			t.Errorf("conf %q: got %+v, %v want %+v", conf, got, err, want)
		}
	}
	if (UFWStatus{}).String() != "not installed" || (UFWStatus{Installed: true}).String() != "inactive" ||
		(UFWStatus{Installed: true, Active: true}).String() != "active" {
		t.Error("UFWStatus.String")
	}
}

func TestCheckDockerEngine(t *testing.T) {
	t.Run("desktop", func(t *testing.T) {
		r := checkDockerEngine(context.Background(), newDoctorApp(t, docker.NewFake(), "http://127.0.0.1:1"))
		if r.Status != SeverityPass || !strings.Contains(r.Summary, "Docker Desktop (desktop-or-vm)") {
			t.Errorf("%v %q", r.Status, r.Summary)
		}
	})
	t.Run("native reports ufw and relay", func(t *testing.T) {
		fake := docker.NewFake()
		useNativeLinux(t, fake)
		stubUFW(t, "ENABLED=yes\n")
		r := checkDockerEngine(context.Background(), newDoctorApp(t, fake, "http://127.0.0.1:1"))
		joined := strings.Join(r.Detail, "\n")
		if r.Status != SeverityPass || !strings.Contains(r.Summary, "native-linux") ||
			!strings.Contains(joined, "ufw: active (no rule needed") || !strings.Contains(joined, RelayContainerName) {
			t.Errorf("%v %q\n%s", r.Status, r.Summary, joined)
		}
	})
	t.Run("unknown warns with reason", func(t *testing.T) {
		fake := docker.NewFake()
		fake.EngineInfoErr = errors.New("docker info exploded")
		r := checkDockerEngine(context.Background(), newDoctorApp(t, fake, "http://127.0.0.1:1"))
		if r.Status != SeverityWarn || !strings.Contains(strings.Join(r.Detail, "\n"), "docker info exploded") {
			t.Errorf("%v %+v", r.Status, r.Detail)
		}
	})
	t.Run("daemon down skips", func(t *testing.T) {
		fake := docker.NewFake()
		fake.DaemonErr = docker.ErrDaemonUnreachable
		if r := checkDockerEngine(context.Background(), newDoctorApp(t, fake, "http://127.0.0.1:1")); r.Status != SeveritySkip {
			t.Errorf("%v", r.Status)
		}
	})
}

func TestCheckContainerConfigDesktopPasses(t *testing.T) {
	fake := docker.NewFake()
	seedCurrentDesktopCaddy(fake, docker.StateRunning)
	r := checkContainerConfig(context.Background(), newDoctorApp(t, fake, "http://127.0.0.1:1"))
	if r.Status != SeverityPass {
		t.Errorf("%v %q", r.Status, r.Summary)
	}
}

// TestCheckContainerConfigDesktopPreResumeOffersRecreate: a Docker Desktop
// container whose command predates --resume loses every route whenever it
// restarts outside caddy_shack, so doctor fails it and offers install.
func TestCheckContainerConfigDesktopPreResumeOffersRecreate(t *testing.T) {
	fake := docker.NewFake()
	seedCurrentDesktopCaddy(fake, docker.StateRunning)
	info := fake.Infos[CaddyContainerName]
	info.Command = []string{"sh", "-c", "exec caddy run --config /etc/caddy/Caddyfile --adapter caddyfile"}
	fake.Infos[CaddyContainerName] = info
	r := checkContainerConfig(context.Background(), newDoctorApp(t, fake, "http://127.0.0.1:1"))
	if r.Status != SeverityFail || r.Fix == nil {
		t.Fatalf("want FAIL with a fix, got %v %q fix=%v", r.Status, r.Summary, r.Fix)
	}
	if !strings.Contains(strings.Join(r.Detail, "\n"), "outdated container command") {
		t.Errorf("detail should name the command: %v", r.Detail)
	}
}

func TestCheckContainerConfigNativeLegacyOffersRecreate(t *testing.T) {
	f, fake, _ := nativeFixture(t, http.StatusOK)
	seedLegacyCaddy(fake, docker.StateRunning)
	r := checkContainerConfig(context.Background(), f.app)
	if r.Status != SeverityFail || r.Fix == nil || r.Fix.RequiresSudo {
		t.Fatalf("want fail with non-sudo fix: %+v", r)
	}
	if !strings.Contains(strings.Join(r.Detail, "\n"), "missing --add-host "+HostGatewayEntry) {
		t.Errorf("detail: %v", r.Detail)
	}
	if _, err := f.app.ApplyFix(context.Background(), r, func(string) bool { return true }); err != nil {
		t.Fatalf("ApplyFix: %v", err)
	}
	if len(fake.Removed) == 0 {
		t.Error("fix should recreate caddy_shack")
	}
}

func TestCheckContainerConfigNativeRelayStates(t *testing.T) {
	t.Run("stopped relay", func(t *testing.T) {
		f, fake, _ := nativeFixture(t, http.StatusOK)
		seedCurrentCaddy(fake)
		fake.Containers[RelayContainerName] = docker.StateStopped
		r := checkContainerConfig(context.Background(), f.app)
		if r.Status != SeverityFail || r.Fix == nil {
			t.Fatalf("%+v", r)
		}
		_, _ = f.app.ApplyFix(context.Background(), r, func(string) bool { return true })
		if st, _ := fake.ContainerState(RelayContainerName); st != docker.StateRunning {
			t.Errorf("fix did not start relay: %v", st)
		}
	})
	t.Run("misconfigured relay", func(t *testing.T) {
		f, fake, _ := nativeFixture(t, http.StatusOK)
		seedCurrentCaddy(fake)
		fake.Infos[RelayContainerName] = docker.ContainerInfo{NetworkMode: "bridge"}
		if r := checkContainerConfig(context.Background(), f.app); r.Status != SeverityFail || r.Fix == nil {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("socket missing", func(t *testing.T) {
		f, fake, _ := nativeFixture(t, http.StatusOK)
		seedCurrentCaddy(fake)
		fake.ExecResponses["caddy_shack:test -S "+RelaySocketPath] = docker.ExecResponse{Err: errors.New("exit 1")}
		r := checkContainerConfig(context.Background(), f.app)
		if r.Status != SeverityFail || r.Fix == nil {
			t.Fatalf("%+v", r)
		}
		_, _ = f.app.ApplyFix(context.Background(), r, func(string) bool { return true })
		if !strings.Contains(strings.Join(fake.Removed, ","), RelayContainerName) {
			t.Error("fix should recreate the relay")
		}
	})
	t.Run("all good", func(t *testing.T) {
		f, fake, _ := nativeFixture(t, http.StatusOK)
		seedCurrentCaddy(fake)
		fake.ExecResponses["caddy_shack:test -S "+RelaySocketPath] = docker.ExecResponse{}
		if r := checkContainerConfig(context.Background(), f.app); r.Status != SeverityPass {
			t.Errorf("%+v", r)
		}
	})
}

func TestCheckHostGateway(t *testing.T) {
	key := "caddy_shack:getent hosts host.docker.internal"
	t.Run("resolves", func(t *testing.T) {
		fake := docker.NewFake()
		fake.Containers[CaddyContainerName] = docker.StateRunning
		fake.ExecResponses[key] = docker.ExecResponse{Stdout: []byte("172.17.0.1        host.docker.internal  host.docker.internal\n")}
		r := checkHostGateway(context.Background(), newDoctorApp(t, fake, "http://127.0.0.1:1"))
		if r.Status != SeverityPass || !strings.Contains(r.Summary, "172.17.0.1") {
			t.Errorf("%v %q", r.Status, r.Summary)
		}
	})
	t.Run("hangs", func(t *testing.T) {
		shortLookups(t)
		fake := docker.NewFake()
		useNativeLinux(t, fake)
		fake.Containers[CaddyContainerName] = docker.StateRunning
		fake.ExecHang[key] = true
		r := checkHostGateway(context.Background(), newDoctorApp(t, fake, "http://127.0.0.1:1"))
		joined := strings.Join(r.Detail, "\n")
		if r.Status != SeverityFail || !strings.Contains(joined, "timed out") || !strings.Contains(joined, "--add-host") {
			t.Errorf("%v %q\n%s", r.Status, r.Summary, joined)
		}
	})
	t.Run("unknown engine only warns", func(t *testing.T) {
		fake := docker.NewFake()
		fake.EngineInfoErr = errors.New("x")
		fake.Containers[CaddyContainerName] = docker.StateRunning
		fake.ExecResponses[key] = docker.ExecResponse{Err: errors.New("exit 2")}
		if r := checkHostGateway(context.Background(), newDoctorApp(t, fake, "http://127.0.0.1:1")); r.Status != SeverityWarn {
			t.Errorf("%v", r.Status)
		}
	})
	t.Run("not running", func(t *testing.T) {
		if r := checkHostGateway(context.Background(), newDoctorApp(t, docker.NewFake(), "http://127.0.0.1:1")); r.Status != SeveritySkip {
			t.Errorf("%v", r.Status)
		}
	})
}

func TestCheckDNSNegativeLinuxPassWhenLookupFailsFast(t *testing.T) {
	shortLookups(t)
	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "getent" && !strings.HasSuffix(args[1], ".caddy-shack-probe.example") {
			t.Errorf("probe name: %v", args)
		}
		return nil, errors.New("exit 2") // NXDOMAIN
	}
	r := checkDNSNegativeLinux(context.Background(), nil, sh, noopShell)
	if r.Status != SeverityPass {
		t.Errorf("%v %q", r.Status, r.Summary)
	}
}

func TestCheckDNSNegativeLinuxFailsAndFixesWhenLookupHangs(t *testing.T) {
	shortLookups(t)
	sh := func(ctx context.Context, name string, _ ...string) ([]byte, error) {
		if name == "getent" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, errors.New("exit 1") // grep: no no-resolv line
	}
	r := checkDNSNegativeLinux(context.Background(), nil, sh, noopShell)
	if r.Status != SeverityFail || r.Fix == nil || !r.Fix.RequiresSudo {
		t.Fatalf("want fail with sudo fix: %+v", r)
	}
	joined := strings.Join(r.Detail, "\n")
	for _, want := range []string{"no 'no-resolv' line", "no-resolv", "| sudo tee " + linuxDNSMasqCaddyConfPath, "sudo systemctl restart dnsmasq && sudo resolvectl flush-caches"} {
		if !strings.Contains(joined, want) {
			t.Errorf("detail missing %q:\n%s", want, joined)
		}
	}

	var ttyCalls []shellCall
	if err := applyDNSLoopFixLinux(context.Background(), recordingShell(&ttyCalls)); err != nil {
		t.Fatalf("fix: %v", err)
	}
	if len(ttyCalls) != 1 || ttyCalls[0].Name != "sudo" || ttyCalls[0].Args[0] != "sh" {
		t.Fatalf("want one sudo sh -c, got %+v", ttyCalls)
	}
	script := ttyCalls[0].Args[2]
	for _, want := range []string{`no-resolv\n`, `local=/#/\n`, `address=/.test/127.0.0.1\n`, "> " + linuxDNSMasqCaddyConfPath, "systemctl restart dnsmasq"} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
}

func TestApplyDNSLoopFixLinuxPropagatesError(t *testing.T) {
	tty := func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("denied") }
	if err := applyDNSLoopFixLinux(context.Background(), tty); err == nil {
		t.Error("expected error")
	}
}

func TestLinuxDNSFixWritesNoResolv(t *testing.T) {
	var shCalls, ttyCalls []shellCall
	sh := recordingShell(&shCalls)
	if err := applyDNSFixLinux(context.Background(), sh, recordingShell(&ttyCalls)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ttyCalls[0].Args[2], `no-resolv\n`) {
		t.Errorf("the full .test setup must also write no-resolv:\n%s", ttyCalls[0].Args[2])
	}
	r := checkDNSLinux(context.Background(), nil,
		func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("x") }, noopShell)
	if !strings.Contains(strings.Join(r.Detail, "\n"), "no-resolv") {
		t.Errorf("manual steps must include no-resolv: %v", r.Detail)
	}
}

func TestCheckBackendsNativeProbesHostModeFromRelay(t *testing.T) {
	_, fake, _ := nativeFixture(t, http.StatusOK)
	dir := t.TempDir()
	sp := filepath.Join(dir, "state.json")
	_ = state.Save(sp, &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"mail": {Domain: "mail.test", Upstream: "host.docker.internal:8025"},
		"app":  {Domain: "app.test", Upstream: "app:80"},
		"down": {Domain: "down.test", Upstream: "host.docker.internal:9999"},
	}})
	seedCurrentCaddy(fake)
	fake.ExecResponses["caddy_shack_relay:nc -z -w 3 127.0.0.1 8025"] = docker.ExecResponse{}
	// An IPv6-only (::1) backend is found by the second probe.
	_ = state.Save(sp, &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"mail": {Domain: "mail.test", Upstream: "host.docker.internal:8025"},
		"vite": {Domain: "vite.test", Upstream: "host.docker.internal:5173"},
		"app":  {Domain: "app.test", Upstream: "app:80"},
		"down": {Domain: "down.test", Upstream: "host.docker.internal:9999"},
	}})
	fake.ExecResponses["caddy_shack_relay:nc -z -w 3 ::1 5173"] = docker.ExecResponse{}
	fake.ExecResponses["caddy_shack:nc -z -w 3 app 80"] = docker.ExecResponse{}
	a := NewWithPaths(Paths{StatePath: sp, LockPath: sp + ".lock"}, nil, fake)

	r := checkBackends(context.Background(), a)
	if r.Status != SeverityFail || len(r.Detail) != 1 ||
		r.Detail[0] != "down -> localhost:9999 (via caddy_shack_relay): unreachable" {
		t.Errorf("%v %q %v", r.Status, r.Summary, r.Detail)
	}
}

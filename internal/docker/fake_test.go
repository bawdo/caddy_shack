package docker

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFakeExecReturnsScriptedOutput(t *testing.T) {
	f := NewFake()
	f.ExecResponses = map[string]ExecResponse{
		"caddy_shack:cat /tmp/x": {Stdout: []byte("hello")},
	}
	out, err := f.Exec(context.Background(), "caddy_shack", []string{"cat", "/tmp/x"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if string(out) != "hello" {
		t.Errorf("output: got %q want %q", out, "hello")
	}
}

func TestFakeExecReturnsScriptedError(t *testing.T) {
	f := NewFake()
	want := errors.New("boom")
	f.ExecResponses = map[string]ExecResponse{
		"caddy_shack:false": {Err: want},
	}
	_, err := f.Exec(context.Background(), "caddy_shack", []string{"false"})
	if !errors.Is(err, want) {
		t.Errorf("Exec err: got %v want %v", err, want)
	}
}

func TestFakeExecUnknownReturnsError(t *testing.T) {
	f := NewFake()
	_, err := f.Exec(context.Background(), "caddy_shack", []string{"unscripted"})
	if err == nil {
		t.Fatal("expected error for unscripted command")
	}
}

func TestFakeImageIDReturnsConfiguredValue(t *testing.T) {
	f := NewFake()
	f.ImageIDs["caddy:2"] = "sha256:abc"
	id, err := f.ImageID("caddy:2")
	if err != nil {
		t.Fatalf("ImageID: %v", err)
	}
	if id != "sha256:abc" {
		t.Errorf("ImageID: got %q want %q", id, "sha256:abc")
	}
}

func TestFakeContainerImageIDReturnsConfiguredValue(t *testing.T) {
	f := NewFake()
	f.ContainerImageIDs["caddy_shack"] = "sha256:def"
	id, err := f.ContainerImageID("caddy_shack")
	if err != nil {
		t.Fatalf("ContainerImageID: %v", err)
	}
	if id != "sha256:def" {
		t.Errorf("ContainerImageID: got %q want %q", id, "sha256:def")
	}
}

func TestFakeInspectTracksCreateConnectRemove(t *testing.T) {
	f := NewFake()
	if _, err := f.InspectContainer("c"); err == nil {
		t.Fatal("absent container should not inspect")
	}
	_ = f.CreateAndStart(ContainerSpec{
		Name:       "c",
		ExtraHosts: []string{"h:host-gateway"},
		Volumes:    []VolumeMount{{VolumeName: "v", ContainerPath: "/v"}},
	})
	_ = f.ConnectNetwork("net", "c")
	info, err := f.InspectContainer("c")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if info.NetworkMode != "bridge" || len(info.ExtraHosts) != 1 || len(info.Mounts) != 1 {
		t.Errorf("info: %+v", info)
	}
	if strings.Join(info.Networks, ",") != "bridge,net" {
		t.Errorf("networks: %v", info.Networks)
	}
	_ = f.Remove("c")
	if _, err := f.InspectContainer("c"); err == nil {
		t.Error("removed container should not inspect")
	}
}

func TestFakeInspectSynthesisesBridgeForSeededContainer(t *testing.T) {
	f := NewFake()
	f.Containers["c"] = StateRunning
	_ = f.ConnectNetwork("net", "c")
	info, err := f.InspectContainer("c")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if info.NetworkMode != "bridge" || strings.Join(info.Networks, ",") != "bridge,net" {
		t.Errorf("info: %+v", info)
	}
}

func TestFakeDefaultsToDockerDesktop(t *testing.T) {
	f := NewFake()
	info, err := f.EngineInfo()
	if err != nil || info.OperatingSystem != "Docker Desktop" {
		t.Errorf("EngineInfo: %+v, %v", info, err)
	}
	f.EngineInfoErr = errors.New("x")
	if _, err := f.EngineInfo(); err == nil {
		t.Error("EngineInfoErr not honoured")
	}
	f.GatewayErr = errors.New("y")
	if _, err := f.BridgeGateway(); err == nil {
		t.Error("GatewayErr not honoured")
	}
}

// A refused Start, Stop or Remove leaves the container as it was, so app
// tests can assert what a failed docker call did not change.
func TestFakeRefusedLifecycleCallsChangeNothing(t *testing.T) {
	f := NewFake()
	f.Containers["c"] = StateStopped
	f.StartErr = errors.New("start")
	if err := f.Start("c"); !errors.Is(err, f.StartErr) || f.Containers["c"] != StateStopped {
		t.Errorf("Start: err=%v state=%v", err, f.Containers["c"])
	}
	f.Containers["c"] = StateRunning
	f.StopErr = errors.New("stop")
	if err := f.Stop("c"); !errors.Is(err, f.StopErr) || f.Containers["c"] != StateRunning {
		t.Errorf("Stop: err=%v state=%v", err, f.Containers["c"])
	}
	f.RemoveErr = errors.New("rm")
	if err := f.Remove("c"); !errors.Is(err, f.RemoveErr) {
		t.Errorf("Remove: err=%v", err)
	}
	if _, ok := f.Containers["c"]; !ok || len(f.Removed) != 0 {
		t.Errorf("refused Remove changed state: containers=%v removed=%v", f.Containers, f.Removed)
	}
}

func TestFakeEnsureVolumeErr(t *testing.T) {
	f := NewFake()
	f.EnsureVolumeErr = errors.New("volume")
	if err := f.EnsureVolume("v"); !errors.Is(err, f.EnsureVolumeErr) || f.Volumes["v"] {
		t.Errorf("err=%v recorded=%v", err, f.Volumes["v"])
	}
}

// ContainerStateErrs fails one name only; the others still report.
func TestFakeContainerStateErrsIsPerName(t *testing.T) {
	f := NewFake()
	f.Containers["a"] = StateRunning
	f.Containers["b"] = StateRunning
	f.ContainerStateErrs = map[string]error{"a": errors.New("inspect a")}
	if _, err := f.ContainerState("a"); err == nil {
		t.Error("a: want error")
	}
	if st, err := f.ContainerState("b"); err != nil || st != StateRunning {
		t.Errorf("b: %v %v", st, err)
	}
}

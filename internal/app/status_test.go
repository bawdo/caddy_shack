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

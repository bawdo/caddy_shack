package docker

import (
	"context"
	"errors"
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

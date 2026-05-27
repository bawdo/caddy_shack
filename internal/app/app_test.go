package app

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNewAppWithOverrides(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, nil)
	if a == nil {
		t.Fatal("nil app")
	}
	if a.paths.StatePath == "" {
		t.Error("StatePath not propagated")
	}
}

func TestDefaultPathsUnderUserConfigDir(t *testing.T) {
	p, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if !strings.HasSuffix(p.StatePath, "/caddy_shack/state.json") {
		t.Errorf("StatePath: %s", p.StatePath)
	}
	if !strings.HasSuffix(p.LockPath, "/caddy_shack/state.json.lock") {
		t.Errorf("LockPath: %s", p.LockPath)
	}
}

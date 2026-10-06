package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/state"
)

func TestListEmptyReturnsEmptySlice(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, nil)

	got, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d items, want 0", len(got))
	}
}

func TestListReturnsBackendsSortedByName(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"zeta":  {Domain: "zeta.test", Upstream: "127.0.0.1:3000"},
			"alpha": {Domain: "alpha.test", Upstream: "127.0.0.1:4000"},
		},
	}
	if err := state.Save(statePath, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	a := NewWithPaths(Paths{
		StatePath: statePath,
		LockPath:  statePath + ".lock",
	}, nil, nil)

	got, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Errorf("not sorted by name: %+v", got)
	}
}

func TestListJSONOutputIsValidArray(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := state.Save(statePath, &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"foo": {Domain: "foo.test", Upstream: "127.0.0.1:3000"},
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	a := NewWithPaths(Paths{
		StatePath: statePath,
		LockPath:  statePath + ".lock",
	}, nil, nil)

	got, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out, _ := json.Marshal(got)
	if !strings.Contains(string(out), `"foo"`) {
		t.Errorf("JSON missing foo: %s", out)
	}
}

func TestListReturnsErrStateCorruptOnBadFile(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := writeFile(t, statePath, "{not json"); err != nil {
		t.Fatal(err)
	}
	a := NewWithPaths(Paths{
		StatePath: statePath,
		LockPath:  statePath + ".lock",
	}, nil, nil)
	_, err := a.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("expected corrupt error, got %v", err)
	}
}

func writeFile(t *testing.T, path, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestListShowsLANDomain(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	s := &state.State{
		SchemaVersion: 2,
		Backends: map[string]state.Backend{
			"gamma": {Domain: "gamma.test", Upstream: "127.0.0.1:3000", LANDomain: "gamma.arrix.home.lan"},
		},
	}
	if err := state.Save(statePath, s); err != nil {
		t.Fatal(err)
	}
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"}, nil, nil)
	got, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].LANDomain != "gamma.arrix.home.lan" {
		t.Errorf("got %+v", got)
	}
}

package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingFileReturnsEmptyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.SchemaVersion != 1 {
		t.Errorf("SchemaVersion: got %d want 1", s.SchemaVersion)
	}
	if len(s.Backends) != 0 {
		t.Errorf("Backends: got %d want 0", len(s.Backends))
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := &State{
		SchemaVersion: 1,
		Backends: map[string]Backend{
			"myapp": {
				Domain:       "myapp.test",
				Upstream:     "127.0.0.1:3000",
				RegisteredAt: "2026-05-27T11:58:00+10:00",
			},
		},
	}
	if err := Save(path, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Backends["myapp"].Upstream != "127.0.0.1:3000" {
		t.Errorf("upstream not round-tripped: %+v", got)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := &State{SchemaVersion: 1, Backends: map[string]Backend{}}
	if err := Save(path, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// No leftover temp file.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp file leaked: %v", err)
	}
}

func TestLoadCorruptFileReturnsErrCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected ErrCorrupt, got nil")
	}
	if !IsCorrupt(err) {
		t.Errorf("not classified as corrupt: %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should mention path: %v", err)
	}
}

func TestLoadUnknownSchemaVersionReturnsErrCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":999,"backends":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if !IsCorrupt(err) {
		t.Errorf("unknown schema_version should be corrupt: %v", err)
	}
}

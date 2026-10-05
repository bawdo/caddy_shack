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
	if s.SchemaVersion != 2 {
		t.Errorf("SchemaVersion: got %d want 2", s.SchemaVersion)
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

func TestLoadUpgradesSchemaV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	v1 := `{"schema_version":1,"backends":{"a":{"domain":"a.test","upstream":"host.docker.internal:3000","registered_at":"x"}}}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.SchemaVersion != 2 {
		t.Errorf("SchemaVersion: got %d want 2", s.SchemaVersion)
	}
	if s.Backends["a"].Domain != "a.test" || s.Backends["a"].Upstream != "host.docker.internal:3000" {
		t.Errorf("backend a not intact: %+v", s.Backends["a"])
	}
	if s.LAN != nil {
		t.Errorf("LAN: got %+v want nil", s.LAN)
	}
	if err := Save(path, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"schema_version": 2`) {
		t.Errorf("saved file should be schema 2:\n%s", raw)
	}
}

func TestLoadRejectsUnknownSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":3,"backends":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !IsCorrupt(err) {
		t.Errorf("schema 3 should be corrupt: %v", err)
	}
}

func lanState() *State {
	return &State{
		SchemaVersion: SchemaVersion,
		Backends: map[string]Backend{
			"gamma": {
				Domain:       "gamma.test",
				Upstream:     "host.docker.internal:3000",
				RegisteredAt: "x",
				LANDomain:    "gamma.arrix.home.lan",
			},
		},
		LAN: &LAN{Enabled: true, IP: "192.168.1.12", Mode: LANModeDiscovered, Suffix: "arrix.home.lan"},
	}
}

func TestRoundTripLAN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := lanState()
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LAN == nil || *got.LAN != *want.LAN {
		t.Errorf("LAN: got %+v want %+v", got.LAN, want.LAN)
	}
	if got.Backends["gamma"] != want.Backends["gamma"] {
		t.Errorf("backend: got %+v want %+v", got.Backends["gamma"], want.Backends["gamma"])
	}
	if !got.LANEnabled() {
		t.Error("LANEnabled: got false want true")
	}
}

func TestLANEnabled(t *testing.T) {
	if (&State{}).LANEnabled() {
		t.Error("nil LAN should not be enabled")
	}
	if (&State{LAN: &LAN{}}).LANEnabled() {
		t.Error("disabled LAN should not be enabled")
	}
}

func TestCloneDeepCopiesLAN(t *testing.T) {
	orig := lanState()
	clone := orig.Clone()
	clone.LAN.IP = "10.0.0.1"
	if orig.LAN.IP != "192.168.1.12" {
		t.Errorf("original mutated: %s", orig.LAN.IP)
	}
	if (&State{}).Clone().LAN != nil {
		t.Error("clone of nil LAN should stay nil")
	}
}

func TestSaveOmitsLANWhenNil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := &State{Backends: map[string]Backend{"a": {Domain: "a.test", Upstream: "x:1", RegisteredAt: "x"}}}
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"lan"`) || strings.Contains(string(raw), `"lan_domain"`) {
		t.Errorf("LAN fields should be omitted:\n%s", raw)
	}
}

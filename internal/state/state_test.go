package state

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadMissingFileReturnsEmptyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion: got %d want %d", s.SchemaVersion, SchemaVersion)
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

func TestLoadMigratesV1FileToV2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	v1 := `{
  "schema_version": 1,
  "backends": {
    "hello": {
      "domain": "hello.test",
      "upstream": "host.docker.internal:3000",
      "registered_at": "2026-05-27T10:00:00+10:00"
    }
  }
}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatalf("write v1 fixture: %v", err)
	}

	s, err := Load(path)
	if err != nil {
		t.Fatalf("a v1 file must migrate, not error: %v", err)
	}
	if s.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", s.SchemaVersion, SchemaVersion)
	}
	if s.Certs == nil {
		t.Error("Certs must be non-nil after migration")
	}
	if len(s.Certs) != 0 {
		t.Errorf("migrated state must have no certs, got %d", len(s.Certs))
	}
	b, ok := s.Backends["hello"]
	if !ok {
		t.Fatal("migration lost the backends")
	}
	if b.Domain != "hello.test" || b.Upstream != "host.docker.internal:3000" {
		t.Errorf("migration corrupted backend: %+v", b)
	}
}

func TestLoadRejectsUnknownFutureSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"schema_version": 99}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := Load(path); !IsCorrupt(err) {
		t.Errorf("a future schema_version must be corrupt, got %v", err)
	}
}

func TestCertsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	want := Cert{
		Domain:   "alpha.arrix.dev",
		Subjects: []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
		Env:      "staging",
		Email:    "k@example.com",
		IssuedAt: "2026-09-08T10:00:00+10:00",
		NotAfter: "2026-12-07T10:00:00+10:00",
	}
	s := &State{
		SchemaVersion: SchemaVersion,
		Backends:      map[string]Backend{},
		Certs:         map[string]Cert{"alpha.arrix.dev": want},
	}
	if err := Save(path, s); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(got.Certs["alpha.arrix.dev"], want) {
		t.Errorf("cert round trip mismatch\ngot:  %+v\nwant: %+v", got.Certs["alpha.arrix.dev"], want)
	}
}

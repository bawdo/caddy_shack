package state

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzLoad checks that every readable byte sequence either loads into a
// usable State or fails as *ErrCorrupt (which the CLI maps to exit code 7),
// and that anything which loads survives a Save and Load round trip.
func FuzzLoad(f *testing.F) {
	for _, seed := range []string{
		// A schema 1 file written by an older version; must migrate.
		`{"schema_version":1,"backends":{"a":{"domain":"a.test","upstream":"host.docker.internal:3000","registered_at":"x"}}}`,
		// A schema 2 file with one backend and one cert.
		`{"schema_version":2,"backends":{"hello":{"domain":"hello.test","upstream":"host.docker.internal:3000","registered_at":"2026-05-27T10:00:00+10:00"}},"certs":{"alpha.arrix.dev":{"domain":"alpha.arrix.dev","subjects":["alpha.arrix.dev","*.alpha.arrix.dev"],"env":"staging","email":"k@example.com","issued_at":"2026-05-27T10:00:00+10:00","not_after":"2026-08-25T10:00:00+10:00"}}}`,
		`{}`,
		`null`,
		`{"schema_version":3}`,
		`{"schema_version":3,"backends":{}}`,
		`{"schema_version":999,"backends":{}}`,
		`{"schema_version": 99}`,
		`not json`,
		``,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "state.json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Load(path)
		if err != nil {
			if !IsCorrupt(err) {
				t.Fatalf("Load(%q) failed with %v, want *ErrCorrupt", data, err)
			}
			return
		}
		if s.SchemaVersion != SchemaVersion {
			t.Fatalf("Load(%q) schema_version = %d, want %d", data, s.SchemaVersion, SchemaVersion)
		}
		if s.Backends == nil || s.Certs == nil {
			t.Fatalf("Load(%q) left a nil map: backends=%v certs=%v", data, s.Backends == nil, s.Certs == nil)
		}
		second := filepath.Join(dir, "second.json")
		if err := Save(second, s); err != nil {
			t.Fatalf("Save after Load(%q): %v", data, err)
		}
		if _, err := Load(second); err != nil {
			t.Fatalf("Load of the saved copy of %q: %v", data, err)
		}
	})
}

package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SchemaVersion is bumped when the on-disk JSON shape changes in a
// non-backward-compatible way.
const SchemaVersion = 1

// Backend is one registered upstream service.
type Backend struct {
	Domain       string `json:"domain"`
	Upstream     string `json:"upstream"`
	RegisteredAt string `json:"registered_at"`
}

// State is the on-disk shape of state.json.
type State struct {
	SchemaVersion int                `json:"schema_version"`
	Backends      map[string]Backend `json:"backends"`
}

// Clone returns a deep copy of s, so a caller can build an updated registry
// while keeping the one it loaded.
func (s *State) Clone() *State {
	c := &State{SchemaVersion: s.SchemaVersion, Backends: make(map[string]Backend, len(s.Backends))}
	for k, v := range s.Backends {
		c.Backends[k] = v
	}
	return c
}

// ErrCorrupt classifies a load failure as "the file is unreadable or has an
// unknown schema; require manual remediation".
type ErrCorrupt struct {
	Path   string
	Reason error
}

func (e *ErrCorrupt) Error() string {
	return fmt.Sprintf("state: %s is corrupt: %v", e.Path, e.Reason)
}

func (e *ErrCorrupt) Unwrap() error { return e.Reason }

// IsCorrupt reports whether err (or any wrapped error) is *ErrCorrupt.
func IsCorrupt(err error) bool {
	var e *ErrCorrupt
	return errors.As(err, &e)
}

// Load reads state.json. A missing file returns an empty State (schema_version=1).
// A malformed or unknown-version file returns *ErrCorrupt.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{SchemaVersion: SchemaVersion, Backends: map[string]Backend{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, &ErrCorrupt{Path: path, Reason: err}
	}
	if s.SchemaVersion != SchemaVersion {
		return nil, &ErrCorrupt{
			Path:   path,
			Reason: fmt.Errorf("unknown schema_version %d (want %d)", s.SchemaVersion, SchemaVersion),
		}
	}
	if s.Backends == nil {
		s.Backends = map[string]Backend{}
	}
	return &s, nil
}

// Save writes state.json atomically (write tmp, fsync, rename).
func Save(path string, s *State) error {
	if s.SchemaVersion == 0 {
		s.SchemaVersion = SchemaVersion
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename tmp: %w", err)
	}
	return nil
}

package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SchemaVersion is bumped when the on-disk JSON shape changes in a
// non-backward-compatible way. Version 2 added the certs map and the LAN
// settings, both additive, so version 1 files are migrated forward on load.
const SchemaVersion = 2

// Backend is one registered upstream service.
type Backend struct {
	Domain       string `json:"domain"`
	Upstream     string `json:"upstream"`
	RegisteredAt string `json:"registered_at"`
	LANDomain    string `json:"lan_domain,omitempty"`
}

// LANMode records how the LAN address was chosen.
type LANMode string

const (
	LANModeDiscovered LANMode = "discovered"
	LANModePinned     LANMode = "pinned"
)

// LAN holds the LAN exposure settings.
type LAN struct {
	Enabled bool    `json:"enabled"`
	IP      string  `json:"ip"`
	Mode    LANMode `json:"mode"`
	Suffix  string  `json:"suffix"`
}

// Cert is the metadata for one externally-issued certificate. The material
// itself lives in the cert store on disk, not here.
type Cert struct {
	Domain   string   `json:"domain"`
	Subjects []string `json:"subjects"`
	// Env is "staging" or "production", naming the ACME directory the
	// certificate came from. Staging certificates are not publicly trusted.
	Env string `json:"env"`
	// Email is the ACME account contact used for this certificate, kept so
	// renewal does not have to ask for it again.
	Email    string `json:"email"`
	IssuedAt string `json:"issued_at"`
	NotAfter string `json:"not_after"`
}

// State is the on-disk shape of state.json.
type State struct {
	SchemaVersion int                `json:"schema_version"`
	Backends      map[string]Backend `json:"backends"`
	Certs         map[string]Cert    `json:"certs"`
	LAN           *LAN               `json:"lan,omitempty"`
}

// LANEnabled reports whether LAN exposure is switched on.
func (s *State) LANEnabled() bool { return s.LAN != nil && s.LAN.Enabled }

// Clone returns a deep copy of s, so a caller can build an updated registry
// while keeping the one it loaded.
func (s *State) Clone() *State {
	c := &State{
		SchemaVersion: s.SchemaVersion,
		Backends:      make(map[string]Backend, len(s.Backends)),
		Certs:         make(map[string]Cert, len(s.Certs)),
	}
	for k, v := range s.Backends {
		c.Backends[k] = v
	}
	for k, v := range s.Certs {
		v.Subjects = append([]string(nil), v.Subjects...)
		c.Certs[k] = v
	}
	if s.LAN != nil {
		lan := *s.LAN
		c.LAN = &lan
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

// Load reads state.json. A missing file returns an empty State (schema_version=2).
// A malformed or unknown-version file returns *ErrCorrupt.
// Version 1 files are automatically migrated to version 2.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{
			SchemaVersion: SchemaVersion,
			Backends:      map[string]Backend{},
			Certs:         map[string]Cert{},
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, &ErrCorrupt{Path: path, Reason: err}
	}
	// Version 1 files predate the certs map and the LAN settings. They are
	// structurally a subset of version 2, so migrate forward rather than
	// declaring them corrupt: an install in the field must not hit exit
	// code 7 on upgrade.
	switch s.SchemaVersion {
	case 1:
		s.SchemaVersion = SchemaVersion
	case SchemaVersion:
	default:
		return nil, &ErrCorrupt{
			Path:   path,
			Reason: fmt.Errorf("unknown schema_version %d (want 1 or %d)", s.SchemaVersion, SchemaVersion),
		}
	}
	if s.Backends == nil {
		s.Backends = map[string]Backend{}
	}
	if s.Certs == nil {
		s.Certs = map[string]Cert{}
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

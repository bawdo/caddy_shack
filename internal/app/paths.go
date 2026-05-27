package app

import (
	"fmt"
	"os"
	"path/filepath"
)

// Paths bundles every filesystem path the app touches.
type Paths struct {
	StatePath string
	LockPath  string
}

// DefaultPaths returns the macOS paths under os.UserConfigDir().
// On macOS that's "~/Library/Application Support".
func DefaultPaths() (Paths, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, fmt.Errorf("user config dir: %w", err)
	}
	dir := filepath.Join(base, "caddy_shack")
	return Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil
}

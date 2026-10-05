package app

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ufwDir is a seam over ufw's config directory. ufw.conf is world-readable,
// so its state can be read without sudo.
var ufwDir = "/etc/ufw"

// UFWStatus is what CaddyShack can learn about ufw without root.
type UFWStatus struct {
	Installed bool
	Active    bool
}

// String renders the status for doctor output.
func (s UFWStatus) String() string {
	switch {
	case !s.Installed:
		return "not installed"
	case s.Active:
		return "active"
	default:
		return "inactive"
	}
}

// readUFWStatus reports whether ufw is installed and enabled
// (ufw.conf ENABLED=yes). A missing ufw.conf means ufw is not installed.
func readUFWStatus() (UFWStatus, error) {
	conf, err := os.ReadFile(filepath.Join(ufwDir, "ufw.conf"))
	if errors.Is(err, fs.ErrNotExist) {
		return UFWStatus{}, nil
	}
	if err != nil {
		return UFWStatus{}, err
	}
	st := UFWStatus{Installed: true}
	for _, line := range strings.Split(string(conf), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && k == "ENABLED" {
			st.Active = strings.EqualFold(strings.Trim(v, `"'`), "yes")
		}
	}
	return st, nil
}

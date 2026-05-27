// Package version exposes build-time identity. Version, Commit, Tag, and
// Dirty are overridden via -ldflags at build time; the Makefile sets all
// four. When ldflags weren't applied (e.g. plain `go install ./`),
// Resolve() falls back to runtime.debug.ReadBuildInfo() which Go's
// toolchain populates automatically from the surrounding git repo.
package version

import "runtime/debug"

var (
	// Version is the human-facing version string (e.g. "v1.2.3",
	// "v1.2.3-dirty", or a short SHA). Defaults to "dev".
	Version = "dev"

	// Commit is the full git SHA at build time. Empty falls back to
	// runtime build info.
	Commit = ""

	// Tag is the git tag at build time, empty if HEAD is not tagged.
	// No runtime fallback (Go's debug.ReadBuildInfo doesn't expose tags).
	Tag = ""

	// Dirty is "true" when the working tree had uncommitted changes at
	// build time. Empty falls back to runtime build info.
	Dirty = ""
)

// Info bundles the resolved build identity printed by `caddy_shack version`.
type Info struct {
	Version string
	Commit  string
	Tag     string
	Dirty   bool
}

// Resolve returns the build identity, preferring ldflags-set values and
// filling missing Commit / Dirty from runtime.debug.ReadBuildInfo().
func Resolve() Info {
	out := Info{
		Version: Version,
		Commit:  Commit,
		Tag:     Tag,
		Dirty:   Dirty == "true",
	}
	if out.Commit != "" && Dirty != "" {
		return out
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return out
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if out.Commit == "" {
				out.Commit = s.Value
			}
		case "vcs.modified":
			if Dirty == "" && s.Value == "true" {
				out.Dirty = true
			}
		}
	}
	return out
}

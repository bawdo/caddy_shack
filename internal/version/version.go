// Package version exposes build-time identity. Version, Commit, Tag, and
// Dirty are overridden via -ldflags at build time; the Makefile sets all
// four. When ldflags weren't applied (e.g. plain `go install ./`),
// Resolve() falls back to runtime.debug.ReadBuildInfo(): Go's toolchain
// stamps the module version there for `go install module@version`, and the
// commit and dirty state when building inside the git repo.
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

// readBuildInfo is debug.ReadBuildInfo, a variable so tests can substitute
// a fake build info.
var readBuildInfo = debug.ReadBuildInfo

// Info bundles the resolved build identity printed by `caddy_shack version`.
type Info struct {
	Version string
	Commit  string
	Tag     string
	Dirty   bool
}

// Resolve returns the build identity, preferring ldflags-set values and
// filling missing Version / Commit / Dirty from runtime.debug.ReadBuildInfo().
// Version falls back to the main module version (set by `go install
// module@version`) only while it is still the default "dev" and the build
// info holds a real version, not "(devel)".
func Resolve() Info {
	out := Info{
		Version: Version,
		Commit:  Commit,
		Tag:     Tag,
		Dirty:   Dirty == "true",
	}
	if out.Version != "dev" && out.Commit != "" && Dirty != "" {
		return out
	}
	bi, ok := readBuildInfo()
	if !ok {
		return out
	}
	if out.Version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		out.Version = bi.Main.Version
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

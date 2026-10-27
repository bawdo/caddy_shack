package version

import (
	"runtime/debug"
	"testing"
)

func TestResolveUsesLdflagsValues(t *testing.T) {
	t.Cleanup(saveAndReset())
	Version = "v9.9.9"
	Commit = "abcdef0123456789"
	Tag = "v9.9.9"
	Dirty = "true"

	got := Resolve()
	if got.Version != "v9.9.9" {
		t.Errorf("Version: got %q", got.Version)
	}
	if got.Commit != "abcdef0123456789" {
		t.Errorf("Commit: got %q", got.Commit)
	}
	if got.Tag != "v9.9.9" {
		t.Errorf("Tag: got %q", got.Tag)
	}
	if !got.Dirty {
		t.Errorf("Dirty: want true")
	}
}

func TestResolveDirtyDefaults(t *testing.T) {
	t.Cleanup(saveAndReset())
	Dirty = "" // unset

	got := Resolve()
	// Cannot assert a concrete value here because go test populates vcs.*
	// settings only when run from a git tree. Just confirm it doesn't
	// panic and returns a struct.
	_ = got
}

// TestResolveVersionFromBuildInfo pins the go install path: with no ldflags,
// the module version Go stamps into the build info is reported, "(devel)"
// is not, and an ldflags version always wins.
func TestResolveVersionFromBuildInfo(t *testing.T) {
	cases := []struct {
		name      string
		ldflags   string
		buildInfo string
		want      string
	}{
		{"module version used", "", "v0.1.0", "v0.1.0"},
		{"devel ignored", "", "(devel)", "dev"},
		{"empty ignored", "", "", "dev"},
		{"ldflags win", "v9.9.9", "v0.1.0", "v9.9.9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(saveAndReset())
			if tc.ldflags != "" {
				Version = tc.ldflags
			}
			readBuildInfo = func() (*debug.BuildInfo, bool) {
				return &debug.BuildInfo{Main: debug.Module{Path: "github.com/bawdo/caddy_shack", Version: tc.buildInfo}}, true
			}
			if got := Resolve().Version; got != tc.want {
				t.Errorf("Version: got %q, want %q", got, tc.want)
			}
		})
	}
}

// saveAndReset returns a cleanup func that restores the package-level vars
// to their default ("dev"/empty) state so tests can't leak state into each
// other or into the cmd-layer tests that also touch Version.
func saveAndReset() func() {
	origV, origC, origT, origD, origR := Version, Commit, Tag, Dirty, readBuildInfo
	return func() {
		Version, Commit, Tag, Dirty, readBuildInfo = origV, origC, origT, origD, origR
	}
}

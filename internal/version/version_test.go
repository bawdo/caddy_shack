package version

import (
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

// saveAndReset returns a cleanup func that restores the package-level vars
// to their default ("dev"/empty) state so tests can't leak state into each
// other or into the cmd-layer tests that also touch Version.
func saveAndReset() func() {
	origV, origC, origT, origD := Version, Commit, Tag, Dirty
	return func() {
		Version, Commit, Tag, Dirty = origV, origC, origT, origD
	}
}

package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/version"
)

func TestVersionCommandPrintsVersion(t *testing.T) {
	defer restoreVersionVars(version.Version, version.Commit, version.Tag, version.Dirty)
	version.Version = "test-1.2.3"
	version.Commit = ""
	version.Tag = ""
	version.Dirty = ""

	got := runVersionCmd(t)
	if !strings.Contains(got, "caddy_shack test-1.2.3") {
		t.Errorf("output missing version line:\n%s", got)
	}
}

func TestVersionCommandPrintsCommit(t *testing.T) {
	defer restoreVersionVars(version.Version, version.Commit, version.Tag, version.Dirty)
	version.Version = "test-1.2.3"
	version.Commit = "abcdef0123456789abcdef0123456789abcdef01"
	version.Tag = ""
	version.Dirty = ""

	got := runVersionCmd(t)
	if !strings.Contains(got, "commit: abcdef0123456789abcdef0123456789abcdef01") {
		t.Errorf("output missing commit line:\n%s", got)
	}
	if strings.Contains(got, "(dirty)") {
		t.Errorf("dirty marker should be absent:\n%s", got)
	}
}

func TestVersionCommandPrintsDirty(t *testing.T) {
	defer restoreVersionVars(version.Version, version.Commit, version.Tag, version.Dirty)
	version.Version = "test"
	version.Commit = "abc1234"
	version.Tag = ""
	version.Dirty = "true"

	got := runVersionCmd(t)
	if !strings.Contains(got, "commit: abc1234 (dirty)") {
		t.Errorf("output missing dirty commit line:\n%s", got)
	}
}

func TestVersionCommandPrintsTag(t *testing.T) {
	defer restoreVersionVars(version.Version, version.Commit, version.Tag, version.Dirty)
	version.Version = "v1.0.0"
	version.Commit = "abc1234"
	version.Tag = "v1.0.0"
	version.Dirty = ""

	got := runVersionCmd(t)
	for _, want := range []string{"caddy_shack v1.0.0", "commit: abc1234", "tag:    v1.0.0"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestVersionCommandOmitsCommitAndTagWhenUnknown(t *testing.T) {
	defer restoreVersionVars(version.Version, version.Commit, version.Tag, version.Dirty)
	version.Version = "dev"
	version.Commit = ""
	version.Tag = ""
	version.Dirty = ""

	got := runVersionCmd(t)
	if strings.Contains(got, "commit:") {
		t.Errorf("commit line should be absent when unknown:\n%s", got)
	}
	if strings.Contains(got, "tag:") {
		t.Errorf("tag line should be absent when unknown:\n%s", got)
	}
}

func runVersionCmd(t *testing.T) string {
	t.Helper()
	root := newRootCmd()
	root.SetArgs([]string{"version"})
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute version: %v (stderr=%q)", err, errBuf.String())
	}
	return out.String()
}

func restoreVersionVars(v, c, tg, d string) {
	version.Version, version.Commit, version.Tag, version.Dirty = v, c, tg, d
}

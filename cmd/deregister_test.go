package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/app"
)

// TestDeregisterHasCascadeFlag confirms the --cascade boolean flag is wired.
func TestDeregisterHasCascadeFlag(t *testing.T) {
	c := newDeregisterCmd()
	f := c.Flags().Lookup("cascade")
	if f == nil {
		t.Fatal("expected --cascade flag on deregister command")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--cascade type: got %q want bool", f.Value.Type())
	}
}

func TestRenderDeregisterRemovedNoChildren(t *testing.T) {
	var out, errOut bytes.Buffer
	renderDeregister(&out, &errOut, "foo", app.DeregisterResult{Removed: true, ParentDomain: "foo.test"})
	if out.String() != "deregistered: foo\n" {
		t.Errorf("stdout: got %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr should be empty: got %q", errOut.String())
	}
}

func TestRenderDeregisterRemovedWithChildrenLeftWarns(t *testing.T) {
	var out, errOut bytes.Buffer
	renderDeregister(&out, &errOut, "foo", app.DeregisterResult{
		Removed: true, ParentDomain: "foo.test", ChildrenLeft: []string{"bar.foo", "baz.foo"},
	})
	if out.String() != "deregistered: foo\n" {
		t.Errorf("stdout: got %q", out.String())
	}
	if !strings.Contains(errOut.String(), "2 subdomain backend(s) still registered under foo.test") {
		t.Errorf("stderr warning missing: got %q", errOut.String())
	}
}

func TestRenderDeregisterCascadePrintsChildrenAndParent(t *testing.T) {
	var out, errOut bytes.Buffer
	renderDeregister(&out, &errOut, "foo", app.DeregisterResult{
		Removed: true, ParentDomain: "foo.test", Cascaded: []string{"bar.foo"},
	})
	got := out.String()
	if !strings.Contains(got, "deregistered: bar.foo\n") || !strings.Contains(got, "deregistered: foo\n") {
		t.Errorf("stdout should list child and parent: got %q", got)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr should be empty: got %q", errOut.String())
	}
}

func TestRenderDeregisterCascadeAbsentParentPrintsChildrenOnly(t *testing.T) {
	var out, errOut bytes.Buffer
	renderDeregister(&out, &errOut, "foo", app.DeregisterResult{
		Removed: false, ParentDomain: "foo.test", Cascaded: []string{"bar.foo"},
	})
	if out.String() != "deregistered: bar.foo\n" {
		t.Errorf("stdout: got %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr should be empty when children were cascaded: got %q", errOut.String())
	}
}

func TestRenderDeregisterUnknownName(t *testing.T) {
	var out, errOut bytes.Buffer
	renderDeregister(&out, &errOut, "foo", app.DeregisterResult{Removed: false})
	if out.Len() != 0 {
		t.Errorf("stdout should be empty: got %q", out.String())
	}
	if errOut.String() != "no such backend: foo\n" {
		t.Errorf("stderr: got %q", errOut.String())
	}
}

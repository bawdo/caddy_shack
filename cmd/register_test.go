package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestRegisterRejectsMissingUpstream confirms cobra's MarkFlagRequired wiring
// for register: invoking without --upstream must return an error from
// Execute, before any App is constructed. Catches accidental removal of the
// MarkFlagRequired call.
func TestRegisterRejectsMissingUpstream(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"register", "myapp"})
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error from missing --upstream, got nil")
	}
	if !strings.Contains(err.Error(), "upstream") {
		t.Errorf("error should mention upstream flag: %v", err)
	}
}

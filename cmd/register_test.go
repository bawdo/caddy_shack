package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestRegisterRejectsMissingPort confirms cobra's MarkFlagRequired wiring
// for register: invoking without --port must return an error from Execute,
// before any App is constructed. Catches accidental removal of the
// MarkFlagRequired call.
func TestRegisterRejectsMissingPort(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"register", "myapp"})
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error from missing --port, got nil")
	}
	if !strings.Contains(err.Error(), "port") {
		t.Errorf("error should mention port flag: %v", err)
	}
}

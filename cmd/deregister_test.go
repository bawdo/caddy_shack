package cmd

import "testing"

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

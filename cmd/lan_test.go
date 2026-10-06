package cmd

import "testing"

func TestLANCommandShape(t *testing.T) {
	c := newLANCmd()
	for _, name := range []string{"enable", "disable"} {
		if sub, _, err := c.Find([]string{name}); err != nil || sub.Name() != name {
			t.Fatalf("missing subcommand %s: %v", name, err)
		}
	}
	enable, _, _ := c.Find([]string{"enable"})
	for _, flag := range []string{"ip", "suffix", "yes"} {
		if enable.Flags().Lookup(flag) == nil {
			t.Errorf("enable missing --%s", flag)
		}
	}
}

package cmd

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func TestWithAppPassesAppToRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	called := false
	fn := withApp(func(_ *cobra.Command, args []string, a *app.App) error {
		called = true
		if a == nil {
			t.Error("expected a non-nil App")
		}
		if len(args) != 1 || args[0] != "x" {
			t.Errorf("args not passed through: %v", args)
		}
		return nil
	})
	if err := fn(&cobra.Command{}, []string{"x"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("run was not called")
	}
}

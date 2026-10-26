package cmd

import (
	"errors"
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

// breakConfigDir makes os.UserConfigDir, and so app.New, fail.
func breakConfigDir(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
}

func TestWithAppReturnsConstructorError(t *testing.T) {
	breakConfigDir(t)
	called := false
	fn := withApp(func(*cobra.Command, []string, *app.App) error {
		called = true
		return nil
	})
	err := fn(&cobra.Command{}, nil)
	if err == nil {
		t.Skip("app.New did not fail on this platform")
	}
	if called {
		t.Fatal("run must not be called when app.New fails")
	}
}

func TestDoctorFlagValidationBeatsConstructorError(t *testing.T) {
	breakConfigDir(t)
	if _, err := app.New(); err == nil {
		t.Skip("app.New did not fail on this platform")
	}
	for _, args := range [][]string{{"--yes"}, {"--json", "--fix"}} {
		c := newDoctorCmd()
		c.SetArgs(args)
		c.SilenceErrors, c.SilenceUsage = true, true
		err := c.Execute()
		if !errors.Is(err, app.ErrInvalidArgs) {
			t.Errorf("args %v: want ErrInvalidArgs, got %v", args, err)
		}
	}
}

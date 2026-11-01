package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/acme"
	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/docker"
)

// TestExitCodeContract pins the exit codes the root help promises scripts
// ("Exit codes (stable, script-friendly)" in root.go). Each row drives the
// real command tree into one documented condition and checks the code
// Execute would hand the shell. Changing any of these breaks callers' scripts.
func TestExitCodeContract(t *testing.T) {
	tests := []struct {
		name  string
		code  int
		want  error // sentinel the error must wrap; nil for 0 and 1
		setup func(t *testing.T, e *testEnv)
		stdin string
		args  []string
		slow  bool
	}{
		{
			name: "0 success",
			code: 0,
			args: []string{"status"},
		},
		{
			name:  "1 generic failure (status unhealthy)",
			code:  1,
			setup: func(t *testing.T, e *testEnv) { e.mustRun(t, "", "stop") },
			args:  []string{"status"},
		},
		{
			name: "2 invalid arguments (app validation)",
			code: 2,
			want: app.ErrInvalidArgs,
			args: []string{"register", "Bad_Name", "--port", "3000"},
		},
		{
			name: "2 invalid arguments (cmd flag validation)",
			code: 2,
			want: app.ErrInvalidArgs,
			args: []string{"doctor", "--yes"},
		},
		{
			name: "2 invalid arguments (unknown command)",
			code: 2,
			want: app.ErrInvalidArgs,
			args: []string{"nosuchcmd"},
		},
		{
			name: "2 invalid arguments (missing positional argument)",
			code: 2,
			want: app.ErrInvalidArgs,
			args: []string{"register"},
		},
		{
			name: "2 invalid arguments (missing required flag)",
			code: 2,
			want: app.ErrInvalidArgs,
			args: []string{"register", "myapp"},
		},
		{
			name: "2 invalid arguments (cert request without --email)",
			code: 2,
			want: app.ErrInvalidArgs,
			args: []string{"cert", "request", "alpha.arrix.dev"},
		},
		{
			name: "2 invalid arguments (unknown flag)",
			code: 2,
			want: app.ErrInvalidArgs,
			args: []string{"status", "--bogus"},
		},
		{
			// An error from inside a command body is not a usage error, even
			// a plain one cobra could not tell apart from its own.
			name: "1 command body failure is not reclassified as 2",
			code: 1,
			setup: func(t *testing.T, e *testEnv) {
				newApp = func() (*app.App, error) { return nil, errors.New("config dir unavailable") }
			},
			args: []string{"status"},
		},
		{
			name: "3 domain conflict",
			code: 3,
			want: app.ErrDomainConflict,
			setup: func(t *testing.T, e *testEnv) {
				e.mustRun(t, "", "register", "one", "--port", "3000", "--domain", "shared.test")
			},
			args: []string{"register", "two", "--port", "3001", "--domain", "shared.test"},
		},
		{
			name:  "4 docker daemon unreachable",
			code:  4,
			want:  app.ErrDockerUnreachable,
			setup: func(t *testing.T, e *testEnv) { e.dock.DaemonErr = docker.ErrDaemonUnreachable },
			args:  []string{"start"},
		},
		{
			name:  "5 caddy not running",
			code:  5,
			want:  app.ErrCaddyNotRunning,
			setup: func(t *testing.T, e *testEnv) { e.mustRun(t, "", "stop") },
			args:  []string{"register", "myapp", "--port", "3000"},
		},
		{
			name: "6 not installed",
			code: 6,
			want: app.ErrNotInstalled,
			setup: func(t *testing.T, e *testEnv) {
				delete(e.dock.Containers, app.CaddyContainerName)
			},
			args: []string{"start"},
		},
		{
			name:  "7 state file corrupt",
			code:  7,
			want:  app.ErrStateCorrupt,
			setup: writeCorruptState,
			args:  []string{"list"},
		},
		{
			name: "8 state lock held",
			code: 8,
			want: app.ErrLockContention,
			setup: func(t *testing.T, e *testEnv) {
				t.Cleanup(holdStateLock(t, e))
			},
			args: []string{"register", "myapp", "--port", "3000"},
			slow: true,
		},
		{
			name: "9 status drift",
			code: 9,
			want: app.ErrDrift,
			setup: func(t *testing.T, e *testEnv) {
				e.mustRun(t, "", "register", "myapp", "--port", "3000")
				e.caddy.wipe()
			},
			args: []string{"status"},
		},
		{
			name: "10 LAN not enabled",
			code: 10,
			want: app.ErrLANDisabled,
			args: []string{"register", "myapp", "--port", "3000", "--lan"},
		},
		{
			name: "11 certificate issuance failed",
			code: 11,
			want: app.ErrCertIssuance,
			setup: func(t *testing.T, e *testEnv) {
				e.setIssuer(acme.NewFakeIssuer(acme.Result{}, errors.New("urn:ietf:params:acme:error:rateLimited")))
			},
			stdin: "\n",
			args:  []string{"cert", "request", "alpha.arrix.dev", "--email", "k@example.com"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.slow && testing.Short() {
				t.Skip("waits out the 5s state lock timeout")
			}
			e := newInstalledEnv(t)
			if tc.setup != nil {
				tc.setup(t, e)
			}
			r := e.run(t, tc.stdin, tc.args...)
			wantCode(t, r, tc.code, tc.want)
		})
	}
}

// TestNoCommandHasPreRunHooks guards the assumption execute relies on: an
// error returned before any RunE starts came from cobra's own validation. A
// PreRun hook that fails would break that and exit 2 for a non-usage error.
func TestNoCommandHasPreRunHooks(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.PreRun != nil || c.PreRunE != nil || c.PersistentPreRun != nil || c.PersistentPreRunE != nil {
			t.Errorf("%q has a PreRun hook; execute would report its errors as exit 2", c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRootCmd())
}

// TestUsageErrorKeepsCobrasMessage checks that classifying a usage error as
// exit 2 changes only the code: the user still sees cobra's own message, once.
func TestUsageErrorKeepsCobrasMessage(t *testing.T) {
	e := newInstalledEnv(t)
	r := e.run(t, "", "status", "--bogus")
	wantCode(t, r, 2, app.ErrInvalidArgs)
	if got := strings.Count(r.stderr, "unknown flag: --bogus"); got != 1 {
		t.Errorf("stderr mentions the unknown flag %d times, want once:\n%s", got, r.stderr)
	}
	if strings.Contains(r.stderr, app.ErrInvalidArgs.Error()) {
		t.Errorf("stderr leaks the internal sentinel text:\n%s", r.stderr)
	}
}

// failingWriter refuses every write, standing in for a full disk or a
// closed pipe.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

// TestCompletionWriteFailureIsNotAUsageError covers the completion commands
// cobra adds itself during Execute. Their RunE must count as a command body
// starting, so a failed write exits 1, not 2.
func TestCompletionWriteFailureIsNotAUsageError(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"completion", "zsh"})
	root.SetOut(failingWriter{})
	root.SetErr(&strings.Builder{})
	err := execute(t.Context(), root)
	if err == nil {
		t.Fatal("completion zsh to a failing writer returned nil")
	}
	if code := app.ExitCode(err); code != 1 {
		t.Errorf("exit code = %d, want 1 (err: %v)", code, err)
	}
}

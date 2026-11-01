package cmd

import (
	"errors"
	"testing"

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

package app

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCodeLANDisabled(t *testing.T) {
	if got := ExitCode(ErrLANDisabled); got != 10 {
		t.Errorf("got %d want 10", got)
	}
}

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"generic", errors.New("boom"), 1},
		{"invalid args", fmt.Errorf("wrap: %w", ErrInvalidArgs), 2},
		{"domain conflict", fmt.Errorf("wrap: %w", ErrDomainConflict), 3},
		{"docker unreachable", fmt.Errorf("wrap: %w", ErrDockerUnreachable), 4},
		{"caddy not running", fmt.Errorf("wrap: %w", ErrCaddyNotRunning), 5},
		{"not installed", fmt.Errorf("wrap: %w", ErrNotInstalled), 6},
		{"state corrupt", fmt.Errorf("wrap: %w", ErrStateCorrupt), 7},
		{"lock contention", fmt.Errorf("wrap: %w", ErrLockContention), 8},
		{"drift", fmt.Errorf("wrap: %w", ErrDrift), 9},
		{"lan disabled", fmt.Errorf("wrap: %w", ErrLANDisabled), 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExitCode(tc.err)
			if got != tc.want {
				t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

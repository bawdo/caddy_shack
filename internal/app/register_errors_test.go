package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// TestRegisterFailuresWriteNothing: a register that fails before the commit
// leaves state.json and Caddy as they were, and maps to the exit code a
// script can branch on.
func TestRegisterFailuresWriteNothing(t *testing.T) {
	cases := []struct {
		name     string
		in       RegisterInput
		inject   func(t *testing.T, f *fixture)
		wantIs   error
		wantExit int
	}{
		{
			name: "inspect",
			in:   RegisterInput{Name: "new", Port: 4000},
			inject: func(_ *testing.T, f *fixture) {
				f.app.docker.(*docker.Fake).ContainerStateErrs = map[string]error{CaddyContainerName: errInjected}
			},
			wantIs:   errInjected,
			wantExit: 1,
		},
		{
			name:     "shared network for a container backend",
			in:       RegisterInput{Name: "new", Port: 4000, Container: "new-app"},
			inject:   func(_ *testing.T, f *fixture) { f.app.docker.(*docker.Fake).ConnectNetworkErr = errInjected },
			wantIs:   errInjected,
			wantExit: 1,
		},
		{
			name:     "lock held",
			in:       RegisterInput{Name: "new", Port: 4000},
			inject:   func(t *testing.T, f *fixture) { holdStateLock(t, f) },
			wantIs:   ErrLockContention,
			wantExit: 8,
		},
		{
			name: "corrupt state",
			in:   RegisterInput{Name: "new", Port: 4000},
			inject: func(t *testing.T, f *fixture) {
				if err := os.WriteFile(f.statePath, []byte("{not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantIs:   ErrStateCorrupt,
			wantExit: 7,
		},
		{
			name:     "certificate material unreadable",
			in:       RegisterInput{Name: "new", Port: 4000},
			inject:   withUnreadableCert,
			wantIs:   os.ErrPermission,
			wantExit: 1,
		},
		{
			name:     "admin API unreachable",
			in:       RegisterInput{Name: "new", Port: 4000},
			inject:   func(_ *testing.T, f *fixture) { f.caddySrv.Close() },
			wantIs:   ErrCaddyNotRunning,
			wantExit: 5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, http.StatusOK)
			seedState(t, f.statePath, "keep", "keep.test", "host.docker.internal:3000")
			tc.inject(t, f)
			before, _ := os.ReadFile(f.statePath)
			err := f.app.Register(context.Background(), tc.in)
			if !errors.Is(err, tc.wantIs) || ExitCode(err) != tc.wantExit {
				t.Fatalf("err = %v (exit %d), want %v (exit %d)", err, ExitCode(err), tc.wantIs, tc.wantExit)
			}
			if after, _ := os.ReadFile(f.statePath); string(before) != string(after) {
				t.Errorf("state written:\n before %s\n after  %s", before, after)
			}
			if f.loads() != 0 {
				t.Errorf("caddy pushed %d times", f.loads())
			}
		})
	}
}

// When the state write fails and the rollback push fails too, Caddy serves
// a backend the registry lacks; the error must say so and name the way out.
func TestRegisterReportsFailedRollback(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	seedState(t, f.statePath, "keep", "keep.test", "host.docker.internal:3000")
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && posts.Add(1) > 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"stub rejection"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	f.app.caddy = caddyapi.NewClient(srv.URL, time.Second)
	// The lock file must exist before its directory goes read-only.
	if l, err := state.AcquireLock(f.statePath+".lock", time.Second); err != nil {
		t.Fatal(err)
	} else {
		_ = l.Release()
	}
	readOnlyStateDir(t, f.statePath)

	err := f.app.Register(context.Background(), RegisterInput{Name: "new", Port: 4000})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"save state", "rolling caddy back failed", "caddy_shack sync"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
	if posts.Load() != 2 {
		t.Errorf("pushes = %d, want the change and one rollback attempt", posts.Load())
	}
	s, _ := state.Load(f.statePath)
	if _, ok := s.Backends["new"]; ok {
		t.Error("state records the backend")
	}
}

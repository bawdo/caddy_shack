package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
)

// fakeShells are buildCheckList's executors for tests. sh records every
// command and fails it, as if the tool were missing, so no real command
// ever runs; tty (the sudo path) fails the test if it is reached at all,
// since only an applied fix uses it.
type fakeShells struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeShells) sh(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	return nil, errors.New("fake shell: not run")
}

func (f *fakeShells) ran(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

// runCheckList builds the list for goos and runs every check against an
// absent container and LAN mode on with nothing registered, so the LAN
// firewall check reaches its per-OS probe while the LAN path and DNS checks
// have nothing to dial or look up. It returns the results in order.
func runCheckList(t *testing.T, goos string, opts DoctorOptions) ([]CheckResult, *fakeShells) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "state.json")
	saveState(t, p, lanState("192.168.1.12"))
	// A closed port, so the Caddy checks never reach a real instance on
	// the default admin port.
	closed := caddyapi.NewClient("http://127.0.0.1:1", time.Second)
	a := NewWithPaths(Paths{StatePath: p, LockPath: p + ".lock", CertStoreDir: t.TempDir()}, closed, docker.NewFake()).
		WithLANResolver(stubLAN{present: true})
	shells := &fakeShells{}
	tty := func(context.Context, string, ...string) ([]byte, error) {
		t.Error("the sudo executor ran while only checking")
		return nil, errors.New("fake tty: not run")
	}
	var results []CheckResult
	for _, c := range a.buildCheckList(opts, goos, shells.sh, tty) {
		results = append(results, c(context.Background()))
	}
	return results, shells
}

// TestBuildCheckListPerOS pins the order and the OS-specific members of the
// doctor list for both supported OSes on whatever OS runs the tests: Linux
// adds the negative-lookup DNS check, and each OS probes DNS and the
// firewall with its own tools. The backends check is last, or a skip
// placeholder with --no-backends.
func TestBuildCheckListPerOS(t *testing.T) {
	common := []string{"docker-daemon", "docker-engine", "port-availability", "container-health",
		"container-config", "shared-network", "host-gateway", "dns-test-tld"}
	tail := []string{"trust-store", "state-integrity", "config-drift", "lan-ip-present", "lan-forwarder",
		"lan-ip-drift", "lan-path", "lan-dns", "lan-firewall", "backends"}
	cases := []struct {
		goos string
		// extra sits between the .test DNS check and the trust store.
		extra []string
		// ran and notRan are command prefixes the fake shell must and must
		// not have seen.
		ran, notRan []string
	}{
		{
			goos:   "linux",
			extra:  []string{"dns-negative-lookups"},
			ran:    []string{"getent ahosts", "systemctl is-active ufw"},
			notRan: []string{"dscacheutil", "brew", "/usr/libexec/ApplicationFirewall/socketfilterfw"},
		},
		{
			goos:   "darwin",
			ran:    []string{"dscacheutil -q host", "/usr/libexec/ApplicationFirewall/socketfilterfw --getblockall"},
			notRan: []string{"getent", "systemctl"},
		},
	}
	for _, tc := range cases {
		for _, skip := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/skip-backends=%v", tc.goos, skip), func(t *testing.T) {
				results, shells := runCheckList(t, tc.goos, DoctorOptions{SkipBackends: skip})
				var names []string
				for _, r := range results {
					names = append(names, r.Name)
				}
				want := slices.Concat(common, tc.extra, tail)
				if !slices.Equal(names, want) {
					t.Errorf("checks:\n got  %q\n want %q", names, want)
				}
				last := results[len(results)-1]
				if skipped := last.Status == SeveritySkip && last.Summary == "skipped (--no-backends)"; skipped != skip {
					t.Errorf("backends result %+v with SkipBackends=%v", last, skip)
				}
				for _, p := range tc.ran {
					if !shells.ran(p) {
						t.Errorf("%s: %q not probed; calls %q", tc.goos, p, shells.calls)
					}
				}
				for _, p := range tc.notRan {
					if shells.ran(p) {
						t.Errorf("%s ran another OS's tool %q; calls %q", tc.goos, p, shells.calls)
					}
				}
			})
		}
	}
}

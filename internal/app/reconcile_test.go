package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

func twoBackends() *state.State {
	return &state.State{SchemaVersion: state.SchemaVersion, Backends: map[string]state.Backend{
		"alpha": {Domain: "alpha.test", Upstream: "host.docker.internal:3000"},
		"bravo": {Domain: "bravo.test", Upstream: "bravo:80"},
	}}
}

func TestCompareConfig(t *testing.T) {
	s := twoBackends()
	desired := caddyapi.Build(s, caddyapi.Options{})

	changed := twoBackends()
	changed.Backends["bravo"] = state.Backend{Domain: "bravo.test", Upstream: "bravo:8080"}
	withStranger := twoBackends()
	withStranger.Backends["stranger"] = state.Backend{Domain: "stranger.test", Upstream: "x:1"}
	onlyAlpha := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{"alpha": s.Backends["alpha"]}}
	empty := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{}}

	cases := []struct {
		name string
		reg  *state.State
		live []byte
		want Drift
	}{
		{"in sync", s, desired, Drift{InSync: true, HTTPApp: true, TLSApp: true}},
		{"bare caddy after restart", s, []byte(bareCaddyConfig),
			Drift{Missing: []string{"alpha", "bravo"}}},
		{"upstream changed", s, caddyapi.Build(changed, caddyapi.Options{}),
			Drift{HTTPApp: true, TLSApp: true, Changed: []string{"bravo"}}},
		{"route the registry does not know", s, caddyapi.Build(withStranger, caddyapi.Options{}),
			Drift{HTTPApp: true, TLSApp: true, Unknown: []string{"*.stranger.test", "stranger.test"}}},
		{"backend missing from live", s, caddyapi.Build(onlyAlpha, caddyapi.Options{}),
			Drift{HTTPApp: true, TLSApp: true, Missing: []string{"bravo"}}},
		{"nothing registered, bare caddy", empty, []byte(bareCaddyConfig), Drift{InSync: true}},
		{"nothing registered, live routes", empty, desired,
			Drift{HTTPApp: true, TLSApp: true, Unknown: []string{"*.alpha.test", "*.bravo.test", "alpha.test", "bravo.test"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := compareConfig(tc.reg, tc.live, caddyapi.Build(tc.reg, caddyapi.Options{}))
			if err != nil {
				t.Fatalf("compareConfig: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("\n got  %+v\n want %+v", got, tc.want)
			}
		})
	}
}

// TestCompareConfigPartiallyLiveIsChanged: a backend whose exact route is
// live but whose wildcard is gone is stale, not missing.
func TestCompareConfigPartiallyLiveIsChanged(t *testing.T) {
	s := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "x:1"},
	}}
	live := `{"apps":{"http":{"servers":{"srv0":{"routes":[` +
		`{"match":[{"host":["a.test"]}],"handle":[{"handler":"reverse_proxy","upstreams":[{"dial":"x:1"}]}]}]}}}}}`
	got, err := compareConfig(s, []byte(live), caddyapi.Build(s, caddyapi.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	if got.InSync || len(got.Missing) != 0 || !reflect.DeepEqual(got.Changed, []string{"a"}) {
		t.Errorf("got %+v", got)
	}
}

func TestCompareConfigRejectsUnparseableLiveConfig(t *testing.T) {
	if _, err := compareConfig(twoBackends(), []byte("not json"), caddyapi.Build(twoBackends(), caddyapi.Options{})); err == nil {
		t.Fatal("expected a parse error")
	}
}

func saveState(t *testing.T, path string, s *state.State) {
	t.Helper()
	if err := state.Save(path, s); err != nil {
		t.Fatalf("save state: %v", err)
	}
}

func TestStatusReportsDriftWithExitCode9(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	saveState(t, f.statePath, twoBackends())
	// live is the bare admin-only config: Caddy restarted and lost routes.

	rep, err := f.app.Status(context.Background())
	if !errors.Is(err, ErrDrift) || ExitCode(err) != 9 {
		t.Fatalf("want ErrDrift (exit 9), got %v (exit %d)", err, ExitCode(err))
	}
	if rep.Healthy {
		t.Error("drifted caddy must not be healthy")
	}
	if rep.Drift == nil || rep.Drift.HTTPApp || rep.Drift.TLSApp ||
		!reflect.DeepEqual(rep.Drift.Missing, []string{"alpha", "bravo"}) {
		t.Errorf("drift: %+v", rep.Drift)
	}
	if !rep.AdminReachable || rep.BackendCount != 2 {
		t.Errorf("report: %+v", rep)
	}
}

func TestStatusHealthyWhenLiveMatchesRegistry(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	saveState(t, f.statePath, twoBackends())
	f.syncLive(t)

	rep, err := f.app.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !rep.Healthy || rep.Drift == nil || !rep.Drift.InSync {
		t.Errorf("report: %+v drift=%+v", rep, rep.Drift)
	}
}

func TestStatusNeverWritesToCaddy(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	saveState(t, f.statePath, twoBackends())
	_, _ = f.app.Status(context.Background())
	if n := f.loads(); n != 0 {
		t.Errorf("status pushed config %d time(s); it must only read", n)
	}
}

func TestStatusUnhealthyWhenLiveConfigUnreadable(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	saveState(t, statePath, twoBackends())
	srv := newGarbageConfigServer(t)
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv, time.Second), fake)

	rep, err := a.Status(context.Background())
	if !errors.Is(err, ErrUnhealthy) {
		t.Fatalf("want ErrUnhealthy, got %v", err)
	}
	if rep.Healthy || rep.DriftError == "" || rep.Drift != nil {
		t.Errorf("report: %+v", rep)
	}
}

// newGarbageConfigServer answers every request 200 with a body that is not
// JSON.
func newGarbageConfigServer(t *testing.T) string {
	t.Helper()
	f := newFixture(t, http.StatusOK)
	f.setLive("not json")
	return f.caddySrv.URL
}

func TestRegisterIdenticalRestoresLostRoutes(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	in := RegisterInput{Name: "myapp", Port: 3000}
	if err := f.app.Register(context.Background(), in); err != nil {
		t.Fatalf("Register: %v", err)
	}
	f.setLive(bareCaddyConfig) // out-of-band restart

	before := f.loads()
	if err := f.app.Register(context.Background(), in); err != nil {
		t.Fatalf("re-Register: %v", err)
	}
	if f.loads() != before+1 {
		t.Fatalf("identical re-register should re-push a drifted Caddy once; loads %d -> %d", before, f.loads())
	}
	if !strings.Contains(string(f.live), "myapp.test") {
		t.Errorf("routes not restored: %s", f.live)
	}
}

// readOnlyStateDir makes the state file's directory unwritable so the next
// state.Save fails, and restores it on cleanup.
func readOnlyStateDir(t *testing.T, statePath string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := filepath.Dir(statePath)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

func TestRegisterRollsCaddyBackWhenStateWriteFails(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "keep", Port: 3000}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	readOnlyStateDir(t, f.statePath)

	err := f.app.Register(context.Background(), RegisterInput{Name: "new", Port: 4000})
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("want a save error mentioning the rollback, got %v", err)
	}
	if strings.Contains(string(f.live), "new.test") {
		t.Errorf("caddy still serves the unsaved backend: %s", f.live)
	}
	if !strings.Contains(string(f.live), "keep.test") {
		t.Errorf("rollback lost the registered backend: %s", f.live)
	}
	s, _ := state.Load(f.statePath)
	if _, ok := s.Backends["new"]; ok || len(s.Backends) != 1 {
		t.Errorf("state changed: %+v", s.Backends)
	}
}

func TestDeregisterRollsCaddyBackWhenStateWriteFails(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "keep", Port: 3000}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	readOnlyStateDir(t, f.statePath)

	if _, err := f.app.Deregister(context.Background(), "keep", false); err == nil {
		t.Fatal("want a save error")
	}
	if !strings.Contains(string(f.live), "keep.test") {
		t.Errorf("caddy dropped a backend the registry still has: %s", f.live)
	}
}

func TestDeregisterUnknownRestoresLostRoutes(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	saveState(t, f.statePath, twoBackends())

	res, err := f.app.Deregister(context.Background(), "nope", false)
	if err != nil || res.Removed {
		t.Fatalf("Deregister: %+v %v", res, err)
	}
	if !strings.Contains(string(f.live), "alpha.test") {
		t.Errorf("drifted caddy not reconciled: %s", f.live)
	}
}

func TestSync(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	saveState(t, f.statePath, twoBackends())

	rep, err := f.app.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !rep.Pushed || rep.Before.InSync || len(rep.Before.Missing) != 2 {
		t.Errorf("first sync: %+v", rep)
	}

	rep, err = f.app.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if rep.Pushed || !rep.Before.InSync {
		t.Errorf("second sync should be a no-op: %+v", rep)
	}
	if f.loads() != 1 {
		t.Errorf("loads: %d", f.loads())
	}
}

func TestSyncRemovesRoutesTheRegistryDoesNotHave(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	saveState(t, f.statePath, twoBackends())
	withStranger := twoBackends()
	withStranger.Backends["stranger"] = state.Backend{Domain: "stranger.test", Upstream: "x:1"}
	f.setLive(string(caddyapi.Build(withStranger, caddyapi.Options{})))

	if _, err := f.app.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if strings.Contains(string(f.live), "stranger.test") {
		t.Errorf("registry is authoritative; unknown route kept: %s", f.live)
	}
}

func TestSyncRequiresRunningContainer(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.app.docker.(*docker.Fake).Containers[CaddyContainerName] = docker.StateStopped
	if _, err := f.app.Sync(context.Background()); !errors.Is(err, ErrCaddyNotRunning) {
		t.Errorf("want ErrCaddyNotRunning, got %v", err)
	}
}

func TestSyncMapsUnreachableAdminToNotRunning(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.caddySrv.Close()
	if _, err := f.app.Sync(context.Background()); !errors.Is(err, ErrCaddyNotRunning) {
		t.Errorf("want ErrCaddyNotRunning, got %v", err)
	}
}

func TestSyncSurfacesRejectedConfig(t *testing.T) {
	f := newFixture(t, http.StatusBadRequest)
	saveState(t, f.statePath, twoBackends())
	_, err := f.app.Sync(context.Background())
	var rej *caddyapi.ErrRejected
	if !errors.As(err, &rej) || ExitCode(err) != 1 {
		t.Errorf("want a rejected-config error (exit 1), got %v", err)
	}
}

func TestInstallOnRunningContainerRestoresLostRoutes(t *testing.T) {
	fake := docker.NewFake()
	seedCurrentDesktopCaddy(fake, docker.StateRunning)
	f := newFixtureWith(t, http.StatusOK, fake)
	saveState(t, f.statePath, twoBackends())

	if err := f.app.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.Contains(string(f.live), "alpha.test") {
		t.Errorf("install left a drifted caddy: %s", f.live)
	}
}

// TestFreshInstallRestoresExistingRegistry: the container was removed
// (`docker rm`) but state.json still has backends; install recreates it
// and must serve them.
func TestFreshInstallRestoresExistingRegistry(t *testing.T) {
	f := newFixtureWith(t, http.StatusOK, docker.NewFake())
	saveState(t, f.statePath, twoBackends())

	if err := f.app.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.Contains(string(f.live), "bravo.test") {
		t.Errorf("fresh install ignored the registry: %s", f.live)
	}
}

func TestListMarksLiveness(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	reg := twoBackends()
	reg.Backends["charlie"] = state.Backend{Domain: "charlie.test", Upstream: "charlie:80"}
	saveState(t, f.statePath, reg)
	live := twoBackends()
	live.Backends["bravo"] = state.Backend{Domain: "bravo.test", Upstream: "bravo:9999"}
	f.setLive(string(caddyapi.Build(live, caddyapi.Options{})))

	items, err := f.app.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.Name] = it.Live
	}
	want := map[string]string{"alpha": LiveYes, "bravo": LiveStale, "charlie": LiveNo}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("live: got %v want %v", got, want)
	}
}

func TestListStillListsWhenCaddyUnreachable(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	saveState(t, f.statePath, twoBackends())
	f.caddySrv.Close()

	items, err := f.app.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 || items[0].Live != LiveUnknown || items[1].Live != LiveUnknown {
		t.Errorf("items: %+v", items)
	}
}

// TestIncidentRegressionUnit replays the 2026-10-05 incident against the
// stub: routes registered, Caddy restarted out of band and came back with
// only its admin config. status must report drift (exit 9) rather than
// healthy, list must show the routes as not live, and sync must restore
// them.
func TestIncidentRegressionUnit(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	ctx := context.Background()
	for _, in := range []RegisterInput{
		{Name: "arrix-gamma-ecs", Domain: "gamma.lan.arrix.dev", Port: 80, Container: "arrix-gamma-alb-proxy"},
		{Name: "mailpit", Port: 8025},
	} {
		if err := f.app.Register(ctx, in); err != nil {
			t.Fatalf("Register %s: %v", in.Name, err)
		}
	}
	if _, err := f.app.Status(ctx); err != nil {
		t.Fatalf("status before restart: %v", err)
	}

	f.setLive(bareCaddyConfig) // host reboot; caddy came back without --resume

	rep, err := f.app.Status(ctx)
	if ExitCode(err) != 9 || rep.Healthy {
		t.Fatalf("status after restart: healthy=%v err=%v (exit %d); want drift exit 9", rep.Healthy, err, ExitCode(err))
	}
	items, _ := f.app.List(ctx)
	for _, it := range items {
		if it.Live != LiveNo {
			t.Errorf("%s live=%s, want %s", it.Name, it.Live, LiveNo)
		}
	}

	if _, err := f.app.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, err := f.app.Status(ctx); err != nil {
		t.Fatalf("status after sync: %v", err)
	}
	if !strings.Contains(string(f.live), "gamma.lan.arrix.dev") {
		t.Errorf("route not restored: %s", f.live)
	}
}

// A LAN name is matched by both its srv-lan proxy route and the lan-http
// redirect; routesByHost must pick the proxy route every time.
func TestRoutesByHostSkipsStaticResponseRoutes(t *testing.T) {
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends:      map[string]state.Backend{"g": {Domain: "g.test", Upstream: "x:1", LANDomain: "g.lan"}},
		LAN:           &state.LAN{Enabled: true, IP: "192.168.1.12", Mode: state.LANModePinned, Suffix: "lan"},
	}
	var cfg map[string]any
	if err := json.Unmarshal(caddyapi.Build(s, caddyapi.Options{}), &cfg); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		r := routesByHost(cfg)["g.lan"].(map[string]any)
		h := r["handle"].([]any)[0].(map[string]any)
		if h["handler"] != "reverse_proxy" {
			t.Fatalf("g.lan mapped to %v", h["handler"])
		}
	}
}

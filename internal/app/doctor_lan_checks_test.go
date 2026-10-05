package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/lanip"
	"github.com/bawdo/caddy_shack/internal/state"
)

type docLANStub struct {
	ip      string
	present bool
}

func (d docLANStub) Discover() (lanip.Result, error) {
	return lanip.Result{IP: netip.MustParseAddr(d.ip)}, nil
}
func (d docLANStub) Present(netip.Addr) (bool, error) { return d.present, nil }

// lanDoctorApp builds an App over a saved state. A nil lan means LAN off.
func lanDoctorApp(t *testing.T, lan *state.LAN, backends map[string]state.Backend, r LANResolver) (*App, *docker.Fake) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if backends == nil {
		backends = map[string]state.Backend{}
	}
	if err := state.Save(p, &state.State{SchemaVersion: 2, Backends: backends, LAN: lan}); err != nil {
		t.Fatal(err)
	}
	fake := docker.NewFake()
	a := NewWithPaths(Paths{StatePath: p, LockPath: p + ".lock"}, nil, fake).WithLANResolver(r)
	a.notices = &strings.Builder{}
	return a, fake
}

func enabledLAN(mode state.LANMode, ip string) *state.LAN {
	return &state.LAN{Enabled: true, IP: ip, Mode: mode, Suffix: "lan"}
}

func TestCheckLANSkipsWhenOff(t *testing.T) {
	a, _ := lanDoctorApp(t, nil, nil, docLANStub{ip: "192.168.1.12", present: true})
	ctx := context.Background()
	sh := recordingShell(&[]shellCall{})
	for name, r := range map[string]CheckResult{
		"lan-ip-present": checkLANIPPresent(ctx, a),
		"lan-forwarder":  checkLANForwarder(ctx, a),
		"lan-ip-drift":   checkLANIPDrift(ctx, a),
		"lan-path":       checkLANPath(ctx, a),
		"lan-dns":        checkLANDNS(ctx, a),
		"lan-firewall":   checkLANFirewall(ctx, a, sh, "linux"),
	} {
		if r.Name != name || r.Status != SeveritySkip || r.Summary != "LAN mode off" {
			t.Errorf("%s: got %+v", name, r)
		}
	}
}

func TestCheckLANIPPresent(t *testing.T) {
	a, _ := lanDoctorApp(t, enabledLAN(state.LANModeDiscovered, "192.168.1.12"), nil, docLANStub{ip: "192.168.1.12", present: true})
	if r := checkLANIPPresent(context.Background(), a); r.Status != SeverityPass {
		t.Errorf("want pass, got %+v", r)
	}
}

func TestCheckLANIPPresentFailDiscoveredOffersFix(t *testing.T) {
	a, fake := lanDoctorApp(t, enabledLAN(state.LANModeDiscovered, "192.168.1.12"), nil, docLANStub{ip: "10.0.0.5", present: false})
	r := checkLANIPPresent(context.Background(), a)
	if r.Status != SeverityFail || r.Fix == nil {
		t.Fatalf("want fail with fix, got %+v", r)
	}
	// The fix rediscovers (10.0.0.5) and then ensureLAN; the stub reports
	// the address absent, so ensureLAN returns the LAN-down error, but the
	// stored IP must have been updated first.
	err := r.Fix.Apply(context.Background())
	if !errors.Is(err, ErrLANDown) {
		t.Errorf("want ErrLANDown, got %v", err)
	}
	s, _ := state.Load(a.paths.StatePath)
	if s.LAN.IP != "10.0.0.5" {
		t.Errorf("stored ip: %s", s.LAN.IP)
	}
	_ = fake
}

func TestCheckLANIPPresentFailPinnedNoFix(t *testing.T) {
	a, _ := lanDoctorApp(t, enabledLAN(state.LANModePinned, "192.168.1.12"), nil, docLANStub{ip: "10.0.0.5", present: false})
	r := checkLANIPPresent(context.Background(), a)
	if r.Status != SeverityFail || r.Fix != nil {
		t.Errorf("want fail without fix, got %+v", r)
	}
}

func TestCheckLANForwarderAbsentFailsAndFixCreates(t *testing.T) {
	a, fake := lanDoctorApp(t, enabledLAN(state.LANModePinned, "192.168.1.12"), nil, docLANStub{ip: "192.168.1.12", present: true})
	r := checkLANForwarder(context.Background(), a)
	if r.Status != SeverityFail || r.Fix == nil {
		t.Fatalf("want fail with fix, got %+v", r)
	}
	if err := r.Fix.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.Containers[a.inst.LANContainerName] != docker.StateRunning {
		t.Error("forwarder not running after fix")
	}
	if r := checkLANForwarder(context.Background(), a); r.Status != SeverityPass {
		t.Errorf("after fix want pass, got %+v", r)
	}
}

func TestCheckLANForwarderStoppedFails(t *testing.T) {
	a, fake := lanDoctorApp(t, enabledLAN(state.LANModePinned, "192.168.1.12"), nil, docLANStub{ip: "192.168.1.12", present: true})
	fake.Containers[a.inst.LANContainerName] = docker.StateStopped
	if r := checkLANForwarder(context.Background(), a); r.Status != SeverityFail {
		t.Errorf("want fail, got %+v", r)
	}
}

func TestCheckLANForwarderDriftFailsWithDetail(t *testing.T) {
	a, fake := lanDoctorApp(t, enabledLAN(state.LANModePinned, "192.168.1.12"), nil, docLANStub{ip: "192.168.1.12", present: true})
	if err := fake.CreateAndStart(lanContainerSpec(a.inst, "10.9.9.9")); err != nil {
		t.Fatal(err)
	}
	r := checkLANForwarder(context.Background(), a)
	if r.Status != SeverityFail || len(r.Detail) == 0 {
		t.Errorf("want fail with drift detail, got %+v", r)
	}
}

func TestCheckLANIPDrift(t *testing.T) {
	ctx := context.Background()
	a, _ := lanDoctorApp(t, enabledLAN(state.LANModeDiscovered, "192.168.1.12"), nil, docLANStub{ip: "192.168.1.12", present: true})
	if r := checkLANIPDrift(ctx, a); r.Status != SeverityPass {
		t.Errorf("same ip: want pass, got %+v", r)
	}

	a, _ = lanDoctorApp(t, enabledLAN(state.LANModeDiscovered, "192.168.1.12"), nil, docLANStub{ip: "10.0.0.5", present: true})
	r := checkLANIPDrift(ctx, a)
	if r.Status != SeverityWarn || r.Fix == nil {
		t.Fatalf("changed ip: want warn with fix, got %+v", r)
	}
	if err := r.Fix.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	s, _ := state.Load(a.paths.StatePath)
	if s.LAN.IP != "10.0.0.5" {
		t.Errorf("stored ip: %s", s.LAN.IP)
	}

	a, _ = lanDoctorApp(t, enabledLAN(state.LANModePinned, "192.168.1.12"), nil, docLANStub{ip: "10.0.0.5", present: true})
	if r := checkLANIPDrift(ctx, a); r.Status != SeveritySkip {
		t.Errorf("pinned: want skip, got %+v", r)
	}
}

// --- lan-path -------------------------------------------------------------

func testCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func lanPathApp(t *testing.T, backends map[string]state.Backend) *App {
	t.Helper()
	pemStr := testCAPEM(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"root_certificate": pemStr})
	}))
	t.Cleanup(srv.Close)
	a, fake := lanDoctorApp(t, enabledLAN(state.LANModePinned, "192.168.1.12"), backends, docLANStub{ip: "192.168.1.12", present: true})
	a.caddy = caddyapi.NewClient(srv.URL, time.Second)
	fake.Containers[a.inst.ContainerName] = docker.StateRunning
	return a
}

func stubDial(t *testing.T, fn func(addr string, cfg *tls.Config) error) *[]string {
	t.Helper()
	old := lanTLSDial
	t.Cleanup(func() { lanTLSDial = old })
	var seen []string
	lanTLSDial = func(_ context.Context, addr string, cfg *tls.Config) error {
		seen = append(seen, addr+"|"+cfg.ServerName)
		return fn(addr, cfg)
	}
	return &seen
}

var pathBackends = map[string]state.Backend{
	"b": {Domain: "b.test", Upstream: "x:1", LANDomain: "b.lan"},
	"a": {Domain: "a.test", Upstream: "x:2", LANDomain: "a.lan"},
}

func TestCheckLANPathPass(t *testing.T) {
	a := lanPathApp(t, pathBackends)
	seen := stubDial(t, func(_ string, cfg *tls.Config) error {
		if strings.HasSuffix(cfg.ServerName, ".test") {
			return errors.New("tls: handshake failure")
		}
		if cfg.RootCAs == nil {
			t.Error("positive dial has no RootCAs")
		}
		return nil
	})
	r := checkLANPath(context.Background(), a)
	if r.Status != SeverityPass {
		t.Fatalf("want pass, got %+v", r)
	}
	want := []string{"192.168.1.12:443|a.lan", "192.168.1.12:443|a.test"}
	if strings.Join(*seen, ",") != strings.Join(want, ",") {
		t.Errorf("dials: %v want %v", *seen, want)
	}
}

func TestCheckLANPathFailsWhenLANNameDoesNotVerify(t *testing.T) {
	a := lanPathApp(t, pathBackends)
	stubDial(t, func(_ string, cfg *tls.Config) error {
		if strings.HasSuffix(cfg.ServerName, ".lan") {
			return errors.New("x509: unknown authority")
		}
		return errors.New("refused")
	})
	if r := checkLANPath(context.Background(), a); r.Status != SeverityFail {
		t.Errorf("want fail, got %+v", r)
	}
}

func TestCheckLANPathFailsWhenTestNameServed(t *testing.T) {
	a := lanPathApp(t, pathBackends)
	stubDial(t, func(string, *tls.Config) error { return nil })
	r := checkLANPath(context.Background(), a)
	if r.Status != SeverityFail || r.Fix != nil {
		t.Errorf("want fail without fix, got %+v", r)
	}
}

func TestCheckLANPathSkipsHalves(t *testing.T) {
	// No LAN names: positive half skips, negative half still runs.
	a := lanPathApp(t, map[string]state.Backend{"a": {Domain: "a.test", Upstream: "x:1"}})
	seen := stubDial(t, func(string, *tls.Config) error { return errors.New("refused") })
	r := checkLANPath(context.Background(), a)
	if r.Status != SeverityPass || len(*seen) != 1 {
		t.Errorf("no names: got %+v dials=%v", r, *seen)
	}
	// No backends at all: whole check skips.
	a = lanPathApp(t, nil)
	seen = stubDial(t, func(string, *tls.Config) error { return nil })
	r = checkLANPath(context.Background(), a)
	if r.Status != SeveritySkip || len(*seen) != 0 {
		t.Errorf("no backends: got %+v dials=%v", r, *seen)
	}
}

// --- lan-dns --------------------------------------------------------------

func stubLookup(t *testing.T, m map[string][]string) {
	t.Helper()
	old := lanLookupHost
	t.Cleanup(func() { lanLookupHost = old })
	lanLookupHost = func(_ context.Context, host string) ([]string, error) {
		if v, ok := m[host]; ok {
			return v, nil
		}
		return nil, errors.New("no such host")
	}
}

func TestCheckLANDNS(t *testing.T) {
	ctx := context.Background()
	a, _ := lanDoctorApp(t, enabledLAN(state.LANModePinned, "192.168.1.12"), pathBackends, docLANStub{ip: "192.168.1.12", present: true})
	stubLookup(t, map[string][]string{"a.lan": {"192.168.1.12"}, "b.lan": {"192.168.1.12"}})
	if r := checkLANDNS(ctx, a); r.Status != SeverityPass {
		t.Errorf("want pass, got %+v", r)
	}
	stubLookup(t, map[string][]string{"a.lan": {"192.168.1.12"}, "b.lan": {"10.0.0.1"}})
	r := checkLANDNS(ctx, a)
	if r.Status != SeverityWarn || !strings.Contains(strings.Join(r.Detail, "\n"), "b.lan") || strings.Contains(strings.Join(r.Detail, "\n"), "a.lan") {
		t.Errorf("want warn naming b.lan only, got %+v", r)
	}
	a, _ = lanDoctorApp(t, enabledLAN(state.LANModePinned, "192.168.1.12"), nil, docLANStub{ip: "192.168.1.12", present: true})
	if r := checkLANDNS(ctx, a); r.Status != SeveritySkip {
		t.Errorf("no names: want skip, got %+v", r)
	}
}

// --- lan-firewall ---------------------------------------------------------

func TestCheckLANFirewall(t *testing.T) {
	ctx := context.Background()
	a, _ := lanDoctorApp(t, enabledLAN(state.LANModePinned, "192.168.1.12"), nil, docLANStub{ip: "192.168.1.12", present: true})
	resp := func(out string, err error) shellExecutor {
		return func(context.Context, string, ...string) ([]byte, error) { return []byte(out), err }
	}
	cases := []struct {
		name string
		goos string
		sh   shellExecutor
		want Severity
	}{
		{"linux ufw active", "linux", resp("active\n", nil), SeverityWarn},
		{"linux ufw inactive", "linux", resp("inactive\n", errors.New("exit 3")), SeverityPass},
		{"darwin enabled", "darwin", resp("Firewall is set to block all non-essential incoming connections (enabled)\n", nil), SeverityWarn},
		{"darwin disabled", "darwin", resp("Block all DISABLED!\n", nil), SeverityPass},
	}
	for _, c := range cases {
		r := checkLANFirewall(ctx, a, c.sh, c.goos)
		if r.Status != c.want || r.Fix != nil {
			t.Errorf("%s: got %+v", c.name, r)
		}
	}
	r := checkLANFirewall(ctx, a, resp("active\n", nil), "linux")
	if !strings.Contains(strings.Join(r.Detail, " "), "DOCKER-USER") {
		t.Errorf("detail should mention DOCKER-USER: %v", r.Detail)
	}
}

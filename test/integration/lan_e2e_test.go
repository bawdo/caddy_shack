//go:build integration

package integration

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/lanip"
	"github.com/bawdo/caddy_shack/internal/state"
)

// lanSuffix is the LAN suffix every LAN test enables. home.arpa is the
// reserved residential space, so no real name is ever shadowed.
const lanSuffix = "it.home.arpa"

// lanFixture is a running isolated install with LAN mode enabled and the
// echo backend registered as gamma with a LAN name.
type lanFixture struct {
	a         *app.App
	inst      app.Instance
	ip        string // the address the forwarder binds
	statePath string
	notices   func() string
}

// echoReply is what the echo backend saw.
type echoReply struct {
	Host       string `json:"host"`
	XFF        string `json:"xff"`
	RemoteAddr string `json:"remote_addr"`
}

// startEchoBackend serves the request's Host, X-Forwarded-For and RemoteAddr
// as JSON on every interface (Caddy dials it via host.docker.internal).
func startEchoBackend(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(echoReply{
				Host:       r.Host,
				XFF:        r.Header.Get("X-Forwarded-For"),
				RemoteAddr: r.RemoteAddr,
			})
		}),
		ReadHeaderTimeout: time.Second,
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

// stubLANResolver hands out a settable address and reports every loopback
// address present (all of 127.0.0.0/8 exists on Linux).
type stubLANResolver struct {
	mu sync.Mutex
	ip netip.Addr
}

func (s *stubLANResolver) set(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ip = netip.MustParseAddr(ip)
}

func (s *stubLANResolver) Discover() (lanip.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return lanip.Result{IP: s.ip}, nil
}

func (s *stubLANResolver) Present(ip netip.Addr) (bool, error) { return ip.IsLoopback(), nil }

// lanOptions tweaks setupLAN.
type lanOptions struct {
	resolver app.LANResolver // nil means the App's real resolver
	pin      bool            // pass the address as --ip instead of discovering
}

// setupLAN installs an isolated instance, enables LAN mode, registers gamma
// with a LAN name and waits until the LAN listener serves it. Skips when
// the machine has no discoverable LAN address.
func setupLAN(t *testing.T, opt lanOptions) *lanFixture {
	t.Helper()
	SkipUnlessDocker(t)
	ctx := context.Background()

	var ip string
	if opt.resolver != nil {
		res, err := opt.resolver.Discover()
		if err != nil {
			t.Fatalf("stub resolver: %v", err)
		}
		ip = res.IP.String()
	} else {
		res, err := lanip.Discover(lanip.System())
		if err != nil {
			t.Skipf("no LAN address to bind: %v", err)
		}
		ip = res.IP.String()
	}

	inst := lanTestInstance()
	cleanupLANInstance(t, inst)
	statePath := filepath.Join(t.TempDir(), "state.json")

	// The App writes notices to whatever os.Stderr was when it was built, so
	// point that at a file for the duration of the constructor.
	noticeFile, err := os.Create(filepath.Join(t.TempDir(), "notices.txt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = noticeFile.Close() })
	stderr := os.Stderr
	os.Stderr = noticeFile
	a := app.NewForInstance(app.Paths{StatePath: statePath, LockPath: statePath + ".lock"}, inst, nil, nil)
	os.Stderr = stderr
	if opt.resolver != nil {
		a.WithLANResolver(opt.resolver)
	}
	f := &lanFixture{
		a: a, inst: inst, ip: ip, statePath: statePath,
		notices: func() string {
			b, _ := os.ReadFile(noticeFile.Name())
			return string(b)
		},
	}
	t.Cleanup(func() {
		if n := f.notices(); n != "" {
			t.Logf("app notices:\n%s", n)
		}
	})

	if err := a.Install(ctx); err != nil {
		t.Fatalf("Install: %v", err)
	}
	in := app.LANEnableInput{Suffix: lanSuffix}
	if opt.pin {
		in.IP = ip
	}
	res, err := a.LANEnable(ctx, in)
	if err != nil {
		t.Fatalf("LANEnable: %v", err)
	}
	if res.IP != ip {
		t.Fatalf("LANEnable bound %s, want %s", res.IP, ip)
	}
	port := startEchoBackend(t)
	if err := a.Register(ctx, app.RegisterInput{Name: "gamma", Port: port, LAN: true}); err != nil {
		t.Fatalf("Register gamma --lan: %v", err)
	}
	f.lanGet(t, ip, "gamma."+lanSuffix, "gamma."+lanSuffix, nil, 45*time.Second)
	return f
}

// lanClient sends every request to ip:port over TLS with the given SNI,
// whatever the URL says. A nil roots skips verification.
func lanClient(ip string, port int, sni string, roots *x509.CertPool) *http.Client {
	tc := &tls.Config{ServerName: sni, RootCAs: roots, InsecureSkipVerify: roots == nil}
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
			},
			TLSClientConfig:   tc,
			DisableKeepAlives: true,
		},
		Timeout: 5 * time.Second,
	}
}

// lanGet GETs https://<host>/ through the forwarder at ip with SNI sni,
// retrying transport errors until timeout, and requires a 200 from the echo
// backend. It returns the response and what the backend saw.
func (f *lanFixture) lanGet(t *testing.T, ip, sni, host string, roots *x509.CertPool, timeout time.Duration) (*http.Response, echoReply) {
	t.Helper()
	resp, body := getWithRetry(t, lanClient(ip, f.inst.LANHTTPSPort, sni, roots), "https://"+host+"/", timeout)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("LAN https://%s (SNI %s) via %s:%d: %d %s", host, sni, ip, f.inst.LANHTTPSPort, resp.StatusCode, body)
	}
	var er echoReply
	if err := json.Unmarshal(body, &er); err != nil {
		t.Fatalf("LAN https://%s: not the echo backend: %v: %s", host, err, body)
	}
	return resp, er
}

// testServes requires https://<host>/ on the instance's loopback HTTPS port
// to return 200 from the echo backend.
func (f *lanFixture) testServes(t *testing.T, host string) {
	t.Helper()
	resp, body := getWithRetry(t, lanClient("127.0.0.1", f.inst.HTTPSPort, host, nil), "https://"+host+"/", 15*time.Second)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"host":"`+host+`"`) {
		t.Fatalf("https://%s on loopback: %d %s", host, resp.StatusCode, body)
	}
}

// lanHandshake runs a bare TLS handshake against the LAN HTTPS port.
func (f *lanFixture) lanHandshake(ip, sni string) (*tls.ConnectionState, error) {
	d := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", net.JoinHostPort(ip, strconv.Itoa(f.inst.LANHTTPSPort)),
		&tls.Config{ServerName: sni, InsecureSkipVerify: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	cs := conn.ConnectionState()
	return &cs, nil
}

// refusesConnections polls until nothing accepts TCP on ip:port.
func refusesConnections(t *testing.T, ip string, port int) {
	t.Helper()
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			return
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("%s still accepts connections", addr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestLANServesLANName(t *testing.T) {
	f := setupLAN(t, lanOptions{})
	name := "gamma." + lanSuffix

	rootPEM, err := f.a.ExportCA(context.Background(), false)
	if err != nil {
		t.Fatalf("ExportCA: %v", err)
	}
	block, _ := pem.Decode([]byte(rootPEM))
	if block == nil {
		t.Fatalf("ExportCA returned no PEM block:\n%s", rootPEM)
	}
	root, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse root: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)

	// The client verifies the served chain against the exported root.
	resp, er := f.lanGet(t, f.ip, name, name, roots, 15*time.Second)
	if er.Host != name {
		t.Errorf("backend saw Host %q, want %q", er.Host, name)
	}
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		t.Fatal("no peer certificates")
	}
	if err := resp.TLS.PeerCertificates[0].VerifyHostname(name); err != nil {
		t.Errorf("leaf: %v", err)
	}
}

func TestLANWildcardSubdomain(t *testing.T) {
	f := setupLAN(t, lanOptions{})
	name := "bar.gamma." + lanSuffix
	_, er := f.lanGet(t, f.ip, name, name, nil, 30*time.Second)
	if er.Host != name {
		t.Errorf("backend saw Host %q, want %q (gamma's upstream)", er.Host, name)
	}
}

// TestLANRefusesTestSNI: the LAN listener must not complete a handshake for
// a .test name. Per Ruling R2 this is not weakened if Caddy adds a catch-all
// connection policy: the failure is reported as a product bug.
func TestLANRefusesTestSNI(t *testing.T) {
	f := setupLAN(t, lanOptions{})
	for _, sni := range []string{"gamma.test", "bar.gamma.test"} {
		cs, err := f.lanHandshake(f.ip, sni)
		if err == nil {
			var subj []string
			if len(cs.PeerCertificates) > 0 {
				subj = cs.PeerCertificates[0].DNSNames
			}
			// Show what a request over that handshake gets back.
			resp, rerr := lanClient(f.ip, f.inst.LANHTTPSPort, sni, nil).Get("https://" + sni + "/")
			got := fmt.Sprintf("error %v", rerr)
			if rerr == nil {
				b, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				got = fmt.Sprintf("%d %s", resp.StatusCode, b)
			}
			t.Errorf("SNI %s: handshake completed on the LAN port (cert DNS names %v); request got %s", sni, subj, got)
			continue
		}
		t.Logf("SNI %s refused: %v", sni, err)
	}
}

func TestLANRefusesTestHost(t *testing.T) {
	f := setupLAN(t, lanOptions{})
	client := lanClient(f.ip, f.inst.LANHTTPSPort, "gamma."+lanSuffix, nil)
	resp, err := client.Get("https://gamma.test/")
	if err != nil {
		t.Logf("Host gamma.test with a LAN SNI: request failed (rejected): %v", err)
		return
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("Host gamma.test with SNI gamma.%s was served: %d %s", lanSuffix, resp.StatusCode, b)
	}
	t.Logf("Host gamma.test with a LAN SNI: %d", resp.StatusCode)
}

func TestLANHTTPRedirect(t *testing.T) {
	f := setupLAN(t, lanOptions{})
	addr := net.JoinHostPort(f.ip, strconv.Itoa(f.inst.LANHTTPPort))
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", addr)
			},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       5 * time.Second,
	}

	name := "gamma." + lanSuffix
	resp, body := getWithRetry(t, client, "http://"+name+"/x?y=1", 15*time.Second)
	_ = resp.Body.Close()
	want := "https://" + name + "/x?y=1"
	if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != want {
		t.Errorf("http://%s/x?y=1: %d Location %q, want 308 %q (%s)", name, resp.StatusCode, resp.Header.Get("Location"), want, body)
	}

	resp, body = getWithRetry(t, client, "http://gamma.test/x?y=1", 15*time.Second)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("http://gamma.test on the LAN port: %d Location %q, want 404 (%s)", resp.StatusCode, resp.Header.Get("Location"), body)
	}
}

func TestLANAbsentIPKeepsTestServing(t *testing.T) {
	f := setupLAN(t, lanOptions{pin: true})
	ctx := context.Background()

	// Simulate the pinned address vanishing from every interface.
	s, err := state.Load(f.statePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	s.LAN.IP = "192.0.2.1"
	if err := state.Save(f.statePath, s); err != nil {
		t.Fatalf("save state: %v", err)
	}

	if err := f.a.Restart(ctx); err != nil {
		t.Fatalf("Restart with the LAN IP gone should be a notice, got %v", err)
	}
	if n := f.notices(); !strings.Contains(n, "LAN access is off: 192.0.2.1") {
		t.Errorf("no LAN-down notice for 192.0.2.1; notices:\n%s", n)
	}
	f.testServes(t, "gamma.test")

	rep, err := f.a.Status(ctx)
	if err != nil {
		t.Errorf("Status: %v", err)
	}
	if rep.LAN == nil {
		t.Fatal("Status has no LAN block")
	}
	if rep.LAN.IPPresent {
		t.Errorf("Status LAN.IPPresent = true for 192.0.2.1")
	}
	st, err := docker.NewCLI().ContainerState(f.inst.LANContainerName)
	if err != nil {
		t.Fatalf("inspect forwarder: %v", err)
	}
	if st == docker.StateRunning {
		t.Errorf("forwarder is running after a restart with its IP gone (status says %q)", rep.LAN.ForwarderState)
	}
}

func TestLANRebindOnRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs 127.0.0.2 and 127.0.0.3, which exist by default only on Linux")
	}
	res := &stubLANResolver{}
	res.set("127.0.0.2")
	f := setupLAN(t, lanOptions{resolver: res})
	name := "gamma." + lanSuffix

	res.set("127.0.0.3")
	if err := f.a.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if n := f.notices(); !strings.Contains(n, "LAN IP changed 127.0.0.2 -> 127.0.0.3") {
		t.Errorf("no rebind notice; notices:\n%s", n)
	}
	f.lanGet(t, "127.0.0.3", name, name, nil, 45*time.Second)
	refusesConnections(t, "127.0.0.2", f.inst.LANHTTPSPort)
}

func TestLANClientIPSameHost(t *testing.T) {
	f := setupLAN(t, lanOptions{})
	name := "gamma." + lanSuffix
	_, er := f.lanGet(t, f.ip, name, name, nil, 15*time.Second)
	t.Logf("observed client IP: X-Forwarded-For=%q (forwarder bound to %s; backend RemoteAddr %s)", er.XFF, f.ip, er.RemoteAddr)

	out, err := exec.Command("docker", "network", "inspect", f.inst.NetworkName,
		"-f", "{{range .IPAM.Config}}{{.Subnet}} {{end}}").CombinedOutput()
	if err != nil {
		t.Fatalf("network inspect: %v: %s", err, out)
	}
	xff, err := netip.ParseAddr(strings.TrimSpace(strings.Split(er.XFF, ",")[0]))
	if err != nil {
		t.Fatalf("X-Forwarded-For %q is not an address: %v", er.XFF, err)
	}
	for _, sub := range strings.Fields(string(out)) {
		p, err := netip.ParsePrefix(sub)
		if err != nil {
			continue
		}
		t.Logf("docker network %s subnet: %s", f.inst.NetworkName, p)
		if p.Contains(xff) {
			t.Errorf("client IP %s is inside the Docker network subnet %s: the PROXY header did not carry the host address", xff, p)
		}
	}
}

func TestLANDisableStopsLAN(t *testing.T) {
	f := setupLAN(t, lanOptions{})
	if err := f.a.LANDisable(context.Background()); err != nil {
		t.Fatalf("LANDisable: %v", err)
	}
	refusesConnections(t, f.ip, f.inst.LANHTTPSPort)
	refusesConnections(t, f.ip, f.inst.LANHTTPPort)
	f.testServes(t, "gamma.test")
}

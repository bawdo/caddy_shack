package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/app"
)

func TestRenderStatusHumanHealthy(t *testing.T) {
	rep := app.StatusReport{
		ContainerState: "running",
		AdminReachable: true,
		BackendCount:   2,
		Healthy:        true,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	out := buf.String()
	for _, want := range []string{
		"container:       running",
		"admin reachable: true",
		"backends:        2",
		"healthy:         true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestRenderStatusHumanShowsVersionWhenKnown(t *testing.T) {
	rep := app.StatusReport{
		ContainerState: "running",
		AdminReachable: true,
		BackendCount:   1,
		Healthy:        true,
		CaddyVersion:   "v2.8.4",
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	if got := buf.String(); !strings.Contains(got, "version:         v2.8.4") {
		t.Errorf("missing version line\nfull:\n%s", got)
	}
}

func TestRenderStatusHumanOmitsVersionWhenUnknown(t *testing.T) {
	rep := app.StatusReport{
		ContainerState: "stopped",
		AdminReachable: false,
		Healthy:        false,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	if got := buf.String(); strings.Contains(got, "version:") {
		t.Errorf("version line should be absent when unknown\nfull:\n%s", got)
	}
}

func TestRenderStatusHumanUnhealthy(t *testing.T) {
	rep := app.StatusReport{
		ContainerState: "stopped",
		AdminReachable: false,
		BackendCount:   0,
		Healthy:        false,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	out := buf.String()
	for _, want := range []string{
		"container:       stopped",
		"admin reachable: false",
		"healthy:         false",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestRenderStatusJSON(t *testing.T) {
	rep := app.StatusReport{
		ContainerState: "running",
		AdminReachable: true,
		BackendCount:   3,
		Healthy:        true,
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, true)
	var got app.StatusReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw:\n%s", err, buf.String())
	}
	if got != rep {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, rep)
	}
}

func TestRenderStatusHumanShowsDrift(t *testing.T) {
	rep := app.StatusReport{
		ContainerState: "running",
		AdminReachable: true,
		BackendCount:   2,
		Drift:          &app.Drift{Missing: []string{"alpha", "bravo"}},
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	out := buf.String()
	for _, want := range []string{
		"config in sync:  false",
		"no http app",
		"no tls app",
		"registered but not live: alpha, bravo",
		"caddy_shack sync",
		"healthy:         false",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestRenderStatusJSONIncludesDrift(t *testing.T) {
	rep := app.StatusReport{ContainerState: "running", AdminReachable: true,
		Drift: &app.Drift{HTTPApp: true, TLSApp: true, Unknown: []string{"x.test"}}}
	var buf bytes.Buffer
	renderStatus(&buf, rep, true)
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	d, ok := m["drift"].(map[string]any)
	if !ok || d["in_sync"] != false || d["unknown"].([]any)[0] != "x.test" {
		t.Errorf("drift: %v", m["drift"])
	}
}

func TestStatusLANTextOutput(t *testing.T) {
	rep := app.StatusReport{
		ContainerState: "running", AdminReachable: true, Healthy: true,
		LAN: &app.LANStatus{Enabled: true, IP: "192.168.1.12", Mode: "discovered", Suffix: "arrix.home.lan",
			IPPresent: true, ForwarderState: "running", LANBackends: 3},
	}
	var buf bytes.Buffer
	renderStatus(&buf, rep, false)
	out := buf.String()
	for _, want := range []string{
		"LAN:        enabled (discovered) 192.168.1.12, suffix arrix.home.lan\n",
		"forwarder:  running\n",
		"drift:      none\n",
		"backends:   3 with LAN names\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\nfull:\n%s", want, out)
		}
	}

	rep.LAN.DiscoveredIP = "192.168.1.40"
	buf.Reset()
	renderStatus(&buf, rep, false)
	if !strings.Contains(buf.String(), "drift:      interface now has 192.168.1.40, run caddy_shack restart\n") {
		t.Errorf("drift line missing:\n%s", buf.String())
	}

	rep.LAN = &app.LANStatus{Enabled: false}
	buf.Reset()
	renderStatus(&buf, rep, false)
	if !strings.Contains(buf.String(), "LAN:        disabled\n") {
		t.Errorf("disabled line missing:\n%s", buf.String())
	}

	rep.LAN = nil
	buf.Reset()
	renderStatus(&buf, rep, false)
	if strings.Contains(buf.String(), "LAN:") {
		t.Errorf("unexpected LAN block:\n%s", buf.String())
	}
}

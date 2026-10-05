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

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

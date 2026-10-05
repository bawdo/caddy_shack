package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/app"
)

func TestRenderSyncInSync(t *testing.T) {
	var buf bytes.Buffer
	renderSync(&buf, app.SyncReport{Before: app.Drift{InSync: true}}, false)
	if !strings.Contains(buf.String(), "in sync") {
		t.Errorf("output: %q", buf.String())
	}
}

func TestRenderSyncPushed(t *testing.T) {
	var buf bytes.Buffer
	renderSync(&buf, app.SyncReport{Before: app.Drift{Missing: []string{"alpha"}}, Pushed: true}, false)
	out := buf.String()
	for _, want := range []string{"drift: registered but not live: alpha", "synced"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

func TestRenderSyncJSON(t *testing.T) {
	var buf bytes.Buffer
	renderSync(&buf, app.SyncReport{Pushed: true}, true)
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil || m["pushed"] != true {
		t.Errorf("json: %v %s", err, buf.String())
	}
}

func TestSyncCommandRegistered(t *testing.T) {
	for _, c := range newRootCmd().Commands() {
		if c.Name() == "sync" {
			return
		}
	}
	t.Fatal("sync command not registered on root")
}

func TestRootHelpDocumentsExitCode9(t *testing.T) {
	if !strings.Contains(newRootCmd().Long, "9  status only") {
		t.Error("exit code 9 missing from --help")
	}
}

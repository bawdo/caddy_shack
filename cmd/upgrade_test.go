package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/app"
)

func TestRenderUpgradeAlreadyCurrentWithVersion(t *testing.T) {
	var buf bytes.Buffer
	renderUpgrade(&buf, app.UpgradeReport{AlreadyCurrent: true, Version: "v2.8.4"})
	if got := buf.String(); !strings.Contains(got, "already up to date (v2.8.4)") {
		t.Errorf("output: %q", got)
	}
}

func TestRenderUpgradeAlreadyCurrentNoVersion(t *testing.T) {
	var buf bytes.Buffer
	renderUpgrade(&buf, app.UpgradeReport{AlreadyCurrent: true})
	got := buf.String()
	if !strings.Contains(got, "already up to date") || strings.Contains(got, "(") {
		t.Errorf("output: %q", got)
	}
}

func TestRenderUpgradeUpgradedWithVersion(t *testing.T) {
	var buf bytes.Buffer
	renderUpgrade(&buf, app.UpgradeReport{Version: "v2.8.4"})
	if got := buf.String(); !strings.Contains(got, "upgraded to v2.8.4") {
		t.Errorf("output: %q", got)
	}
}

func TestRenderUpgradeUpgradedNoVersion(t *testing.T) {
	var buf bytes.Buffer
	renderUpgrade(&buf, app.UpgradeReport{})
	if got := buf.String(); !strings.Contains(got, "run caddy_shack status") {
		t.Errorf("output: %q", got)
	}
}

func TestUpgradeCommandRegistered(t *testing.T) {
	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "upgrade" {
			return
		}
	}
	t.Fatal("upgrade command not registered on root")
}

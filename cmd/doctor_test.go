package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/app"
)

func TestDoctorYesWithoutFixIsError(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"doctor", "--yes"})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected --yes without --fix to error")
	}
	if !strings.Contains(err.Error(), "--yes requires --fix") {
		t.Errorf("error message: %v", err)
	}
}

func TestDoctorJSONWithFixIsError(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"doctor", "--json", "--fix"})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected --json --fix to error")
	}
	if !strings.Contains(err.Error(), "--json cannot be combined with --fix") {
		t.Errorf("error message: %v", err)
	}
}

func TestRenderDoctorHumanShowsEachCheck(t *testing.T) {
	rep := app.DoctorReport{
		SchemaVersion: 1,
		Overall:       app.SeverityFail,
		Checks: []app.CheckResult{
			{Name: "docker-daemon", Status: app.SeverityPass, Summary: "Docker reachable"},
			{Name: "dns-test-tld", Status: app.SeverityFail, Summary: ".test does not resolve",
				Detail: []string{"dnsmasq: not installed"},
				Fix:    &app.FixAction{Label: "install dnsmasq", RequiresSudo: true}},
			{Name: "backends", Status: app.SeveritySkip, Summary: "skipped (--no-backends)"},
		},
		Summary: app.DoctorReportSummary{Pass: 1, Fail: 1, Skip: 1},
	}

	var buf bytes.Buffer
	renderDoctorHuman(&buf, rep)
	out := buf.String()

	for _, want := range []string{
		"[PASS] docker-daemon",
		"Docker reachable",
		"[FAIL] dns-test-tld",
		".test does not resolve",
		"dnsmasq: not installed",
		"fix: install dnsmasq",
		"requires sudo",
		"[SKIP] backends",
		"1 issue", // summary line counts FAIL+WARN
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestRenderDoctorHumanAllHealthy(t *testing.T) {
	rep := app.DoctorReport{
		Overall: app.SeverityPass,
		Checks: []app.CheckResult{
			{Name: "docker-daemon", Status: app.SeverityPass, Summary: "Docker reachable"},
		},
		Summary: app.DoctorReportSummary{Pass: 1},
	}
	var buf bytes.Buffer
	renderDoctorHuman(&buf, rep)
	if !strings.Contains(buf.String(), "all checks passed") {
		t.Errorf("expected 'all checks passed' in healthy output, got:\n%s", buf.String())
	}
}

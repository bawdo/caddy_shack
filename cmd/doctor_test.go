package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/docker"
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

func TestRenderDoctorJSONSchema(t *testing.T) {
	rep := app.DoctorReport{
		SchemaVersion: 1,
		Overall:       app.SeverityWarn,
		Checks: []app.CheckResult{
			{Name: "x", Status: app.SeverityPass, Summary: "ok"},
		},
		Summary: app.DoctorReportSummary{Pass: 1, Warn: 0, Fail: 0, Skip: 0},
	}
	var buf bytes.Buffer
	renderDoctorJSON(&buf, rep)

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}
	if m["schema_version"].(float64) != 1 {
		t.Errorf("schema_version: %v", m["schema_version"])
	}
	if m["overall"] != "warn" {
		t.Errorf("overall: %v", m["overall"])
	}
	checks, ok := m["checks"].([]any)
	if !ok || len(checks) != 1 {
		t.Fatalf("checks: %v", m["checks"])
	}
	first, _ := checks[0].(map[string]any)
	for _, k := range []string{"name", "status", "summary", "fix_available", "fix_requires_sudo"} {
		if _, present := first[k]; !present {
			t.Errorf("missing key %q in check", k)
		}
	}
	summary := m["summary"].(map[string]any)
	for _, k := range []string{"pass", "warn", "fail", "skip"} {
		if _, present := summary[k]; !present {
			t.Errorf("missing summary key %q", k)
		}
	}
}

func TestRunFixLoopAppliesFixesWhenUserAcceptsY(t *testing.T) {
	dir := t.TempDir()
	a := app.NewWithPaths(app.Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	applied := 0
	rep := app.DoctorReport{
		Checks: []app.CheckResult{
			{Name: "x", Status: app.SeverityFail, Fix: &app.FixAction{
				Label: "do the thing",
				Apply: func(context.Context) error { applied++; return nil },
			}},
		},
	}

	cmd := newDoctorCmd()
	cmd.SetIn(strings.NewReader("y\n"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())

	if err := runFixLoop(cmd, a, rep, false); err != nil {
		t.Fatalf("runFixLoop: %v", err)
	}
	if applied != 1 {
		t.Errorf("expected one fix applied, got %d", applied)
	}
	if !strings.Contains(buf.String(), "[FIXED] x") {
		t.Errorf("expected [FIXED] x in output:\n%s", buf.String())
	}
}

func TestRunFixLoopSkipsOnUserDeclineN(t *testing.T) {
	dir := t.TempDir()
	a := app.NewWithPaths(app.Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	applied := 0
	rep := app.DoctorReport{
		Checks: []app.CheckResult{
			{Name: "x", Status: app.SeverityFail, Fix: &app.FixAction{
				Apply: func(context.Context) error { applied++; return nil },
			}},
		},
	}

	cmd := newDoctorCmd()
	cmd.SetIn(strings.NewReader("n\n"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())

	_ = runFixLoop(cmd, a, rep, false)
	if applied != 0 {
		t.Errorf("expected zero fixes, got %d", applied)
	}
	if !strings.Contains(buf.String(), "skipped") {
		t.Errorf("expected 'skipped' in output:\n%s", buf.String())
	}
}

func TestRunFixLoopReturnsErrorWhenAnyFixFails(t *testing.T) {
	dir := t.TempDir()
	a := app.NewWithPaths(app.Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	want := errors.New("boom")
	rep := app.DoctorReport{
		Checks: []app.CheckResult{
			{Name: "x", Status: app.SeverityFail, Fix: &app.FixAction{
				Apply: func(context.Context) error { return want },
			}},
		},
	}
	cmd := newDoctorCmd()
	cmd.SetIn(strings.NewReader("y\n"))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(context.Background())

	err := runFixLoop(cmd, a, rep, false)
	if err == nil {
		t.Fatal("expected error when fix fails")
	}
}

func TestRunFixLoopYesModeAppliesWithoutPrompt(t *testing.T) {
	dir := t.TempDir()
	a := app.NewWithPaths(app.Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	applied := 0
	rep := app.DoctorReport{
		Checks: []app.CheckResult{
			{Name: "x", Status: app.SeverityFail, Fix: &app.FixAction{
				Apply: func(context.Context) error { applied++; return nil },
			}},
		},
	}

	cmd := newDoctorCmd()
	// No stdin set - --yes means no prompt is read.
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())

	if err := runFixLoop(cmd, a, rep, true); err != nil {
		t.Fatalf("runFixLoop: %v", err)
	}
	if applied != 1 {
		t.Errorf("expected one fix applied, got %d", applied)
	}
}

// stubExec replaces the sudo preflight executor with a configurable result.
type stubExec struct{ err error }

func (s stubExec) Run() error { return s.err }

func TestRunFixLoopYesModeMarksSudoFixFailedWhenSudoCachedFalse(t *testing.T) {
	prev := execCommandContext
	defer func() { execCommandContext = prev }()
	execCommandContext = func(context.Context, string, ...string) runnable {
		return stubExec{err: errors.New("sudo: a password is required")}
	}

	dir := t.TempDir()
	a := app.NewWithPaths(app.Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	applied := 0
	rep := app.DoctorReport{
		Checks: []app.CheckResult{
			{Name: "needs-sudo", Status: app.SeverityFail, Fix: &app.FixAction{
				RequiresSudo: true,
				Apply:        func(context.Context) error { applied++; return nil },
			}},
		},
	}

	cmd := newDoctorCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())

	err := runFixLoop(cmd, a, rep, true)
	if err == nil {
		t.Fatal("expected error when sudo preflight fails")
	}
	if applied != 0 {
		t.Errorf("Apply must not be invoked when sudo preflight fails (got applied=%d)", applied)
	}
	if !strings.Contains(buf.String(), "[FIX-FAILED] needs-sudo") {
		t.Errorf("expected [FIX-FAILED] in output:\n%s", buf.String())
	}
}

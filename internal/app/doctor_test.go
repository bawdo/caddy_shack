package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bawdo/caddy_shack/internal/docker"
)

func TestSeverityMarshalsAsLowercaseString(t *testing.T) {
	cases := []struct {
		sev  Severity
		want string
	}{
		{SeverityPass, `"pass"`},
		{SeverityWarn, `"warn"`},
		{SeverityFail, `"fail"`},
		{SeveritySkip, `"skip"`},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.sev)
		if err != nil {
			t.Fatalf("marshal %v: %v", c.sev, err)
		}
		if string(got) != c.want {
			t.Errorf("severity %v: got %s want %s", c.sev, got, c.want)
		}
	}
}

func TestCheckResultJSONShape(t *testing.T) {
	r := CheckResult{
		Name:    "dns-test-tld",
		Status:  SeverityFail,
		Summary: ".test does not resolve",
		Detail:  []string{"dnsmasq: not installed"},
		Fix: &FixAction{
			Label:        "install dnsmasq",
			RequiresSudo: true,
		},
	}
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["name"] != "dns-test-tld" {
		t.Errorf("name: %v", m["name"])
	}
	if m["status"] != "fail" {
		t.Errorf("status: %v", m["status"])
	}
	if m["fix_available"] != true {
		t.Errorf("fix_available: %v", m["fix_available"])
	}
	if m["fix_requires_sudo"] != true {
		t.Errorf("fix_requires_sudo: %v", m["fix_requires_sudo"])
	}
	if _, present := m["apply"]; present {
		t.Errorf("Apply closure must not appear in JSON output")
	}
}

func TestDoctorReportOverallAndSummaryCounting(t *testing.T) {
	rep := DoctorReport{
		Checks: []CheckResult{
			{Name: "a", Status: SeverityPass},
			{Name: "b", Status: SeverityWarn},
			{Name: "c", Status: SeverityFail},
			{Name: "d", Status: SeveritySkip},
		},
	}
	finaliseDoctorReport(&rep)
	if rep.Overall != SeverityFail {
		t.Errorf("overall: got %v want fail", rep.Overall)
	}
	if rep.Summary != (DoctorReportSummary{Pass: 1, Warn: 1, Fail: 1, Skip: 1}) {
		t.Errorf("summary: %+v", rep.Summary)
	}
}

func TestDoctorReportOverallWarnWhenNoFail(t *testing.T) {
	rep := DoctorReport{
		Checks: []CheckResult{
			{Name: "a", Status: SeverityPass},
			{Name: "b", Status: SeverityWarn},
		},
	}
	finaliseDoctorReport(&rep)
	if rep.Overall != SeverityWarn {
		t.Errorf("overall: got %v want warn", rep.Overall)
	}
}

func TestDoctorReturnsCtxErrIfCancelled(t *testing.T) {
	fake := docker.NewFake()
	a := NewWithPaths(Paths{}, nil, fake)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.Doctor(ctx, DoctorOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestApplyFixSkippedWhenNoFix(t *testing.T) {
	a := NewWithPaths(Paths{}, nil, docker.NewFake())
	r := CheckResult{Name: "x", Status: SeverityFail}
	out, err := a.ApplyFix(context.Background(), r, func(string) bool { return true })
	if err != nil {
		t.Fatalf("ApplyFix: %v", err)
	}
	if out != FixSkipped {
		t.Errorf("outcome: got %v want FixSkipped", out)
	}
}

func TestApplyFixSkippedWhenPromptDeclines(t *testing.T) {
	a := NewWithPaths(Paths{}, nil, docker.NewFake())
	called := false
	r := CheckResult{Fix: &FixAction{Apply: func(context.Context) error { called = true; return nil }}}
	out, _ := a.ApplyFix(context.Background(), r, func(string) bool { return false })
	if out != FixSkipped {
		t.Errorf("outcome: %v", out)
	}
	if called {
		t.Errorf("Apply must not be invoked when prompt declines")
	}
}

func TestApplyFixAppliedAndFailedPaths(t *testing.T) {
	a := NewWithPaths(Paths{}, nil, docker.NewFake())

	okResult := CheckResult{Name: "ok", Fix: &FixAction{Apply: func(context.Context) error { return nil }}}
	out, err := a.ApplyFix(context.Background(), okResult, func(string) bool { return true })
	if err != nil || out != FixApplied {
		t.Errorf("ok path: out=%v err=%v", out, err)
	}

	boom := errors.New("boom")
	failResult := CheckResult{Name: "bad", Fix: &FixAction{Apply: func(context.Context) error { return boom }}}
	out, err = a.ApplyFix(context.Background(), failResult, func(string) bool { return true })
	if out != FixFailed {
		t.Errorf("fail path outcome: %v", out)
	}
	if !errors.Is(err, boom) {
		t.Errorf("fail path err: %v", err)
	}
}

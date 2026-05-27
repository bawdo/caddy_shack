package app

import (
	"encoding/json"
	"testing"
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

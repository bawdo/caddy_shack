package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/acme"
	"github.com/bawdo/caddy_shack/internal/app"
)

func TestCLIPrompterShowsEveryRecordAndWaitsForConfirmation(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("\n")
	p := &cliPrompter{in: in, out: &out}

	err := p.Confirm([]acme.Record{
		{FQDN: "_acme-challenge.alpha.arrix.dev.", Value: "value-one"},
		{FQDN: "_acme-challenge.alpha.arrix.dev.", Value: "value-two"},
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}

	got := out.String()
	for _, want := range []string{"_acme-challenge.alpha.arrix.dev", "value-one", "value-two", "TXT"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt must mention %q, got:\n%s", want, got)
		}
	}
	// Both records live on one name and must coexist. An operator who
	// replaces the first with the second fails validation.
	if !strings.Contains(strings.ToLower(got), "both") {
		t.Errorf("prompt must say both records are needed at once, got:\n%s", got)
	}
}

func TestCLIPrompterErrorsWhenInputClosesWithoutConfirmation(t *testing.T) {
	var out bytes.Buffer
	p := &cliPrompter{in: strings.NewReader(""), out: &out}
	if err := p.Confirm([]acme.Record{{FQDN: "a.", Value: "v"}}); err == nil {
		t.Error("a closed input must error rather than proceed as if confirmed")
	}
}

func TestRenderCertListTable(t *testing.T) {
	var buf bytes.Buffer
	rows := []app.CertListing{{
		Domain:        "alpha.arrix.dev",
		Env:           "production",
		Subjects:      []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
		NotAfter:      "2026-12-07T10:00:00+10:00",
		DaysRemaining: 90,
	}}
	if err := renderCertList(&buf, rows, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	got := buf.String()
	for _, want := range []string{"alpha.arrix.dev", "production", "90"} {
		if !strings.Contains(got, want) {
			t.Errorf("table must contain %q, got:\n%s", want, got)
		}
	}
}

func TestRenderCertListJSON(t *testing.T) {
	var buf bytes.Buffer
	rows := []app.CertListing{{
		Domain:        "alpha.arrix.dev",
		Env:           "staging",
		Subjects:      []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
		NotAfter:      "2026-12-07T10:00:00+10:00",
		DaysRemaining: 90,
	}}
	if err := renderCertList(&buf, rows, true); err != nil {
		t.Fatalf("render: %v", err)
	}
	var back []app.CertListing
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("output must be valid JSON: %v\n%s", err, buf.String())
	}
	if len(back) != 1 || back[0].Domain != "alpha.arrix.dev" {
		t.Errorf("round trip mismatch: %+v", back)
	}
}

func TestRenderCertListEmptyJSONIsAnArrayNotNull(t *testing.T) {
	var buf bytes.Buffer
	if err := renderCertList(&buf, nil, true); err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Errorf("empty JSON = %q, want [] so scripts can iterate without a nil check", got)
	}
}

func TestCertCommandHasTheFourSubcommands(t *testing.T) {
	c := newCertCmd()
	want := map[string]bool{"request": false, "list": false, "renew": false, "remove": false}
	for _, sub := range c.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("cert is missing the %q subcommand", name)
		}
	}
}

func TestCertRequestRequiresEmailFlag(t *testing.T) {
	c := newCertCmd()
	for _, sub := range c.Commands() {
		if sub.Name() == "request" {
			if sub.Flags().Lookup("email") == nil {
				t.Error("request must expose --email")
			}
			if sub.Flags().Lookup("production") == nil {
				t.Error("request must expose --production")
			}
		}
	}
}

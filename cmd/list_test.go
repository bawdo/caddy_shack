package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/app"
)

func TestRenderListEmptyHuman(t *testing.T) {
	var buf bytes.Buffer
	if err := renderList(&buf, nil, false); err != nil {
		t.Fatalf("renderList: %v", err)
	}
	if got := buf.String(); got != "no backends registered\n" {
		t.Errorf("got %q want %q", got, "no backends registered\n")
	}
}

func TestRenderListHumanTable(t *testing.T) {
	items := []app.Listing{
		{Name: "alpha", Domain: "alpha.test", Upstream: "127.0.0.1:3000", RegisteredAt: "2026-05-27T10:00:00Z"},
		{Name: "bravo", Domain: "bravo.test", Upstream: "127.0.0.1:4000", RegisteredAt: "2026-05-27T11:00:00Z"},
	}
	var buf bytes.Buffer
	if err := renderList(&buf, items, false); err != nil {
		t.Fatalf("renderList: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "DOMAIN", "UPSTREAM", "REGISTERED",
		"alpha", "alpha.test", "127.0.0.1:3000",
		"bravo", "bravo.test", "127.0.0.1:4000"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestRenderListJSON(t *testing.T) {
	items := []app.Listing{
		{Name: "foo", Domain: "foo.test", Upstream: "127.0.0.1:3000", RegisteredAt: "2026-05-27T10:00:00Z"},
	}
	var buf bytes.Buffer
	if err := renderList(&buf, items, true); err != nil {
		t.Fatalf("renderList: %v", err)
	}
	var got []app.Listing
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw:\n%s", err, buf.String())
	}
	if len(got) != 1 || got[0].Name != "foo" {
		t.Errorf("unexpected JSON shape: %+v", got)
	}
}

func TestRenderListJSONEmptyIsValidArray(t *testing.T) {
	var buf bytes.Buffer
	if err := renderList(&buf, nil, true); err != nil {
		t.Fatalf("renderList: %v", err)
	}
	out := strings.TrimSpace(buf.String())
	// Empty slice should marshal as null OR []; both are valid JSON for a
	// nil []Listing. Accept either.
	if out != "[]" && out != "null" {
		t.Errorf("empty JSON output: got %q want [] or null", out)
	}
}

func TestRenderListHumanTableShowsWildcardFooter(t *testing.T) {
	var buf bytes.Buffer
	items := []app.Listing{
		{Name: "web", Domain: "web.test", Upstream: "127.0.0.1:3000", RegisteredAt: "2026-06-06T00:00:00Z"},
	}
	if err := renderList(&buf, items, false); err != nil {
		t.Fatalf("renderList: %v", err)
	}
	if !strings.Contains(buf.String(), "Each backend also serves *.<domain> unless a more specific override exists.") {
		t.Errorf("expected wildcard footer in human output, got:\n%s", buf.String())
	}
}

func TestRenderListFooterAbsentWhenEmptyOrJSON(t *testing.T) {
	var human bytes.Buffer
	if err := renderList(&human, nil, false); err != nil {
		t.Fatalf("renderList empty human: %v", err)
	}
	if strings.Contains(human.String(), "Each backend also serves") {
		t.Errorf("footer should not appear when no backends: %q", human.String())
	}
	var js bytes.Buffer
	items := []app.Listing{{Name: "web", Domain: "web.test", Upstream: "127.0.0.1:3000", RegisteredAt: "x"}}
	if err := renderList(&js, items, true); err != nil {
		t.Fatalf("renderList json: %v", err)
	}
	if strings.Contains(js.String(), "Each backend also serves") {
		t.Errorf("footer should not appear in JSON: %q", js.String())
	}
}

func TestRenderListShowsLivenessAndWarns(t *testing.T) {
	items := []app.Listing{
		{Name: "alpha", Domain: "alpha.test", Live: app.LiveYes},
		{Name: "bravo", Domain: "bravo.test", Live: app.LiveNo},
	}
	var buf bytes.Buffer
	if err := renderList(&buf, items, false); err != nil {
		t.Fatalf("renderList: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"LIVE", "1 backend(s) registered but not live", "caddy_shack sync"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull:\n%s", want, out)
		}
	}
}

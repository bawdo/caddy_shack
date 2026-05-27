package caddyapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bawdo/caddy_shack/internal/state"
)

func TestBuildConfigEmptyState(t *testing.T) {
	s := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{}}
	got := Build(s)
	assertJSONMatchesGolden(t, got, "empty.json")
}

func TestBuildConfigOneBackend(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"myapp": {Domain: "myapp.test", Upstream: "127.0.0.1:3000"},
		},
	}
	got := Build(s)
	assertJSONMatchesGolden(t, got, "one_backend.json")
}

// TestBuildPreservesAdminBinding pins down that every rendered config keeps
// admin bound to 0.0.0.0:2019. Without this, POST /load reverts admin to
// caddy's default localhost:2019 and the next register/deregister can't
// reach the container's admin API from the host. Discovered by smoke test.
func TestBuildPreservesAdminBinding(t *testing.T) {
	got := Build(&state.State{SchemaVersion: 1, Backends: map[string]state.Backend{}})
	var doc struct {
		Admin struct {
			Listen string `json:"listen"`
		} `json:"admin"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Admin.Listen != "0.0.0.0:2019" {
		t.Errorf("admin.listen: got %q want 0.0.0.0:2019", doc.Admin.Listen)
	}
}

func TestBuildConfigTwoBackendsSortedByDomain(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"web": {Domain: "web.test", Upstream: "127.0.0.1:3000"},
			"api": {Domain: "api.test", Upstream: "127.0.0.1:4000"},
		},
	}
	got := Build(s)
	assertJSONMatchesGolden(t, got, "two_backends.json")
}

func assertJSONMatchesGolden(t *testing.T, got []byte, name string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	var gotAny, wantAny any
	if err := json.Unmarshal(got, &gotAny); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if err := json.Unmarshal(want, &wantAny); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	gotNorm, _ := json.MarshalIndent(gotAny, "", "  ")
	wantNorm, _ := json.MarshalIndent(wantAny, "", "  ")
	if string(gotNorm) != string(wantNorm) {
		t.Errorf("golden mismatch for %s\nwant:\n%s\n\ngot:\n%s", name, wantNorm, gotNorm)
	}
}

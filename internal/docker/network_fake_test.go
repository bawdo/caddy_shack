package docker

import (
	"slices"
	"testing"
)

func TestFakeEnsureNetworkIsIdempotent(t *testing.T) {
	f := NewFake()
	if err := f.EnsureNetwork("caddy_shack"); err != nil {
		t.Fatalf("EnsureNetwork first: %v", err)
	}
	if err := f.EnsureNetwork("caddy_shack"); err != nil {
		t.Fatalf("EnsureNetwork second: %v", err)
	}
	if !f.Networks["caddy_shack"] {
		t.Fatal("network not recorded as created")
	}
}

func TestFakeConnectNetworkThenAttached(t *testing.T) {
	f := NewFake()
	f.Containers["caddy_shack"] = StateRunning
	attached := func() bool {
		info, err := f.InspectContainer("caddy_shack")
		if err != nil {
			t.Fatalf("InspectContainer: %v", err)
		}
		return slices.Contains(info.Networks, "caddy_shack")
	}
	if attached() {
		t.Fatal("expected not attached before connect")
	}
	if err := f.ConnectNetwork("caddy_shack", "caddy_shack"); err != nil {
		t.Fatalf("ConnectNetwork: %v", err)
	}
	if !attached() {
		t.Fatal("expected attached after connect")
	}
}

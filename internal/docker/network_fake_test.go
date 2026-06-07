package docker

import "testing"

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
	attached, err := f.NetworkAttached("caddy_shack", "caddy_shack")
	if err != nil {
		t.Fatalf("NetworkAttached: %v", err)
	}
	if attached {
		t.Fatal("expected not attached before connect")
	}
	if err := f.ConnectNetwork("caddy_shack", "caddy_shack"); err != nil {
		t.Fatalf("ConnectNetwork: %v", err)
	}
	attached, err = f.NetworkAttached("caddy_shack", "caddy_shack")
	if err != nil {
		t.Fatalf("NetworkAttached after connect: %v", err)
	}
	if !attached {
		t.Fatal("expected attached after connect")
	}
}

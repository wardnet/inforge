package meshpaths

import "testing"

func TestEgressPort(t *testing.T) {
	if got := EgressPort(0); got != EgressBase {
		t.Errorf("EgressPort(0) = %d, want %d", got, EgressBase)
	}
	if got := EgressPort(3); got != EgressBase+3 {
		t.Errorf("EgressPort(3) = %d, want %d", got, EgressBase+3)
	}
}

func TestLeafPaths(t *testing.T) {
	if got := LeafCertPath("ddns"); got != "/run/wardnet/mesh/ddns/leaf.crt" {
		t.Errorf("LeafCertPath = %q", got)
	}
	if got := LeafKeyPath("ddns"); got != "/run/wardnet/mesh/ddns/leaf.key" {
		t.Errorf("LeafKeyPath = %q", got)
	}
	if BundlePath != "/run/wardnet/mesh/bundle.crt" {
		t.Errorf("BundlePath = %q", BundlePath)
	}
}

func TestInReservedEgressRange(t *testing.T) {
	cases := map[int]bool{
		EgressBase - 1:               false,
		EgressBase:                   true,
		EgressBase + MaxServices - 1: true,
		EgressBase + MaxServices:     false,
		MTLSPort:                     false, // the mТLS port is on the host interface, not the loopback egress range
	}
	for port, want := range cases {
		if got := InReservedEgressRange(port); got != want {
			t.Errorf("InReservedEgressRange(%d) = %v, want %v", port, got, want)
		}
	}
}

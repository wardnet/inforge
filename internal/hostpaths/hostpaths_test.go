package hostpaths

import "testing"

func TestHostPaths(t *testing.T) {
	if got := RuntimeSubdir("bridge"); got != "wardnet/bridge" {
		t.Errorf("RuntimeSubdir = %q, want wardnet/bridge", got)
	}
	if got := RuntimeDir("bridge"); got != "/run/wardnet/bridge" {
		t.Errorf("RuntimeDir = %q, want /run/wardnet/bridge", got)
	}
	if got := UnitName("bridge"); got != "wardnet-bridge.service" {
		t.Errorf("UnitName = %q, want wardnet-bridge.service", got)
	}
	// RuntimeDir is RuntimeSubdir under /run — the unit's RuntimeDirectory= and
	// the bootstrapper's projection target must stay byte-for-byte identical.
	if RuntimeDir("svc") != "/run/"+RuntimeSubdir("svc") {
		t.Error("RuntimeDir must be /run/ + RuntimeSubdir")
	}
}

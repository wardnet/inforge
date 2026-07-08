package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestReloadOrRestartCmdToleratesMissingUnit guards the fresh-env mesh baseline: the
// signal reloads the unit when it is installed but is a graceful no-op (never a
// failure) when it is not — an mtls_files: service's unit is installed later, at
// release time, so it is absent during the deploy-time baseline.
func TestReloadOrRestartCmdToleratesMissingUnit(t *testing.T) {
	cmd := reloadOrRestartCmd("wardnet-tunneller.service")
	for _, want := range []string{
		"systemctl cat wardnet-tunneller.service",                    // existence guard
		"sudo systemctl reload-or-restart wardnet-tunneller.service", // when present
		"not installed yet", // graceful skip branch
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("reloadOrRestartCmd missing %q\n%s", want, cmd)
		}
	}
	// The guard must not be a bare unconditional reload.
	if !strings.HasPrefix(cmd, "if systemctl cat ") {
		t.Errorf("reload must be guarded on unit existence, got: %s", cmd)
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	p := filepath.Join(t.TempDir(), "reload.sh")
	if err := os.WriteFile(p, []byte("set -euo pipefail\n"+cmd+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bash, "-n", p).CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, out)
	}
}

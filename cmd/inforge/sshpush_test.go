package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestReloadOrRestartCmdGuardsOnRunning verifies the fresh-env mesh baseline only
// signals a RUNNING service. An mtls_files: service's unit is installed by the deploy
// but its workload arrives later via `inforge releases deploy`, so during the baseline
// the unit exists yet cannot start — the guard must be `is-active` (running), not mere
// existence, or reload-or-restart would try to start it and crash the baseline.
func TestReloadOrRestartCmdGuardsOnRunning(t *testing.T) {
	cmd := reloadOrRestartCmd("wardnet-tunneller.service")
	for _, want := range []string{
		"systemctl is-active --quiet wardnet-tunneller.service",      // running guard
		"sudo systemctl reload-or-restart wardnet-tunneller.service", // when running
		"not running", // graceful skip branch
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("reloadOrRestartCmd missing %q\n%s", want, cmd)
		}
	}
	// Must guard on is-active, not existence (`systemctl cat`) — an existing but
	// not-yet-released unit must be skipped, not started.
	if !strings.HasPrefix(cmd, "if systemctl is-active ") {
		t.Errorf("reload must be guarded on is-active, got: %s", cmd)
	}
	if strings.Contains(cmd, "systemctl cat ") {
		t.Errorf("must not guard on existence (systemctl cat) — an existing unreleased unit would be started: %s", cmd)
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

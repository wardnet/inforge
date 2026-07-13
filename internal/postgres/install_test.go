package postgres

import (
	"strings"
	"testing"
)

// TestUnitFileRestartsOnAnyExit — the cluster is a daemon, and a daemon has no correct
// exit. `Restart=on-failure` only restarts an exit systemd CLASSIFIES as a failure, so a
// postgres that exits in a way systemd records as clean would go inactive (dead) and STAY
// there: every database on the cluster unreachable, indefinitely, with no crash-loop to
// notice it. The same policy left a production service down for forty minutes.
func TestUnitFileRestartsOnAnyExit(t *testing.T) {
	unit := UnitFile("pg-regional", "17", ClusterPort(0))
	if !strings.Contains(unit, "Restart=always") {
		t.Error("the cluster must restart on ANY exit, not only on an exit systemd deems a failure")
	}
	if strings.Contains(unit, "Restart=on-failure") {
		t.Error("on-failure lets a cleanly-exited cluster stay dead forever")
	}
}

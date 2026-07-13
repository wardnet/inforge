package postgres

import (
	"strings"
	"testing"
)

// firstCluster is the cluster index whose port the unit under test listens on. The tests
// below assert on the unit's restart policy, not on the port, but naming it beats a bare 0.
const firstCluster = 0

// TestUnitFileRestartsOnAnyExit — the cluster is a daemon, and a daemon has no correct
// exit. All three directives are ONE policy, and any of them missing lets the cluster end
// permanently dead — every database on it unreachable. See
// .agents/rules/daemon-units-restart-on-any-exit.md.
//
//   - Restart=always: `on-failure` only restarts an exit systemd CLASSIFIES as a failure,
//     so a postgres that exits in a way systemd records as clean goes inactive (dead) and
//     STAYS there. That policy left a production service down for forty minutes.
//   - RestartSec=5: systemd's 100ms default would relaunch a dying postmaster (full disk,
//     corrupt PGDATA) ten times a second into the same wall.
//   - StartLimitIntervalSec=0: the default 5-starts-per-10s limit would end the retries and
//     park the unit in `failed` — the same permanent death Restart=always exists to
//     prevent, just reached faster.
func TestUnitFileRestartsOnAnyExit(t *testing.T) {
	unit := UnitFile("pg-regional", "17", ClusterPort(firstCluster))
	for _, want := range []string{"Restart=always", "RestartSec=5", "StartLimitIntervalSec=0"} {
		if !strings.Contains(unit, want) {
			t.Errorf("missing %q: the cluster must restart on ANY exit, back off, and never give up", want)
		}
	}
	if strings.Contains(unit, "Restart=on-failure") {
		t.Error("on-failure lets a cleanly-exited cluster stay dead forever")
	}
}

package meshnginx

import (
	"strings"
	"testing"

	"github.com/wardnet/inforge/internal/hostpaths"
	"github.com/wardnet/inforge/internal/meshpaths"
)

func TestUnitFile(t *testing.T) {
	u := UnitFile()
	for _, want := range []string{
		"Type=forking",
		"PIDFile=" + meshpaths.PIDPath,
		"ExecStartPre=/usr/bin/env bash " + SeedScriptPath,
		"-c " + meshpaths.ConfigPath,
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit file missing %q\n%s", want, u)
		}
	}
	// The mesh unit must NOT declare a RuntimeDirectory — systemd would wipe the
	// tmpfs cert dir when the unit stops.
	if strings.Contains(u, "RuntimeDirectory=") {
		t.Error("mesh unit must not declare RuntimeDirectory=")
	}
	// The local projection must be `-`-prefixed (an absent/corrupt leaf.age never
	// blocks the proxy from starting) and run BEFORE the placeholder seed, which
	// runs before the config check — so real material wins and placeholders only
	// fill gaps.
	project := strings.Index(u, "ExecStartPre=-"+hostpaths.AgentBin+" mesh-project "+meshpaths.AgentDir)
	seed := strings.Index(u, "ExecStartPre=/usr/bin/env bash "+SeedScriptPath)
	check := strings.Index(u, "ExecStartPre="+nginxBin+" -t")
	if project == -1 || seed == -1 || check == -1 || project >= seed || seed >= check {
		t.Errorf("ExecStartPre order must be project < seed < nginx -t (got %d, %d, %d)\n%s", project, seed, check, u)
	}
}

func TestSeedScript(t *testing.T) {
	s := SeedScript([]string{"tunneller", "ddns"})
	for _, want := range []string{
		"install -d -m 0700 -o nginx -g nginx " + meshpaths.RuntimeDir,
		// bundle placeholder guarded on absence.
		"if [ ! -f " + meshpaths.BundlePath + " ]; then",
		// per-service leaf placeholders, seeded only when absent (never clobbering
		// real material a later delivery writes).
		"if [ ! -f " + meshpaths.LeafCertPath("ddns") + " ] || [ ! -f " + meshpaths.LeafKeyPath("ddns") + " ]; then",
		"if [ ! -f " + meshpaths.LeafCertPath("tunneller") + " ]",
		"openssl req -x509",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("seed script missing %q\n%s", want, s)
		}
	}
	// Services are seeded in sorted order (ddns before tunneller).
	if strings.Index(s, meshpaths.LeafCertPath("ddns")) > strings.Index(s, meshpaths.LeafCertPath("tunneller")) {
		t.Error("seed script services must be in sorted order")
	}
}

func TestSeedScriptEmpty(t *testing.T) {
	// A host with no co-located services still seeds the dir + bundle (a valid,
	// if degenerate, config), never erroring.
	s := SeedScript(nil)
	if !strings.Contains(s, meshpaths.RuntimeDir) || !strings.Contains(s, meshpaths.BundlePath) {
		t.Errorf("empty seed script must still create the dir + bundle\n%s", s)
	}
}

// TestUnitFileRestartsOnAnyExit — the mesh proxy is a daemon, and a daemon has no correct
// exit. `Restart=on-failure` only restarts an exit systemd classifies as a failure, so an
// nginx that exits in a way systemd records as clean would go inactive (dead) and STAY
// there: every co-located service's east-west plane down, indefinitely, with no crash-loop
// to notice. The same policy left a production service down for forty minutes.
func TestUnitFileRestartsOnAnyExit(t *testing.T) {
	unit := UnitFile()
	// All three halves are one policy — any one of them missing lets the proxy end
	// permanently dead, and a dead mesh proxy is every co-located service's east-west
	// plane. See .agents/rules/daemon-units-restart-on-any-exit.md.
	for _, want := range []string{"Restart=always", "RestartSec=5", "StartLimitIntervalSec=0"} {
		if !strings.Contains(unit, want) {
			t.Errorf("missing %q: the proxy must restart on ANY exit, back off, and never give up", want)
		}
	}
	if strings.Contains(unit, "Restart=on-failure") {
		t.Error("on-failure lets a cleanly-exited proxy stay dead forever")
	}
}

package hetzner

import (
	"strings"
	"testing"
)

// TestMeshReloadStartsUnitBeforeValidate guards the v5 first-deploy fix: the mesh
// config references tmpfs leaf certs that only the unit's ExecStartPre seed
// creates, so the reload step must `systemctl start` (which seeds) BEFORE the
// standalone `nginx -t` — otherwise `nginx -t` fails on a fresh host and the
// `&&`-guarded reload never runs.
func TestMeshReloadStartsUnitBeforeValidate(t *testing.T) {
	cmd := meshReloadCommand()
	startIdx := strings.Index(cmd, "systemctl start ")
	validateIdx := strings.Index(cmd, " -t -c ")
	if startIdx < 0 {
		t.Fatalf("reload must start the unit; got %q", cmd)
	}
	if validateIdx < 0 {
		t.Fatalf("reload must validate with nginx -t; got %q", cmd)
	}
	if startIdx > validateIdx {
		t.Fatalf("`systemctl start` (seeds certs) must precede `nginx -t`; got %q", cmd)
	}
	if !strings.Contains(cmd, "reload-or-restart") {
		t.Errorf("reload must end in reload-or-restart; got %q", cmd)
	}
}

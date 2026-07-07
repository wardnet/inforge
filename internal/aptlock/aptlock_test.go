package aptlock

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateCmdRetriesAndParses(t *testing.T) {
	cmd := UpdateCmd()
	for _, want := range []string{
		"apt-get -o DPkg::Lock::Timeout=300 update",
		"for _i in $(seq 1 12)", // retries
		"DEBIAN_FRONTEND=noninteractive",
		"failed after retries", // final failure surfaces
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("UpdateCmd missing %q\n---\n%s", want, cmd)
		}
	}

	// The whole statement must parse as bash (it is embedded mid-script under set -e).
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	p := filepath.Join(t.TempDir(), "upd.sh")
	if err := os.WriteFile(p, []byte("set -euo pipefail\n"+cmd+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bash, "-n", p).CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, out)
	}
}

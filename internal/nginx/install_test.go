package nginx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallScript guards the v5 deploy fixes: no `gpg --dearmor` (fails with no
// controlling tty), the ARMORED .asc key used via signed-by, DEBIAN_FRONTEND set on
// the sudo command line (not the ignored `sudo -E`), and the retrying apt update.
func TestInstallScript(t *testing.T) {
	s := InstallScript()
	for _, want := range []string{
		"if [ ! -f /usr/share/keyrings/nginx-archive-keyring.asc ]",
		`sudo install -m 0644 "$asc" /usr/share/keyrings/nginx-archive-keyring.asc`,
		"signed-by=/usr/share/keyrings/nginx-archive-keyring.asc",
		"sudo DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y nginx nginx-module-acme",
		"apt-get update blocked", // aptlock retry
		"sudo systemctl enable nginx",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("InstallScript missing %q", want)
		}
	}
	if strings.Contains(s, "gpg --dearmor") {
		t.Error("must not use gpg --dearmor (fails with no tty; apt reads the .asc directly)")
	}
	if strings.Contains(s, "sudo -E apt-get") {
		t.Error("must not use `sudo -E` (ignored by sudoers → apt falls back to a Teletype debconf frontend)")
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	p := filepath.Join(t.TempDir(), "nginx-install.sh")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bash, "-n", p).CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, out)
	}
}

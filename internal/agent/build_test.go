package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestStaticNoCgoBuild guards the AGENTS "binaries must stay fully self-contained"
// rule for inforge-agent: it cross-compiles the binary for linux with
// CGO_ENABLED=0 and fails if anything pulls in cgo (e.g. an accidental os/user
// import in the privilege-drop path). A non-static agent would silently
// depend on the host's libc.
func TestStaticNoCgoBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping build smoke in -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	out := filepath.Join(t.TempDir(), "inforge-agent")
	cmd := exec.Command("go", "build", "-o", out, "github.com/wardnet/inforge/cmd/inforge-agent")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("static CGO_ENABLED=0 linux build failed: %v\n%s", err, combined)
	}
}

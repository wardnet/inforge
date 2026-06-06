//go:build linux && integration

// This test exercises the real privilege-drop + exec path and therefore needs
// root and a throwaway user. It is gated behind the `integration` build tag and a
// root check so it never runs in CI's `go test -race ./...`. Run it manually:
//
//	sudo go test -tags integration -run TestPrivilegeDropExec ./internal/bootstrapper
package bootstrapper

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDropHelperProcess is the child half of the integration test: when invoked
// with BOOTSTRAP_DROP_TARGET set it calls dropAndExec, which replaces this
// process image with the target. It is a no-op in a normal test run.
func TestDropHelperProcess(t *testing.T) {
	target := os.Getenv("BOOTSTRAP_DROP_TARGET")
	if target == "" {
		t.Skip("helper process not invoked")
	}
	uid, _ := strconv.Atoi(os.Getenv("BOOTSTRAP_DROP_UID"))
	gid, _ := strconv.Atoi(os.Getenv("BOOTSTRAP_DROP_GID"))
	// On success this never returns (the image is replaced). If it returns, the
	// drop or exec failed; exit non-zero so the parent observes the failure.
	if err := dropAndExec(target, uid, gid, []string{"PATH=/usr/bin:/bin"}); err != nil {
		os.Stderr.WriteString("dropAndExec: " + err.Error() + "\n")
	}
	os.Exit(7)
}

func TestPrivilegeDropExec(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}

	const user = "wardnetitsvc"
	_ = exec.Command("userdel", "-f", user).Run()
	require.NoError(t, exec.Command("useradd", "--system", "--shell", "/usr/sbin/nologin", user).Run())
	t.Cleanup(func() { _ = exec.Command("userdel", "-f", user).Run() })

	u, err := lookupUser(user)
	require.NoError(t, err)
	require.NotZero(t, u.uid, "throwaway user must not be root")

	// A target that prints its dropped identity then execs sleep, so the parent
	// can both read the id output and signal the live exec'd child.
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o755)) // dropped user must reach the script
	script := filepath.Join(dir, "run")
	require.NoError(t, os.WriteFile(script,
		[]byte("#!/bin/sh\nid -u\nid -g\nid -G\nexec sleep 30\n"), 0o755))

	cmd := exec.Command(os.Args[0], "-test.run=TestDropHelperProcess")
	cmd.Env = append(os.Environ(),
		"BOOTSTRAP_DROP_TARGET="+script,
		"BOOTSTRAP_DROP_UID="+strconv.Itoa(u.uid),
		"BOOTSTRAP_DROP_GID="+strconv.Itoa(u.gid),
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	require.NoError(t, cmd.Start())

	// Poll until the script has printed its three id lines (before it execs sleep).
	deadline := time.Now().Add(10 * time.Second)
	var lines []string
	for time.Now().Before(deadline) {
		lines = strings.Fields(strings.TrimSpace(out.String()))
		if len(lines) >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.GreaterOrEqual(t, len(lines), 3, "did not capture id output; got %q", out.String())

	assert.Equal(t, strconv.Itoa(u.uid), lines[0], "effective uid must be the service user, not root")
	assert.Equal(t, strconv.Itoa(u.gid), lines[1], "effective gid must be the service group")
	assert.Equal(t, strconv.Itoa(u.gid), lines[2], "no supplementary groups beyond the primary gid")

	// SIGTERM must reach the exec'd child (proving syscall.Exec replaced the image
	// in place, so systemd's supervision/signaling works).
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		assert.Error(t, err, "the exec'd sleep should be terminated by SIGTERM")
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("SIGTERM did not reach the exec'd child")
	}
}

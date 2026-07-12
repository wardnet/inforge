//go:build linux

package agent

import (
	"fmt"
	"syscall"
)

// dropAndExec irrevocably drops from root to (uid, gid) and execs execPath with
// envv. This is the security-critical core; the ordering and the checks below
// are load-bearing:
//
//   - Setgroups BEFORE Setgid/Setuid: supplementary groups must be cleared while
//     still privileged, or the dropped process could retain root's groups.
//   - Setgid BEFORE Setuid: once uid is dropped you can no longer change gid.
//   - Every drop is checked; any failure aborts hard (returns) and never reaches
//     Exec — a fallthrough would leave the service running as root.
//   - syscall.Setgroups/Setgid/Setuid (stdlib) apply across all OS threads (Go
//     1.16+); the raw per-thread syscalls would leave sibling threads as root.
//   - uid/gid 0 is refused: if passwd resolution misfired and returned 0, the
//     getuid/getgid asserts below would pass while still root, so guard up front.
//   - syscall.Exec (not exec.Command) replaces this process image, so systemd
//     keeps supervising the real service PID and Type=/Restart=/signals stay
//     correct.
func dropAndExec(execPath string, uid, gid int, envv []string) error {
	if uid <= 0 || gid <= 0 {
		return fmt.Errorf("refusing to exec service as uid=%d gid=%d (resolved to a privileged id)", uid, gid)
	}

	if err := syscall.Setgroups([]int{gid}); err != nil {
		return fmt.Errorf("setgroups: %w", err)
	}
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("setgid(%d): %w", gid, err)
	}
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("setuid(%d): %w", uid, err)
	}

	if got := syscall.Getuid(); got != uid {
		return fmt.Errorf("privilege drop verification failed: uid is %d, want %d", got, uid)
	}
	if got := syscall.Getgid(); got != gid {
		return fmt.Errorf("privilege drop verification failed: gid is %d, want %d", got, gid)
	}

	// Same audit as the #nosec below: execPath comes from a root-owned, ParseDescriptor-validated
	// descriptor (ADR-0035), and the process has already dropped privileges irrevocably by this point.
	// nosemgrep: go.lang.security.audit.dangerous-syscall-exec.dangerous-syscall-exec
	if err := syscall.Exec(execPath, []string{execPath}, envv); err != nil { // #nosec G204 -- execPath is desc.Exec from the root-owned, inforge-written descriptor.yaml validated by ParseDescriptor (ADR-0035); not user or network input, and this runs only after an irrevocable privilege drop
		return fmt.Errorf("exec %s: %w", execPath, err)
	}
	// syscall.Exec only returns on error; reaching here means it did not replace
	// the image despite a nil error, which must not be treated as success.
	return fmt.Errorf("exec %s returned unexpectedly", execPath)
}

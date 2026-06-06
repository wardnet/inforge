//go:build !linux

package bootstrapper

import "fmt"

// dropAndExec is unsupported off Linux: the privilege-drop semantics
// (process-wide Setuid/Setgid/Setgroups) and syscall.Exec supervision the
// bootstrapper relies on are Linux-specific. This stub keeps run.go and the rest
// of the package building and unit-testable on developer machines (darwin); the
// real implementation lives in exec.go (//go:build linux).
func dropAndExec(_ string, _, _ int, _ []string) error {
	return fmt.Errorf("inforge-bootstrap privilege-drop exec is only supported on linux")
}

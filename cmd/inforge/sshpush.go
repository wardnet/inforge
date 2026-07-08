package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/wardnet/inforge/internal/hostpaths"
	"github.com/wardnet/inforge/internal/hostsecrets"
)

// sshArgs returns the common raw-ssh flags: the deploy key, accept-new host
// key pinning (so a freshly provisioned host's first connection doesn't
// prompt), and BatchMode (never fall back to a password prompt). This mirrors
// meshBaseline's pre-existing SSH transport — a plain `ssh` exec, not a Go SSH
// client — so `inforge pki renew`'s push reuses the exact same idiom rather
// than introducing a second one.
func sshArgs(keyPath string) []string {
	return []string{"-i", keyPath, "-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=yes"}
}

// sshReadHostPubKey reads a host's own SSH host public key over the same SSH
// connection used to reach it — the age recipient `inforge pki renew`
// encrypts leaf.age to (mirroring program.go's deploy-time readHostPubKey,
// which reads the identical file; the two must always agree since both
// encrypt to the SAME on-host identity that decrypts at boot/mesh-project).
func sshReadHostPubKey(ctx context.Context, keyPath, account string) (string, error) {
	args := append(append([]string{}, sshArgs(keyPath)...), account, "cat "+hostpaths.SSHHostPubKeyPath)
	out, err := exec.CommandContext(ctx, "ssh", args...).Output()
	if err != nil {
		return "", fmt.Errorf("ssh %s: read host public key: %w", account, err)
	}
	pub := strings.TrimSpace(string(out))
	if pub == "" {
		return "", fmt.Errorf("ssh %s: empty host public key", account)
	}
	return pub, nil
}

// sshWriteFile streams content to remotePath on the target host over the SSH
// connection's stdin — no local temp file, no shell-quoting of the payload
// (age ciphertext is binary). `install -D` creates any missing parent
// directory and sets the final mode atomically.
func sshWriteFile(ctx context.Context, keyPath, account, remotePath string, content []byte) error {
	return sshWriteFileFrom(ctx, keyPath, account, remotePath, bytes.NewReader(content))
}

// sshWriteFileFrom is sshWriteFile's streaming form: it copies r straight into
// the SSH stdin without buffering the whole payload in memory, for large inputs
// like a database dump (`inforge db restore --from-dump`).
func sshWriteFileFrom(ctx context.Context, keyPath, account, remotePath string, r io.Reader) error {
	// Stage to <path>.tmp then atomically mv into place: `inforge pki renew` signals
	// reload-or-restart immediately after this write, so an interrupted SSH stream
	// must never leave a truncated leaf.age/secrets.age at the live path (the reload
	// would then project a broken cert). install -D creates the parent dir; the mv is
	// a same-directory atomic rename. Renew is per-host sequential, so the fixed .tmp
	// suffix is not raced.
	remoteCmd := fmt.Sprintf("sudo install -D -m 0600 /dev/stdin %[1]s.tmp && sudo mv %[1]s.tmp %[1]s", remotePath)
	args := append(append([]string{}, sshArgs(keyPath)...), account, remoteCmd)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = r
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh %s: write %s: %w: %s", account, remotePath, err, stderr.String())
	}
	return nil
}

// sshRun runs remoteCmd over SSH, streaming its stdout/stderr into w.
func sshRun(ctx context.Context, keyPath, account, remoteCmd string, w io.Writer) error {
	args := append(append([]string{}, sshArgs(keyPath)...), account, remoteCmd)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh %s: %s: %w", account, remoteCmd, err)
	}
	return nil
}

// pushHostSecrets is `inforge pki renew`'s (and the deploy-time mesh
// baseline's) delivery primitive (ADR-0035): it reads the target host's own
// SSH host public key, age-encrypts blob to it, writes the ciphertext to
// remotePath (mode 0600), and — when restart is true — signals
// `systemctl reload-or-restart unit` over the same connection. Renewal never
// hash-gates this (unlike the deploy-time secrets.age write): a fresh mint
// always differs, so there is nothing to diff against.
//
// projectCmd, when non-empty, is run over the SAME connection immediately
// BEFORE the reload-or-restart signal (restart == true only) — the on-host,
// network-free decrypt-and-project of the leaf.age just written (`inforge-agent
// mesh-project`/`project-leaf`). Both the mesh proxy unit and any mtls_files:
// service that declares reload: carry an ExecReload=, so systemd's
// reload-or-restart ALWAYS resolves to a plain reload — which re-reads
// whatever cert/key files are already on disk but never re-triggers their
// decryption (that happens only in ExecStartPre/ExecStart). Running projectCmd
// first refreshes those files on disk so the reload (or, on a Restart=, the
// unit's own idempotent re-project) picks up the fresh material. Empty when
// the target has nothing to locally re-project before signaling (there is
// none today, but the parameter stays generic rather than special-casing the
// mesh unit).
//
// restart is false for the pre-release per-service mint (mintReleasedServiceLeaf/
// mintReplicatedServiceLeaf): that path only needs leaf.age in place before a
// separate, imminent release delivery restarts the unit anyway — reloading
// (and projecting) here would work on the OLD binary for no benefit.
func pushHostSecrets(ctx context.Context, keyPath, account string, blob hostsecrets.Blob, remotePath, unit, projectCmd string, restart bool, w io.Writer) error {
	pub, err := sshReadHostPubKey(ctx, keyPath, account)
	if err != nil {
		return err
	}
	ct, err := hostsecrets.EncryptBlob(blob, pub)
	if err != nil {
		return fmt.Errorf("encrypt %s: %w", remotePath, err)
	}
	if err := sshWriteFile(ctx, keyPath, account, remotePath, ct); err != nil {
		return err
	}
	if !restart {
		return nil
	}
	if projectCmd != "" {
		if err := sshRun(ctx, keyPath, account, projectCmd, w); err != nil {
			return fmt.Errorf("project %s before reload: %w", remotePath, err)
		}
	}
	return sshRun(ctx, keyPath, account, reloadOrRestartCmd(unit), w)
}

// reloadOrRestartCmd builds the host command that reloads-or-restarts unit, but only
// when the unit is currently RUNNING. The mesh baseline's job is to stage fresh
// leaf.age and signal a live service to re-read it; a service that is not running
// needs no signal — its next start reads the staged leaf.
//
// The guard is `is-active`, NOT `systemctl cat` (mere existence): on a fresh env
// `inforge deploy` installs an mtls_files: service's unit (ExecStart = inforge-agent)
// but the workload is only delivered later by `inforge releases deploy`, so the unit
// EXISTS yet cannot start — a plain `reload-or-restart` would try to start it and the
// agent, finding no released artifact, exits non-zero ("control process exited with
// error code"), aborting the whole baseline. `is-active` skips such a not-yet-running
// unit (and an absent one). The mesh proxy IS running after the deploy, so its reload
// runs normally. unit is an inforge-derived name (wardnet-<svc>.service), not user
// input, so it needs no shell quoting.
func reloadOrRestartCmd(unit string) string {
	return fmt.Sprintf(
		"if systemctl is-active --quiet %[1]s; then sudo systemctl reload-or-restart %[1]s; "+
			"else echo \"inforge: %[1]s not running — leaf.age staged, read on next start\"; fi",
		unit)
}

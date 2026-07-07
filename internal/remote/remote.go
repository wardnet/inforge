// Package remote holds the shared primitives for running inforge provisioning
// steps on a host over SSH via pulumi-command: building the SSH connection and
// rendering the small shell snippets that write or delete files. Orchestration
// — what sequence of commands a given feature runs — stays with each consumer
// (e.g. the Caddy realization in providers/hetzner, service provisioning in the
// program), because those sequences differ; only the primitives are shared.
package remote

import (
	"encoding/base64"
	"fmt"
	"path"
	"strings"

	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Connection builds the SSH connection inforge uses to reach a host: it connects
// to host (the server's public IP) as deployUser — a sudo-capable account that
// trusts the deploy public key via provision.sh — using the deploy private key.
//
// HostKey is intentionally unset: the host's SSH host key is generated on first
// boot and inforge has no out-of-band source for it yet, so the connection is
// trust-on-first-use. Pinning the host key (read at provision time) is tracked
// for the deploy/E2E slice; until then a MITM at the host's IP is a residual
// risk. The deploy private key is the only secret exposed on the wire, and SSH
// still encrypts the session.
func Connection(host pulumi.StringInput, deployUser, deployPrivateKey string) remote.ConnectionArgs {
	return remote.ConnectionArgs{
		Host:       host,
		User:       pulumi.String(deployUser),
		PrivateKey: pulumi.String(deployPrivateKey),
	}
}

// WriteFileScript renders a shell command that writes content to an absolute
// path on the host with default (world-readable) permissions. See
// WriteFileScriptMode for the quoting and encoding rationale.
func WriteFileScript(absPath, content string) string {
	return WriteFileScriptMode(absPath, content, "")
}

// WriteFileScriptMode renders a shell command that writes content to an absolute
// path on the host, creating the parent directory and, when mode is non-empty,
// chmod-ing the file to that octal mode (e.g. "0600" for a secret credential).
// The content is base64-encoded to sidestep all shell-quoting concerns (config
// files contain braces, globs, and newlines); the host decodes it and writes via
// sudo. The path is POSIX single-quoted — Go's %q is NOT shell-safe (a
// double-quoted shell string still expands $(...) and backticks), and paths
// embed caller-supplied names, so %q would be a remote-command-injection vector
// if a name ever carried shell metacharacters. The chmod runs before any bytes
// are written so the file is never momentarily world-readable.
func WriteFileScriptMode(absPath, content, mode string) string {
	b64 := base64.StdEncoding.EncodeToString([]byte(content))
	if mode == "" {
		// Default effective mode (matches the pre-atomic `tee` behaviour: umask 022
		// on root → 0644). A restrictive caller passes 0600 (e.g. secrets.age).
		mode = "0644"
	}
	dir := path.Dir(absPath)
	// Stage to a UNIQUE temp in the SAME directory (so the final `mv` is an atomic
	// same-filesystem rename), set the mode on the temp, then move it into place. A
	// partial/interrupted write (disk full, killed mid-stream, bad base64) leaves the
	// temp truncated but never touches the live path — critical for secrets.age /
	// nginx configs, where a truncated live file would crash-loop the unit at boot.
	// The temp is created 0600 by mktemp and never widened before the mode is set, so
	// the content is never briefly world-readable.
	return strings.Join([]string{
		"set -euo pipefail",
		fmt.Sprintf("sudo install -d -m 0755 %s", Quote(dir)),
		fmt.Sprintf("tmp=$(sudo mktemp %s)", Quote(dir+"/.inforge-write.XXXXXX")),
		fmt.Sprintf(`printf '%%s' %s | base64 -d | sudo tee "$tmp" >/dev/null`, Quote(b64)),
		fmt.Sprintf(`sudo chmod %s "$tmp"`, Quote(mode)),
		fmt.Sprintf(`sudo mv "$tmp" %s`, Quote(absPath)),
	}, "\n")
}

// DeleteFileScript renders the command run when a written file's resource is
// deleted. It is best-effort: a missing file is fine.
func DeleteFileScript(absPath string) string {
	return fmt.Sprintf("sudo rm -f %s", Quote(absPath))
}

// Quote wraps s in POSIX single quotes, escaping any embedded single quote as
// '\''. Inside single quotes the shell performs no expansion, so the result is
// safe to interpolate into a command regardless of the metacharacters in s.
// Callers building their own host commands (service users, unit names) must
// route any caller-supplied value through Quote.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

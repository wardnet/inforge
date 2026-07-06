// Package hostpaths is the single source of truth for the on-host names and
// paths inforge and inforge-agent must agree on byte-for-byte: the systemd
// unit name and the tmpfs RuntimeDirectory a service's mesh PEMs are projected
// into. It is deliberately dependency-free (stdlib only) so the minimal,
// statically-linked inforge-agent binary can import it without pulling in
// the deploy-side packages (internal/service → naming/types → the Pulumi SDK).
package hostpaths

// RuntimeSubdir is the RuntimeDirectory= value (relative to /run) for a service:
// systemd creates /run/<RuntimeSubdir>. The agent projects mesh PEMs there.
func RuntimeSubdir(service string) string { return "wardnet/" + service }

// RuntimeDir is the absolute tmpfs directory the agent projects a
// service's mesh PEMs into, matching the unit's RuntimeDirectory=.
func RuntimeDir(service string) string { return "/run/" + RuntimeSubdir(service) }

// UnitName is the service's systemd unit name.
func UnitName(service string) string { return "wardnet-" + service + ".service" }

// AgentBin is the on-host path of the inforge-agent binary — referenced by every
// service unit's ExecStart, the per-service renew timers, and the mesh proxy's
// material pull (ExecStartPre + wardnet-mesh-renew).
const AgentBin = "/usr/local/bin/inforge-agent"

// SSHHostPubKeyPath is the on-host path of the SSH host's own Ed25519 public
// key, read (via "cat") by both the deploy-time host-key discovery
// (program.readHostPubKey, inside a Pulumi remote.Command) and the CLI's SSH
// push path (cmd/inforge.sshReadHostPubKey) — the two must agree on the exact
// command byte-for-byte, so both build their command string from this single
// constant instead of each hardcoding the literal.
//
// This is the PUBLIC half; the host's PRIVATE key path
// (/etc/ssh/ssh_host_ed25519_key, used by internal/agent's decrypt path via
// its own defaultHostKeyPath) is a distinct concern (agent-side decryption,
// not deploy/push tooling) and is kept as a separate constant.
const SSHHostPubKeyPath = "/etc/ssh/ssh_host_ed25519_key.pub"

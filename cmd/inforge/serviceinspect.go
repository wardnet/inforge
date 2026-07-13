package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/wardnet/inforge/internal/remote"
	"github.com/wardnet/inforge/internal/service"
)

// The read-only half of `inforge service`: the SSH plane (ADR-0041). Every command
// here resolves its targets from the SAME deployDescriptor stack output `releases
// deploy` and `service restart` deliver by, so inspection can never disagree with
// deploy about where a service lives.
//
// Nothing in this file needs anything of the service process itself — it is all
// systemd, /proc and the on-host descriptor, reached as the sudo-capable deploy user
// we already connect as. That is deliberate: ADR-0041's rule is that the in-process
// admin contract holds only what nothing else can answer, and none of this qualifies.

// selector narrows a service's deploy targets. `region` and `host` are FILTERS (they
// choose which targets to act on). `instance` is a PRECONDITION — see requireInstance.
type selector struct {
	region   string
	host     string
	instance string
}

// selectTargets applies the region/host filters. Matching a host is lenient about the
// FQDN: `--host edge` matches `edge.vm.prd.euc.wardnet.network`, because operators
// think in compute names, not DNS records.
func selectTargets(targets []service.DeployTarget, sel selector) []service.DeployTarget {
	out := make([]service.DeployTarget, 0, len(targets))
	for _, t := range targets {
		if sel.region != "" && t.Scope != sel.region {
			continue
		}
		if sel.host != "" && t.HostDNS != sel.host && !strings.HasPrefix(t.HostDNS, sel.host+".") {
			continue
		}
		out = append(out, t)
	}
	return out
}

// instance is one live service process, as observed on its host.
type instance struct {
	Target service.DeployTarget

	State    string // systemd is-active: active / failed / inactive …
	PID      string
	Since    string // systemd ActiveEnterTimestamp
	Restarts string // systemd NRestarts

	// InstanceID is the process's INFORGE_INSTANCE_ID — regenerated on every start, so
	// it identifies this INCARNATION, not the host. That is what makes it usable as a
	// compare-and-swap token (see requireInstance).
	InstanceID string

	Exe     string // the binary /proc/<pid>/exe resolves to
	Running string // sha256 of /proc/<pid>/exe — the image actually EXECUTING
	OnDisk  string // sha256 of Exe on the filesystem — the image that would run on a restart

	// Egress is the service's mesh egress URL, read off its on-host descriptor by
	// meshCheckScript. Only mesh-check populates it.
	Egress string
}

// integrity compares the executing image against the one on disk.
//
// `on disk != executing` is the failure worth having an instrument for: a new binary is
// staged but the process never restarted onto it, so it is running yesterday's code while
// every dashboard says otherwise. /proc/<pid>/exe resolves to the INODE the process is
// executing, which survives the on-disk file being replaced — so the comparison is real,
// not a re-read of the same bytes.
//
// The third column ADR-0041 describes — `delivered`, what inforge recorded at deploy —
// needs an on-host release manifest that does not exist yet, so drift from what we
// *shipped* (as opposed to from what is *staged*) is not yet detectable. That is a
// follow-up, not an oversight.
func (i instance) integrity() string {
	switch {
	case i.State != "active":
		return "-"
	case i.Running == "" || i.OnDisk == "":
		return "unknown"
	case i.Running != i.OnDisk:
		return "STALE"
	default:
		return "ok"
	}
}

// short truncates a hex digest for display. Twelve hex chars (48 bits) is plenty to
// eyeball a match between two hosts; the full digests stay in the struct.
func short(digest string) string {
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12]
}

// inspectScript builds the remote probe for one service unit. It emits `key=value` lines
// rather than pretty output, so the parsing lives here in Go rather than in shell.
//
// Every field degrades to empty rather than failing the command: a service that is down
// has no MainPID, and a probe that cannot answer "which image is executing" must still be
// able to answer "is it even running".
func inspectScript(unit string) string {
	u := remote.Quote(unit)
	return strings.Join([]string{
		`set -u`,
		fmt.Sprintf(`unit=%s`, u),
		`printf 'state=%s\n' "$(systemctl is-active "$unit" 2>/dev/null || true)"`,
		`pid=$(systemctl show -p MainPID --value "$unit" 2>/dev/null || echo 0)`,
		`printf 'pid=%s\n' "$pid"`,
		`printf 'since=%s\n' "$(systemctl show -p ActiveEnterTimestamp --value "$unit" 2>/dev/null || true)"`,
		`printf 'restarts=%s\n' "$(systemctl show -p NRestarts --value "$unit" 2>/dev/null || true)"`,
		`if [ -n "$pid" ] && [ "$pid" != "0" ]; then`,
		// The instance id lives only in the process's environment — it is generated per
		// start by the agent, so it exists nowhere on disk.
		//
		// `sudo cat`, NOT `sudo tr … < /proc/…/environ`: a redirect is opened by the
		// SHELL, which is the unprivileged deploy user, so the redirect form dies with
		// "Permission denied" and silently yields an empty instance id. sudo has to be the
		// process that opens the file.
		`  printf 'instance_id=%s\n' "$(sudo cat /proc/$pid/environ 2>/dev/null | tr '\0' '\n' | sed -n 's/^INFORGE_INSTANCE_ID=//p')"`,
		// A replaced binary leaves the symlink reading "<path> (deleted)"; strip that so
		// we can still stat the path that now occupies it.
		`  exe=$(sudo readlink /proc/$pid/exe 2>/dev/null | sed 's/ (deleted)$//')`,
		`  printf 'exe=%s\n' "$exe"`,
		// Reads the INODE being executed, not the path — this is the half that survives a
		// swap of the file underneath a running process.
		`  printf 'running=%s\n' "$(sudo sha256sum /proc/$pid/exe 2>/dev/null | cut -d" " -f1)"`,
		`  if [ -n "$exe" ] && [ -f "$exe" ]; then printf 'ondisk=%s\n' "$(sudo sha256sum "$exe" 2>/dev/null | cut -d" " -f1)"; fi`,
		`fi`,
	}, "\n")
}

// parseInspect reads inspectScript's key=value output. Unknown keys are ignored so an
// older inforge can read a newer probe's output without erroring.
func parseInspect(out string) instance {
	var i instance
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "state":
			i.State = val
		case "pid":
			i.PID = val
		case "since":
			i.Since = val
		case "restarts":
			i.Restarts = val
		case "instance_id":
			i.InstanceID = val
		case "exe":
			i.Exe = val
		case "running":
			i.Running = val
		case "ondisk":
			i.OnDisk = val
		case "egress":
			i.Egress = val
		}
	}
	return i
}

// sshRunner runs a remote command and writes its output to w. It is the seam that lets
// every command in this file be tested without an SSH daemon — production passes sshRun.
type sshRunner func(ctx context.Context, keyPath, account, remoteCmd string, w io.Writer) error

// collectInstances probes every target over real SSH.
func collectInstances(ctx context.Context, targets []service.DeployTarget, sshKeyPath string) []instance {
	return collectInstancesWith(ctx, targets, sshKeyPath, sshRun)
}

// collectInstancesWith probes every target and returns what it found, in target order.
//
// A host that cannot be reached yields an instance carrying "unreachable" as its state
// rather than aborting the sweep: `service instances` is a diagnostic, and "one of your
// five hosts is unreachable" is exactly the finding you called it to get. Failing the
// whole command on the first bad host would hide the other four.
func collectInstancesWith(ctx context.Context, targets []service.DeployTarget, sshKeyPath string, run sshRunner) []instance {
	out := make([]instance, 0, len(targets))
	for _, t := range targets {
		var buf bytes.Buffer
		if err := run(ctx, sshKeyPath, sshAccount(t), inspectScript(t.Unit), &buf); err != nil {
			out = append(out, instance{Target: t, State: "unreachable"})
			continue
		}
		i := parseInspect(buf.String())
		i.Target = t
		out = append(out, i)
	}
	return out
}

// sshAccount is the `user@host` inforge connects as for a target. BuildDeployDescriptor
// always sets SSHUser, but the fallback mirrors sshRestart's and costs nothing.
func sshAccount(t service.DeployTarget) string {
	user := t.SSHUser
	if user == "" {
		user = "deploy"
	}
	return fmt.Sprintf("%s@%s", user, t.HostDNS)
}

// requireInstance enforces --instance as a PRECONDITION rather than a filter.
//
// The instance id is regenerated on every start, so it is not merely an address — it is a
// version token for the running process. An operator lists the instances, reads the
// output, thinks for thirty seconds, and fires a command; if the service crash-looped in
// between, a plain filter would either silently do nothing or (worse, when combined with
// --host) act on a DIFFERENT incarnation than the one they were looking at. Compare-and-
// swap instead: refuse, and say what changed — which is itself the diagnosis.
// It matches on a PREFIX, like a git short SHA. `service instances` prints a truncated id
// (the full one is 32 hex chars and unreadable in a table), so an operator copy-pasting what
// they were shown MUST be able to match — comparing against the full id would mean the
// documented workflow could never work. An ambiguous prefix is refused rather than guessed.
func requireInstance(instances []instance, want string) ([]service.DeployTarget, error) {
	if want == "" {
		targets := make([]service.DeployTarget, 0, len(instances))
		for _, i := range instances {
			targets = append(targets, i.Target)
		}
		return targets, nil
	}

	var matched []instance
	for _, i := range instances {
		if i.InstanceID != "" && strings.HasPrefix(i.InstanceID, want) {
			matched = append(matched, i)
		}
	}
	if len(matched) == 1 {
		return []service.DeployTarget{matched[0].Target}, nil
	}
	if len(matched) > 1 {
		hosts := make([]string, 0, len(matched))
		for _, i := range matched {
			hosts = append(hosts, fmt.Sprintf("%s on %s", short(i.InstanceID), i.Target.HostDNS))
		}
		sort.Strings(hosts)
		return nil, fmt.Errorf("instance %q is ambiguous — it matches %s; give more characters", want, strings.Join(hosts, ", "))
	}

	// No match. Distinguish "the process was replaced" from "we could not see the host":
	// an SSH failure yields no instance id, and reporting that as a restart would send the
	// operator hunting a phantom deploy.
	var live, unreachable []string
	for _, i := range instances {
		switch {
		case i.State == "unreachable":
			unreachable = append(unreachable, i.Target.HostDNS)
		case i.InstanceID != "":
			live = append(live, fmt.Sprintf("%s on %s", short(i.InstanceID), i.Target.HostDNS))
		}
	}
	sort.Strings(live)
	sort.Strings(unreachable)

	if len(unreachable) > 0 {
		return nil, fmt.Errorf("instance %s not found, but %d host(s) were unreachable (%s) — it may still be running there; fix the connection before assuming a restart",
			short(want), len(unreachable), strings.Join(unreachable, ", "))
	}
	if len(live) == 0 {
		return nil, fmt.Errorf("instance %s is gone — no live instance of this service was found (it is not running)", short(want))
	}
	return nil, fmt.Errorf("instance %s is gone — the service restarted. Live now: %s", short(want), strings.Join(live, ", "))
}

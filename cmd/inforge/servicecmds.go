package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/remote"
	"github.com/wardnet/inforge/internal/service"
)

// The `inforge service` verbs that inspect or drive a deployed service over SSH
// (ADR-0041's SSH plane). They share one selector grammar and one target-resolution
// path with `service restart` and `releases deploy`.

// addSelectorFlags wires the shared selector. `--region`/`--host` narrow which targets a
// command acts on; `--instance` asserts WHICH running process it acts on (see
// requireInstance) and is only meaningful for commands that touch a live process.
func addSelectorFlags(cmd *cobra.Command, sel *selector, withInstance bool) {
	cmd.Flags().StringVar(&sel.region, "region", "", "act only on targets in this region (e.g. eu-central)")
	cmd.Flags().StringVar(&sel.host, "host", "", "act only on this host (compute name or full DNS)")
	if withInstance {
		cmd.Flags().StringVar(&sel.instance, "instance", "", "act only if the live process still has this instance id (from `service instances`)")
	}
}

// serviceTargets resolves a service's deploy targets and applies the selector's filters.
func serviceTargets(ctx context.Context, configPath, env, svc, stackConfigPath string, sel selector) ([]service.DeployTarget, error) {
	projCfg, err := loadProjectConfig(configPath)
	if err != nil {
		return nil, err
	}
	stackCfg, err := resolveStackConfig(stackConfigPath, env)
	if err != nil {
		return nil, err
	}
	targets, err := resolveDeployTargets(ctx, projCfg, env, "", stackCfg, svc)
	if err != nil {
		return nil, err
	}
	selected := selectTargets(targets, sel)
	if len(selected) == 0 {
		return nil, fmt.Errorf("no target of service %q matches the selector (region=%q host=%q) — run `inforge service instances %s %s` to see where it runs", svc, sel.region, sel.host, env, svc)
	}
	return selected, nil
}

// ── instances ────────────────────────────────────────────────────────────────────

func newServiceInstancesCmd(configPath *string) *cobra.Command {
	var stackConfig, sshKeyPath string
	var sel selector
	cmd := &cobra.Command{
		Use:   "instances <env> <service>",
		Short: "List the live processes of a service, with artifact integrity",
		Long: `List every live process of a service across the hosts it deploys to.

The instance id is regenerated on every restart, so it identifies this incarnation of
the process — pass it to another command's --instance to assert you are acting on the
same one you are looking at.

INTEGRITY compares the image the process is EXECUTING (/proc/<pid>/exe, which survives
the on-disk file being replaced) against the image on disk. STALE means a new binary is
staged but the process never restarted onto it: it is running the old code while every
dashboard claims otherwise.`,
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceInstances(cmd.Context(), *configPath, args[0], args[1], stackConfig, sshKeyPath, sel)
		},
	}
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to the infra stack config file (default: inforge.<env>.yaml)")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "path to the SSH deploy key (overrides INFORGE_DEPLOY_KEY)")
	addSelectorFlags(cmd, &sel, false)
	return cmd
}

func runServiceInstances(ctx context.Context, configPath, env, svc, stackConfigPath, sshKeyPath string, sel selector) error {
	targets, err := serviceTargets(ctx, configPath, env, svc, stackConfigPath, sel)
	if err != nil {
		return err
	}
	sshKeyPath, cleanup, err := resolveSSHKey(sshKeyPath)
	if err != nil {
		return err
	}
	defer cleanup()

	return renderInstances(os.Stdout, collectInstances(ctx, targets, sshKeyPath), env, svc)
}

// renderInstances prints the instances table. Split from the command so the output —
// including the STALE call-out, the one line an operator must not miss — is testable
// without an SSH daemon.
func renderInstances(out io.Writer, instances []instance, env, svc string) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "HOST\tREGION\tSTATE\tINSTANCE\tSINCE\tRESTARTS\tRUNNING\tINTEGRITY"); err != nil {
		return err
	}
	stale := 0
	for _, i := range instances {
		if i.integrity() == "STALE" {
			stale++
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			i.Target.HostDNS, i.Target.Scope, dash(i.State), dash(short(i.InstanceID)),
			dash(i.Since), dash(i.Restarts), dash(short(i.Running)), i.integrity()); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	// A silent table would let STALE scroll past unread — and STALE is the whole reason
	// the integrity column exists.
	if stale > 0 {
		if _, err := fmt.Fprintf(out, "\n%d instance(s) STALE: a newer binary is on disk but the process never restarted onto it.\nRun `inforge service restart %s %s` to pick it up.\n", stale, env, svc); err != nil {
			return err
		}
	}
	return nil
}

// dash renders an empty field as "-" so a column never silently collapses.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ── logs ─────────────────────────────────────────────────────────────────────────

func newServiceLogsCmd(configPath *string) *cobra.Command {
	var stackConfig, sshKeyPath, since string
	var lines int
	var follow bool
	var sel selector
	cmd := &cobra.Command{
		Use:   "logs <env> <service>",
		Short: "Read a service's journal on the hosts it runs on",
		Long: `Read a service's systemd journal directly on its hosts — no Grafana round-trip.

journald is persistent on our hosts, so this survives reboots.

--follow requires a single target (a merged live tail across hosts would interleave
unreadably); narrow with --host or --region.`,
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceLogs(cmd.Context(), *configPath, args[0], args[1], stackConfig, sshKeyPath, sel, lines, since, follow)
		},
	}
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to the infra stack config file (default: inforge.<env>.yaml)")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "path to the SSH deploy key (overrides INFORGE_DEPLOY_KEY)")
	cmd.Flags().IntVarP(&lines, "lines", "n", 100, "number of journal lines per host")
	cmd.Flags().StringVar(&since, "since", "", "only entries newer than this (journalctl syntax, e.g. \"-30min\", \"2026-07-13 09:00\")")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream new entries (requires a single target)")
	addSelectorFlags(cmd, &sel, false)
	return cmd
}

// journalScript builds the remote journalctl invocation. Every argument is shell-quoted:
// --since takes free text from the operator, so it is the one input here that is not a
// fixed template.
func journalScript(unit string, lines int, since string, follow bool) string {
	parts := []string{"sudo", "journalctl", "-u", remote.Quote(unit), "--no-pager", "-n", fmt.Sprintf("%d", lines)}
	if since != "" {
		parts = append(parts, "--since", remote.Quote(since))
	}
	if follow {
		parts = append(parts, "-f")
	}
	return strings.Join(parts, " ")
}

func runServiceLogs(ctx context.Context, configPath, env, svc, stackConfigPath, sshKeyPath string, sel selector, lines int, since string, follow bool) error {
	targets, err := serviceTargets(ctx, configPath, env, svc, stackConfigPath, sel)
	if err != nil {
		return err
	}
	if follow && len(targets) > 1 {
		return fmt.Errorf("--follow needs a single target, but %q runs on %d hosts — narrow with --host or --region", svc, len(targets))
	}
	sshKeyPath, cleanup, err := resolveSSHKey(sshKeyPath)
	if err != nil {
		return err
	}
	defer cleanup()

	return streamLogs(ctx, os.Stdout, targets, sshKeyPath, lines, since, follow, sshRun)
}

// streamLogs writes each target's journal to out, headed by the host when there is more
// than one (an unheaded merge of two hosts' logs is unreadable).
func streamLogs(ctx context.Context, out io.Writer, targets []service.DeployTarget, sshKeyPath string, lines int, since string, follow bool, run sshRunner) error {
	for _, t := range targets {
		if len(targets) > 1 {
			if _, err := fmt.Fprintf(out, "\n=== %s (%s)\n", t.HostDNS, t.Scope); err != nil {
				return err
			}
		}
		if err := run(ctx, sshKeyPath, sshAccount(t), journalScript(t.Unit, lines, since, follow), out); err != nil {
			return err
		}
	}
	return nil
}

// ── status ───────────────────────────────────────────────────────────────────────

func newServiceStatusCmd(configPath *string) *cobra.Command {
	var stackConfig, sshKeyPath string
	var sel selector
	cmd := &cobra.Command{
		Use:           "status <env> <service>",
		Short:         "Show systemd unit status on the hosts a service runs on",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := serviceTargets(cmd.Context(), *configPath, args[0], args[1], stackConfig, sel)
			if err != nil {
				return err
			}
			key, cleanup, err := resolveSSHKey(sshKeyPath)
			if err != nil {
				return err
			}
			defer cleanup()
			return streamStatus(cmd.Context(), os.Stdout, targets, key, sshRun)
		},
	}
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to the infra stack config file (default: inforge.<env>.yaml)")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "path to the SSH deploy key (overrides INFORGE_DEPLOY_KEY)")
	addSelectorFlags(cmd, &sel, false)
	return cmd
}

// statusScript builds the remote `systemctl status`. The `|| true` is load-bearing:
// systemctl exits non-zero for an inactive unit, and a service being DOWN is a finding to
// print — the very finding you ran this to get — not an error to abort the sweep on.
func statusScript(unit string) string {
	return fmt.Sprintf("sudo systemctl status %s --no-pager --lines=0 || true", remote.Quote(unit))
}

// streamStatus writes each target's unit status to out, headed by the host.
func streamStatus(ctx context.Context, out io.Writer, targets []service.DeployTarget, sshKeyPath string, run sshRunner) error {
	for _, t := range targets {
		if _, err := fmt.Fprintf(out, "\n=== %s (%s)\n", t.HostDNS, t.Scope); err != nil {
			return err
		}
		if err := run(ctx, sshKeyPath, sshAccount(t), statusScript(t.Unit), out); err != nil {
			return err
		}
	}
	return nil
}

// ── mesh-check ───────────────────────────────────────────────────────────────────

func newServiceMeshCheckCmd(configPath *string) *cobra.Command {
	var stackConfig, sshKeyPath, target, path string
	var sel selector
	cmd := &cobra.Command{
		Use:   "mesh-check <env> <service> --target <callee>",
		Short: "Call a mesh peer as this service, from its own host, and report the status",
		Long: `Make a real, fully-authenticated mesh call from <service> to <callee> and report
what came back.

This needs no code in the service. The mesh egress listener is plain HTTP on loopback and
the mesh PROXY attaches the client certificate, so a request made on the host carries the
service's real identity. The call therefore exercises the whole chain — egress config, the
routing table, network reachability, the TLS handshake (both leaves AND the trust bundle),
and the callee's allow-list.

Reading the result (with the default path, which no callee declares):

  404  the mesh path WORKS. You are allowed; the path is simply not a declared endpoint —
       the 404 is the callee's catch-all, served from INSIDE its allow-guard, so receiving
       it proves you authenticated, were admitted, and were routed.
  403  you are NOT in the callee's allowed_services — or its mesh proxy has not yet
       reloaded the allow-map that admits you.
  000  the call never completed: connectivity, TLS handshake, or trust-bundle failure.
  5xx  ambiguous: either --target names no such mesh peer, or the callee's app is down
       behind a healthy mesh proxy. The status alone cannot tell them apart.

Pass --path to probe a specific declared endpoint instead.`,
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if target == "" {
				return fmt.Errorf("--target is required: the mesh peer to call")
			}
			return runServiceMeshCheck(cmd.Context(), *configPath, args[0], args[1], stackConfig, sshKeyPath, sel, target, path)
		},
	}
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to the infra stack config file (default: inforge.<env>.yaml)")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "path to the SSH deploy key (overrides INFORGE_DEPLOY_KEY)")
	cmd.Flags().StringVar(&target, "target", "", "the mesh peer to call (the X-Mesh-Target service name)")
	cmd.Flags().StringVar(&path, "path", "/", "request path (default probes the callee's catch-all: 404 = reachable and allowed)")
	addSelectorFlags(cmd, &sel, false)
	return cmd
}

// meshCheckScript reads the caller's mesh egress URL out of its on-host descriptor —
// rather than recomputing the positional egress-port assignment here, which would be a
// second source of truth for something `meshpaths.EgressPort` already owns and could
// silently drift from what was actually deployed — and calls the peer through it.
//
// -o /dev/null -w '%{http_code}' prints the status and nothing else; a call that never
// completes prints 000, which is the signal we want (connectivity/TLS, not HTTP).
func meshCheckScript(svc, peer, path string) string {
	descriptor := service.DescriptorPath(svc)
	return strings.Join([]string{
		`set -u`,
		fmt.Sprintf(`url=$(sudo sed -n 's/^[[:space:]]*url:[[:space:]]*//p' %s 2>/dev/null | head -1)`, remote.Quote(descriptor)),
		`if [ -z "$url" ]; then echo "no mesh egress url in descriptor — is this service a mesh member (pki:)?" >&2; exit 1; fi`,
		`printf 'egress=%s\n' "$url"`,
		fmt.Sprintf(`printf 'status=%%s\n' "$(curl -s -o /dev/null -w '%%{http_code}' --max-time 10 -H %s "$url%s")"`,
			remote.Quote("X-Mesh-Target: "+peer), path),
	}, "\n")
}

// meshResult is one host's mesh probe: what its egress is, what came back, and what that
// means.
type meshResult struct {
	Target  service.DeployTarget
	Egress  string
	Status  string
	Verdict string
	OK      bool
}

func runServiceMeshCheck(ctx context.Context, configPath, env, svc, stackConfigPath, sshKeyPath string, sel selector, peer, path string) error {
	targets, err := serviceTargets(ctx, configPath, env, svc, stackConfigPath, sel)
	if err != nil {
		return err
	}
	sshKeyPath, cleanup, err := resolveSSHKey(sshKeyPath)
	if err != nil {
		return err
	}
	defer cleanup()

	results := meshCheck(ctx, targets, sshKeyPath, svc, peer, path, sshRun)
	return renderMeshCheck(os.Stdout, results, svc, peer, path)
}

// meshCheck probes the peer from every target host. Like collectInstancesWith, an
// unreachable host becomes a result rather than aborting the sweep — "it works from one
// region and not the other" is a finding, and bailing on the first failure would hide it.
func meshCheck(ctx context.Context, targets []service.DeployTarget, sshKeyPath, svc, peer, path string, run sshRunner) []meshResult {
	out := make([]meshResult, 0, len(targets))
	for _, t := range targets {
		var buf strings.Builder
		if err := run(ctx, sshKeyPath, sshAccount(t), meshCheckScript(svc, peer, path), &buf); err != nil {
			out = append(out, meshResult{Target: t, Verdict: "probe failed (host unreachable, or not a mesh member)"})
			continue
		}
		status := statusOf(buf.String())
		verdict, ok := meshVerdict(status)
		out = append(out, meshResult{
			Target:  t,
			Egress:  parseInspect(buf.String()).Egress,
			Status:  status,
			Verdict: verdict,
			OK:      ok,
		})
	}
	return out
}

// renderMeshCheck prints the results and fails the command if ANY host could not reach the
// peer. A per-host result matters: a mesh that works from one region and not another is
// exactly the shape of the incident this command exists for.
func renderMeshCheck(out io.Writer, results []meshResult, svc, peer, path string) error {
	if _, err := fmt.Fprintf(out, "calling %s -> %s (path %s), from %d host(s)\n\n", svc, peer, path, len(results)); err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "HOST\tREGION\tEGRESS\tSTATUS\tVERDICT"); err != nil {
		return err
	}
	failed := false
	for _, r := range results {
		if !r.OK {
			failed = true
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			r.Target.HostDNS, r.Target.Scope, dash(r.Egress), dash(r.Status), r.Verdict); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if failed {
		return fmt.Errorf("mesh check failed from at least one host")
	}
	return nil
}

// statusOf pulls the `status=` line out of meshCheckScript's output.
func statusOf(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "status="); ok {
			return v
		}
	}
	return ""
}

// meshVerdict turns an HTTP status into the thing the operator actually wants to know,
// and reports whether the check passed.
//
// 404 is a PASS, and that is not a bug in the reading. The callee's mesh proxy serves only
// its declared endpoint paths and answers anything else with a JSON 404 — but that 404 is
// emitted from INSIDE the allow-guard, so receiving it proves the peer authenticated us,
// admitted us, and routed the request. 403 is the guard refusing us. The two are exactly
// the states the mesh 403 incident was about.
//
// A 5xx is genuinely AMBIGUOUS and must not be dressed up as either outcome: verified
// against the live mesh, a bad `--target` returns 502 (the egress cannot route an unknown
// X-Mesh-Target) — and so does a callee whose app is down behind a healthy mesh proxy. We
// cannot tell those apart from the status alone, so we say so, and fail the check rather
// than reporting a reachable mesh the operator has not actually proven.
func meshVerdict(status string) (verdict string, ok bool) {
	switch {
	case status == "":
		return "no status", false
	case status == "000":
		return "UNREACHABLE (connectivity/TLS)", false
	case status == "403":
		return "FORBIDDEN (not in callee's allowed_services, or its allow-map is stale)", false
	case strings.HasPrefix(status, "5"):
		return "AMBIGUOUS 5xx (unknown --target, or the callee's app is down)", false
	default:
		// 404 (catch-all, allowed), 2xx (declared path), 401/400 (the app answered) —
		// all of them prove the request crossed the mesh and reached the callee.
		return "reachable", true
	}
}

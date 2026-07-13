package main

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/remote"
	"github.com/wardnet/inforge/internal/service"
)

// newServiceCmd operates on already-deployed services over SSH — ADR-0041's SSH plane.
// `restart` came first (after a rotated secret merges and deploys, services re-fetch
// their secrets on start, so a restart is what makes a new value live); `instances`,
// `logs`, `status` and `mesh-check` are the read-only siblings, in servicecmds.go.
//
// Everything here is systemd, /proc and the on-host descriptor, reached as the
// sudo-capable deploy user we already connect as — no service-side code at all.
func newServiceCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Operate on deployed services",
	}
	cmd.AddCommand(
		newServiceRestartCmd(configPath),
		newServiceInstancesCmd(configPath),
		newServiceLogsCmd(configPath),
		newServiceStatusCmd(configPath),
		newServiceMeshCheckCmd(configPath),
	)
	return cmd
}

func newServiceRestartCmd(configPath *string) *cobra.Command {
	var stackConfig, sshKeyPath string
	var sel selector
	cmd := &cobra.Command{
		Use:   "restart <env> <service>",
		Short: "Restart a service's systemd unit on every host it deploys to",
		Long: `Restart a service's systemd unit, then verify it came back up.

--instance asserts WHICH process you are restarting: the id is regenerated on every
start, so if the service restarted between your last look and this command, the id no
longer matches and the restart is refused rather than silently acting on a different
incarnation.`,
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceRestart(cmd.Context(), *configPath, args[0], args[1], stackConfig, sshKeyPath, sel)
		},
	}
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to the infra stack config file (default: inforge.<env>.yaml)")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "path to the SSH deploy key (overrides INFORGE_DEPLOY_KEY)")
	addSelectorFlags(cmd, &sel, true)
	return cmd
}

func runServiceRestart(ctx context.Context, configPath, env, svc, stackConfigPath, sshKeyPath string, sel selector) error {
	// Hosts, unit names and SSH users come from the deployDescriptor stack
	// output — the same source `releases deploy` delivers by, so restart can
	// never disagree with deploy about where a service lives.
	targets, err := serviceTargets(ctx, configPath, env, svc, stackConfigPath, sel)
	if err != nil {
		return err
	}
	var cleanupKey func()
	sshKeyPath, cleanupKey, err = resolveSSHKey(sshKeyPath)
	if err != nil {
		return err
	}
	defer cleanupKey()

	// --instance is a precondition, not a filter: probe the live processes and refuse if
	// the one named is gone. Skipped entirely when unset, so the common case costs no
	// extra round-trip.
	if sel.instance != "" {
		targets, err = requireInstance(collectInstances(ctx, targets, sshKeyPath), sel.instance)
		if err != nil {
			return err
		}
	}

	fmt.Printf("restarting %s on %d host(s) (env: %s)\n", svc, len(targets), env)
	for _, t := range targets {
		if err := sshRestart(ctx, t, sshKeyPath); err != nil {
			return err
		}
		fmt.Printf("restarted %s on %s\n", t.Unit, t.HostDNS)
	}
	return nil
}

// sshRestart restarts the target's systemd unit over SSH as the deploy user
// (sudo-capable — the same account and privileges sshDeliver already relies on)
// and verifies the unit came back up before reporting success.
func sshRestart(ctx context.Context, target service.DeployTarget, sshKeyPath string) error {
	sshUser := target.SSHUser
	if sshUser == "" {
		sshUser = "deploy"
	}
	account := fmt.Sprintf("%s@%s", sshUser, target.HostDNS)
	sshArgs := []string{"-i", sshKeyPath, "-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=yes", account, restartCommand(target.Unit)}
	if out, err := exec.CommandContext(ctx, "ssh", sshArgs...).CombinedOutput(); err != nil { // #nosec G204 -- ssh binary hardcoded; account/unit come from the deploy descriptor's resolved DeployTarget (Pulumi stack output), and the remote command is shell-quoted via remote.Quote
		return fmt.Errorf("restart %s on %s: %w\n%s", target.Unit, target.HostDNS, err, out)
	}
	return nil
}

// restartCommand builds the remote shell command: restart, then is-active so a
// unit that crashes immediately on its new configuration fails the command
// instead of reporting a successful restart.
func restartCommand(unit string) string {
	q := remote.Quote(unit)
	return fmt.Sprintf("sudo systemctl restart %s && sudo systemctl is-active %s", q, q)
}

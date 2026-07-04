package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/wardnet/inforge/internal/loader"
	"github.com/wardnet/inforge/internal/meshnginx"
	"github.com/wardnet/inforge/internal/meshplan"
)

// meshBaseline is the deploy-time half of the pull-based mesh leaf delivery
// (ADR-0033). After `pulumi up` has provisioned the mesh (proxies started on
// placeholder material; mesh workspace + per-host identities + on-host
// descriptors in place), it mints the env's real mesh material into the
// provider — the same core `inforge pki renew` runs — and SSH-triggers each
// mesh host's pull (`systemctl start wardnet-mesh-renew.service`) so the
// proxies converge NOW rather than on their next daily timer. The trigger
// pushes a SIGNAL, never material: renewal stays a pure provider write and
// each host's identity stays the only reader of its own path.
//
// configEnv/identityEnv mirror renewMeshCertsAs (a static deploy passes the
// env twice; `ephemeral up` passes source + slug). Targets come from the
// stack's meshDeployDescriptor output; an env with no mesh hosts is a no-op
// needing neither the secrets key nor an SSH key. The SSH key is resolved
// BEFORE any minting, so a misconfigured deploy fails in microseconds, not
// after a full provider write pass.
//
// A failed trigger — any number of them — is a WARNING, not an error: by then
// the material is already in the provider, so the affected hosts converge on
// their daily wardnet-mesh-renew timer (or a manual `systemctl start`). Only
// the mint phase (and the up-front key/targets resolution) can fail the
// deploy. All progress goes to w, never straight to stdout — in `-o json`
// mode the machine summary owns stdout.
func meshBaseline(ctx context.Context, s auto.Stack, dir, configEnv, identityEnv, sshKeyPath string, w io.Writer) error {
	outputs, err := s.Outputs(ctx)
	if err != nil {
		return fmt.Errorf("mesh baseline: read stack outputs: %w", err)
	}
	targets, err := decodeTargets[meshplan.DeployTarget](outputs, "meshDeployDescriptor")
	if err != nil {
		return fmt.Errorf("mesh baseline: %w", err)
	}
	if len(targets) == 0 {
		return nil
	}

	key, err := resolveSSHKey(sshKeyPath)
	if err != nil {
		return fmt.Errorf("mesh baseline: %w", err)
	}

	globalRes, err := loader.LoadGlobalResources(configEnv, dir)
	if err != nil {
		return fmt.Errorf("mesh baseline: %w", err)
	}
	regionalRes, err := loader.LoadResources(configEnv, dir)
	if err != nil {
		return fmt.Errorf("mesh baseline: %w", err)
	}
	count, err := renewMeshCertsAs(ctx, dir, configEnv, identityEnv, globalRes, regionalRes, "")
	if err != nil {
		return fmt.Errorf("mesh baseline: %w", err)
	}

	_, _ = fmt.Fprintf(w, "\nmesh baseline: minted %d leaf certificate(s); triggering %d host pull(s)\n", count, len(targets))
	sshArgs := []string{"-i", key, "-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=yes"}
	var failures []string
	for _, t := range targets {
		account := fmt.Sprintf("%s@%s", t.SSHUser, t.HostDNS)
		_, _ = fmt.Fprintf(w, "  mesh host %s (%s): pull\n", t.Host, account)
		cmd := exec.CommandContext(ctx, "ssh", append(append([]string{}, sshArgs...), account,
			"sudo systemctl start "+meshnginx.RenewUnitName+".service")...)
		cmd.Stdout, cmd.Stderr = w, w
		if err := cmd.Run(); err != nil {
			failures = append(failures, fmt.Sprintf("%s (%s): %v", t.Host, t.Scope, err))
		}
	}
	if len(failures) > 0 {
		_, _ = fmt.Fprintf(w, "warning: mesh baseline: %d host trigger(s) failed — the material is in the provider, so these hosts converge on their daily wardnet-mesh-renew timer, or start it manually:\n  - %s\n",
			len(failures), strings.Join(failures, "\n  - "))
	}
	return nil
}

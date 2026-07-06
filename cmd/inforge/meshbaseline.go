package main

import (
	"context"
	"fmt"
	"io"

	"github.com/wardnet/inforge/internal/loader"
)

// meshBaseline is the deploy-time half of mesh leaf delivery (ADR-0035). After
// `pulumi up` has provisioned the mesh (proxies started on placeholder
// material; per-host mesh descriptors in place), it mints the env's real mesh
// material and SSH-pushes it directly to each mesh host as leaf.age,
// signaling reload-or-restart so the proxies converge on real material NOW
// rather than waiting for the next `inforge pki renew`. It reuses exactly the
// same mint-and-push core `inforge pki renew` runs (renewMeshCertsAs) — there
// is no separate "trigger" step to keep in sync with it.
//
// configEnv/identityEnv mirror renewMeshCertsAs (a static deploy passes the
// env twice; `ephemeral up` passes source + slug). The SSH key is resolved
// BEFORE any minting, so a misconfigured deploy fails in microseconds, not
// after a full mint pass. All progress goes to w, never straight to stdout —
// in `-o json` mode the machine summary owns stdout.
//
// This does not change `inforge pki renew`'s standing invariant that it never
// runs Pulumi (.agents/rules/pki-renew-never-runs-pulumi.md) — the push is an
// imperative SSH step alongside the imperative leaf-minting, run here only
// after the Pulumi `up` this function is called from has already completed.
func meshBaseline(ctx context.Context, dir, configEnv, identityEnv, sshKeyPath string, w io.Writer) error {
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
	count, err := renewMeshCertsAs(ctx, dir, configEnv, identityEnv, globalRes, regionalRes, "", key, w)
	if err != nil {
		return fmt.Errorf("mesh baseline: %w", err)
	}

	_, _ = fmt.Fprintf(w, "\nmesh baseline: minted and pushed %d leaf certificate(s)\n", count)
	return nil
}

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/deployment"
	"github.com/wardnet/inforge/internal/loader"
	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/release"
	"github.com/wardnet/inforge/internal/service"
)

// newReleasesCmd is the `inforge releases` group: push builds + uploads an
// artifact to the R2 release store and prunes; deploy ships a stored SHA to the
// host(s) and records it in the per-env manifest; list reads that manifest. See
// ADR-0016.
func newReleasesCmd(configPath, dir *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "releases",
		Short: "Manage service release artifacts (R2 store + per-env manifest)",
	}
	cmd.AddCommand(
		newReleasesPushCmd(configPath),
		newReleasesDeployCmd(configPath, dir),
		newReleasesListCmd(configPath),
	)
	return cmd
}

// --- releases push ------------------------------------------------------------

func newReleasesPushCmd(configPath *string) *cobra.Command {
	var svc, sha, arch, deployDir string
	cmd := &cobra.Command{
		Use:           "push <env>",
		Short:         "Package a service artifact and upload it to the release store",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReleasesPush(cmd.Context(), *configPath, args[0], svc, sha, arch, deployDir)
		},
	}
	cmd.Flags().StringVarP(&svc, "service", "s", "", "service name to push (required)")
	cmd.Flags().StringVar(&sha, "sha", "", "artifact SHA key (default: $GITHUB_SHA)")
	cmd.Flags().StringVar(&arch, "arch", "", "CPU architecture this artifact was built for: amd64 or arm64 (required)")
	cmd.Flags().StringVar(&deployDir, "deploy-dir", "./deployments", "path to the deployments directory")
	mustRequire(cmd, "service", "arch")
	return cmd
}

func runReleasesPush(ctx context.Context, configPath, env, svc, sha, arch, deployDir string) error {
	if err := validateArchFlag(arch); err != nil {
		return err
	}
	sha, err := resolveSHA(sha)
	if err != nil {
		return err
	}

	artifactPath, err := resolveArtifactPath(deployDir, svc, env)
	if err != nil {
		return err
	}

	projCfg, err := loadProjectConfig(configPath)
	if err != nil {
		return err
	}
	store, err := newArtifactStore(ctx, projCfg)
	if err != nil {
		return err
	}

	fmt.Printf("packaging %s artifact from %s...\n", svc, artifactPath)
	payload, cleanup, err := packageDir(ctx, artifactPath)
	if err != nil {
		return err
	}
	defer cleanup()

	f, err := os.Open(payload) // #nosec G304 -- payload is an internally generated os.CreateTemp() name
	if err != nil {
		return fmt.Errorf("open payload: %w", err)
	}
	defer func() { _ = f.Close() }()

	fmt.Printf("uploading %s...\n", release.ArtifactKey(svc, sha, arch))
	if err := store.PutArtifact(ctx, svc, sha, arch, f); err != nil {
		return err
	}

	// Pruning is best-effort: the upload already succeeded, so a failed sweep
	// must not fail the push — it only leaves extra history behind.
	deleted, err := store.Prune(ctx, svc, projCfg.Artifacts.Keep)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: prune incomplete: %v\n", err)
	}
	if len(deleted) > 0 {
		fmt.Printf("pruned %d old artifact(s): %s\n", len(deleted), strings.Join(deleted, ", "))
	}
	fmt.Printf("pushed %s @ %s (%s)\n", svc, sha, arch)
	return nil
}

// --- releases deploy ----------------------------------------------------------

func newReleasesDeployCmd(configPath, dir *string) *cobra.Command {
	var svc, sha, deployDir, stackConfig, sshKeyPath string
	var dryRun bool
	cmd := &cobra.Command{
		Use:           "deploy <env>",
		Short:         "Deploy a stored artifact SHA to its host(s) and record it in the manifest",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReleasesDeploy(cmd.Context(), *configPath, *dir, args[0], svc, sha, deployDir, stackConfig, sshKeyPath, dryRun)
		},
	}
	cmd.Flags().StringVarP(&svc, "service", "s", "", "service name to deploy (required)")
	cmd.Flags().StringVar(&sha, "sha", "", "artifact SHA to deploy (required; default: $GITHUB_SHA)")
	cmd.Flags().StringVar(&deployDir, "deploy-dir", "./deployments", "path to the deployments directory")
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to the infra stack config file (default: inforge.<env>.yaml)")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "path to the SSH deploy key (overrides INFORGE_DEPLOY_KEY)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "resolve targets and verify the artifact without delivering")
	mustRequire(cmd, "service")
	return cmd
}

func runReleasesDeploy(ctx context.Context, configPath, dir, env, svc, sha, deployDir, stackConfigPath, sshKeyPath string, dryRun bool) error {
	sha, err := resolveSHA(sha)
	if err != nil {
		return err
	}

	platform, err := resolvePlatform(deployDir, svc, env)
	if err != nil {
		return err
	}

	projCfg, err := loadProjectConfig(configPath)
	if err != nil {
		return err
	}

	stackCfg, err := resolveStackConfig(stackConfigPath, env)
	if err != nil {
		return err
	}

	targets, err := resolveDeployTargets(ctx, projCfg, env, platform, stackCfg, svc)
	if err != nil {
		return err
	}

	store, err := newArtifactStore(ctx, projCfg)
	if err != nil {
		return err
	}

	// The SSH key is resolved before the dry-run branch (a deliberate behavior
	// change): probing each target's real architecture is the only way to
	// verify this fleet has every arch it needs already pushed, and that
	// verification is exactly what dry-run exists to do.
	var cleanupKey func()
	sshKeyPath, cleanupKey, err = resolveSSHKey(sshKeyPath)
	if err != nil {
		return err
	}
	defer cleanupKey()

	archRes, err := probeAndVerifyArch(ctx, store, svc, sha, targets, sshKeyPath)
	if err != nil {
		return err
	}

	fmt.Printf("deploying %s @ %s → %d host(s) across %d arch(es) (env: %s)\n", svc, sha, len(targets), len(archRes.archList), env)
	for _, t := range targets {
		fmt.Printf("  host: %s  arch: %s  folder: %s  unit: %s\n", t.HostDNS, archRes.archOf[t.HostDNS], t.Folder, t.Unit)
	}
	if dryRun {
		fmt.Println("(dry-run: artifact present for every detected arch, skipping delivery)")
		return nil
	}

	// A mesh service's leaf.age must already be on the host before the unit
	// restarts into it: the boot path decrypts+projects whatever leaf.age
	// already sits on disk, so the first release (and every update) re-mints
	// and SSH-pushes it here so the restart lands a fresh leaf rather than
	// crash-looping. `inforge releases deploy` runs from the infra repo, so it
	// holds the same INFORGE_SECRETS_KEY as `inforge deploy` and can sign from
	// the intermediate.
	if err := mintReleasedServiceLeaf(ctx, dir, env, svc, sshKeyPath); err != nil {
		return err
	}

	deliveryTargets, cleanupPayloads, err := downloadArchPayloads(ctx, store, svc, sha, targets, archRes)
	if err != nil {
		return err
	}
	defer cleanupPayloads()

	if err := deliverRelease(ctx, store, svc, env, sha, deliveryTargets, sshKeyPath); err != nil {
		return err
	}
	fmt.Printf("deployed %s @ %s\n", svc, sha)
	return nil
}

// --- releases list ------------------------------------------------------------

func newReleasesListCmd(configPath *string) *cobra.Command {
	var svc string
	cmd := &cobra.Command{
		Use:           "list <env>",
		Short:         "List the artifact SHA deployed to each host for a service+env",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReleasesList(cmd.Context(), *configPath, args[0], svc)
		},
	}
	cmd.Flags().StringVarP(&svc, "service", "s", "", "service name (required)")
	mustRequire(cmd, "service")
	return cmd
}

func runReleasesList(ctx context.Context, configPath, env, svc string) error {
	projCfg, err := loadProjectConfig(configPath)
	if err != nil {
		return err
	}
	store, err := newArtifactStore(ctx, projCfg)
	if err != nil {
		return err
	}
	m, _, exists, err := store.LoadManifest(ctx, svc, env)
	if err != nil {
		return err
	}
	if !exists || len(m.Deployments) == 0 {
		fmt.Printf("no deployments recorded for %s in %s\n", svc, env)
		return nil
	}

	hosts := make([]string, 0, len(m.Deployments))
	for h := range m.Deployments {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)

	fmt.Printf("%-45s  %-12s  %-6s  %s\n", "HOST", "SHA", "ARCH", "DEPLOYED")
	for _, h := range hosts {
		d := m.Deployments[h]
		sha := d.SHA
		if len(sha) > 12 {
			sha = sha[:12]
		}
		arch := d.Arch
		if arch == "" {
			arch = "-" // app deployment, or a manifest entry predating arch-awareness
		}
		fmt.Printf("%-45s  %-12s  %-6s  %s\n", h, sha, arch, d.DeployedAt.Format(time.RFC3339))
	}
	return nil
}

// --- shared helpers -----------------------------------------------------------

// resolveSHA returns the explicit --sha or falls back to $GITHUB_SHA (set in CI).
func resolveSHA(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if env := os.Getenv("GITHUB_SHA"); env != "" {
		return env, nil
	}
	return "", fmt.Errorf("artifact SHA required: pass --sha or set GITHUB_SHA")
}

// resolveArtifactPath loads the service's deployment manifests and returns the
// absolute local artifact directory for env.
func resolveArtifactPath(deployDir, svc, env string) (string, error) {
	cfg, err := deployment.LoadConfig(deployDir)
	if err != nil {
		return "", err
	}
	desc, err := deployment.LoadServiceDescriptor(deployDir, svc)
	if err != nil {
		return "", err
	}
	_, envCfg, err := deployment.Resolve(cfg, desc, svc, env)
	if err != nil {
		return "", err
	}
	return filepath.Abs(envCfg.ArtifactPath)
}

// resolvePlatform loads the service's deployment manifests and returns the
// resolved platform (used to locate the infra stack).
func resolvePlatform(deployDir, svc, env string) (string, error) {
	cfg, err := deployment.LoadConfig(deployDir)
	if err != nil {
		return "", err
	}
	desc, err := deployment.LoadServiceDescriptor(deployDir, svc)
	if err != nil {
		return "", err
	}
	platform, _, err := deployment.Resolve(cfg, desc, svc, env)
	return platform, err
}

// resolveSSHKey resolves the SSH deploy key FILE the SSH-pushing commands need
// (pki renew, releases deploy, db, service, ephemeral, the deploy mesh baseline).
// Precedence: an explicit --ssh-key, then INFORGE_DEPLOY_KEY (both file paths,
// returned verbatim), then the deploy key MATERIAL in INFORGE_DEPLOY_PRIVATE_KEY —
// the same secret the deploy program uses to provision every host over SSH —
// materialized to a private 0600 temp file. So a workflow configured with just the
// deploy key material (as the deploy job already sets) satisfies EVERY inforge
// command that SSHes, without also exporting a separate INFORGE_DEPLOY_KEY path.
//
// The returned cleanup removes the temp file (a no-op when a path was supplied); it
// is never nil, so a caller can always `defer cleanup()`.
func resolveSSHKey(sshKeyPath string) (string, func(), error) {
	noop := func() {}
	if sshKeyPath != "" {
		return sshKeyPath, noop, nil
	}
	if p := os.Getenv("INFORGE_DEPLOY_KEY"); p != "" {
		return p, noop, nil
	}
	if material := os.Getenv("INFORGE_DEPLOY_PRIVATE_KEY"); material != "" {
		p, err := writeTempKeyFile(material)
		if err != nil {
			return "", noop, fmt.Errorf("materialize deploy key: %w", err)
		}
		return p, func() { _ = os.Remove(p) }, nil // #nosec G703 -- p is f.Name() from writeTempKeyFile's os.CreateTemp(), an internally generated path; material is key content, never a path
	}
	return "", noop, fmt.Errorf("SSH deploy key required: pass --ssh-key, or set INFORGE_DEPLOY_KEY (a path) or INFORGE_DEPLOY_PRIVATE_KEY (the key material)")
}

// newArtifactStore builds the release store from the project config's artifacts
// block, reading the Cloudflare account ID from the environment.
func newArtifactStore(ctx context.Context, projCfg projectConfig) (*release.Store, error) {
	if !projCfg.Artifacts.configured() {
		return nil, fmt.Errorf("no artifacts backend configured — add an `artifacts:` block to inforge.yaml")
	}
	if projCfg.Artifacts.Backend.Type != "r2" {
		return nil, fmt.Errorf("artifacts backend type %q unsupported (only r2)", projCfg.Artifacts.Backend.Type)
	}
	bucket, err := projCfg.Artifacts.Backend.bucket()
	if err != nil {
		return nil, fmt.Errorf("artifacts.backend: %w", err)
	}
	return release.NewStore(ctx, bucket, os.Getenv("CLOUDFLARE_ACCOUNT_ID"))
}

// packageDir tars+gzips the contents of dir into a temp file, returning its path
// and a cleanup func.
func packageDir(ctx context.Context, dir string) (string, func(), error) {
	f, err := os.CreateTemp("", "inforge-artifact-*.tgz")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp payload: %w", err)
	}
	payload := f.Name()
	_ = f.Close()
	cleanup := func() { _ = os.Remove(payload) }
	if out, err := exec.CommandContext(ctx, "tar", "-czf", payload, "-C", dir, ".").CombinedOutput(); err != nil { // #nosec G204 -- tar binary hardcoded; payload is from os.CreateTemp and dir is the internally-resolved local artifact/build directory
		cleanup()
		return "", func() {}, fmt.Errorf("package artifact: %w\n%s", err, out)
	}
	return payload, cleanup, nil
}

// downloadArtifact streams (svc, sha, arch) from the store into a temp file,
// returning its path and a cleanup func. Pass release.NoArch for an
// architecture-agnostic (app) artifact.
func downloadArtifact(ctx context.Context, store *release.Store, svc, sha, arch string) (string, func(), error) {
	rc, err := store.GetArtifact(ctx, svc, sha, arch)
	if err != nil {
		return "", func() {}, err
	}
	defer func() { _ = rc.Close() }()

	f, err := os.CreateTemp("", "inforge-payload-*.tgz")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp payload: %w", err)
	}
	payload := f.Name()
	cleanup := func() { _ = os.Remove(payload) }
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("download artifact: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close payload: %w", err)
	}
	return payload, cleanup, nil
}

// resolveDeployTargets connects to the infra Pulumi stack for env and returns
// every DeployTarget for svc from the deployDescriptor output (one per region a
// multi-region service fans out to).
func resolveDeployTargets(ctx context.Context, projCfg projectConfig, env, platform string, stackCfg stackConfig, svc string) ([]service.DeployTarget, error) {
	// platform is informational — the backend URL and credentials come from the
	// project config the service CI inherits from the infra setup.
	_ = platform

	targets, err := resolveDescriptorTargets(ctx, projCfg, env, "deployDescriptor", stackCfg,
		func(t service.DeployTarget) bool { return t.Service == svc })
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("service %q not found in deployDescriptor for env %q — is it defined as a ServiceSpec in the infra resources?", svc, env)
	}
	// A legitimate match is either N regional targets (one per region, distinct
	// region Scopes) or exactly one global target (Scope == pki.ScopeGlobal) —
	// never both. `inforge validate` rejects a service name declared in both
	// scopes (checkCrossScopeNames), so this should be unreachable; it's a
	// defensive check against deploying to two unrelated hosts under one name
	// should that guard ever be bypassed (e.g. a stale stack from before the
	// validation existed).
	if hasMixedScope(targets, func(t service.DeployTarget) string { return t.Scope }, pki.ScopeGlobal) {
		return nil, fmt.Errorf("service %q matches both a global and a regional target in deployDescriptor for env %q — this is a service-name collision across scopes; rename one of the two ServiceSpecs (run `inforge validate` to locate them)", svc, env)
	}
	return targets, nil
}

// hasMixedScope reports whether targets contains both a globalScope entry and
// a non-global one — the signature of a same-named service/app declared in
// both the regional and global resource sets. Shared by the service and app
// deploy-target resolvers (resolveDeployTargets / resolveAppDeployTargets).
func hasMixedScope[T any](targets []T, scopeOf func(T) string, globalScope string) bool {
	hasGlobal, hasRegional := false, false
	for _, t := range targets {
		if scopeOf(t) == globalScope {
			hasGlobal = true
		} else {
			hasRegional = true
		}
	}
	return hasGlobal && hasRegional
}

// mintReleasedServiceLeaf mints a fresh leaf for the released service and
// writes it under /<svc>/mtls before delivery, so the unit restarts into a
// provider that already holds the leaf its boot path will project. Only an
// mtls_files: opted-in service (the raw-mTLS-plane exception) projects its own
// leaf — every other service is a no-op here: its mesh copy lives with the
// host's mesh proxy and is the deploy baseline's / renew cron's business
// (ADR-0033). It reuses renewMeshCertsAs — the same minting core as
// `inforge pki renew` — in per-service mode.
func mintReleasedServiceLeaf(ctx context.Context, dir, env, svc, sshKeyPath string) error {
	globalRes, err := loader.LoadGlobalResources(env, dir)
	if err != nil {
		return err
	}
	regionalRes, err := loader.LoadResources(env, dir)
	if err != nil {
		return err
	}
	// renewMeshCertsAs no-ops (before touching the store or INFORGE_SECRETS_KEY)
	// when the named service has no service-side mtls files to mint.
	count, err := renewMeshCertsAs(ctx, dir, env, env, globalRes, regionalRes, svc, sshKeyPath, os.Stdout)
	if err != nil {
		return fmt.Errorf("mint mesh leaf for %s: %w", svc, err)
	}
	if count > 0 {
		fmt.Printf("minted %d mesh leaf certificate(s) for %s\n", count, svc)
	}
	return nil
}

// mustRequire marks flags required, panicking on the impossible misconfiguration
// (a typo'd flag name) at startup rather than silently.
func mustRequire(cmd *cobra.Command, names ...string) {
	for _, n := range names {
		if err := cmd.MarkFlagRequired(n); err != nil {
			panic(err)
		}
	}
}

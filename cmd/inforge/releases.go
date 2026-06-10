package main

import (
	"context"
	"encoding/json"
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
	"github.com/wardnet/inforge/internal/release"
	"github.com/wardnet/inforge/internal/service"
)

// newReleasesCmd is the `inforge releases` group: push builds + uploads an
// artifact to the R2 release store and prunes; deploy ships a stored SHA to the
// host(s) and records it in the per-env manifest; list reads that manifest. See
// ADR-0016.
func newReleasesCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "releases",
		Short: "Manage service release artifacts (R2 store + per-env manifest)",
	}
	cmd.AddCommand(
		newReleasesPushCmd(configPath),
		newReleasesDeployCmd(configPath),
		newReleasesListCmd(configPath),
	)
	return cmd
}

// --- releases push ------------------------------------------------------------

func newReleasesPushCmd(configPath *string) *cobra.Command {
	var svc, sha, deployDir string
	cmd := &cobra.Command{
		Use:           "push <env>",
		Short:         "Package a service artifact and upload it to the release store",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReleasesPush(cmd.Context(), *configPath, args[0], svc, sha, deployDir)
		},
	}
	cmd.Flags().StringVarP(&svc, "service", "s", "", "service name to push (required)")
	cmd.Flags().StringVar(&sha, "sha", "", "artifact SHA key (default: $GITHUB_SHA)")
	cmd.Flags().StringVar(&deployDir, "deploy-dir", "./deployments", "path to the deployments directory")
	mustRequire(cmd, "service")
	return cmd
}

func runReleasesPush(ctx context.Context, configPath, env, svc, sha, deployDir string) error {
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

	f, err := os.Open(payload)
	if err != nil {
		return fmt.Errorf("open payload: %w", err)
	}
	defer func() { _ = f.Close() }()

	fmt.Printf("uploading %s/%s.tar.gz...\n", svc, sha)
	if err := store.PutArtifact(ctx, svc, sha, f); err != nil {
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
	fmt.Printf("pushed %s @ %s\n", svc, sha)
	return nil
}

// --- releases deploy ----------------------------------------------------------

func newReleasesDeployCmd(configPath *string) *cobra.Command {
	var svc, sha, deployDir, stackConfig, sshKeyPath string
	var dryRun bool
	cmd := &cobra.Command{
		Use:           "deploy <env>",
		Short:         "Deploy a stored artifact SHA to its host(s) and record it in the manifest",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReleasesDeploy(cmd.Context(), *configPath, args[0], svc, sha, deployDir, stackConfig, sshKeyPath, dryRun)
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

func runReleasesDeploy(ctx context.Context, configPath, env, svc, sha, deployDir, stackConfigPath, sshKeyPath string, dryRun bool) error {
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
	exists, err := store.ArtifactExists(ctx, svc, sha)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("artifact %s/%s.tar.gz not found in release store — run `inforge releases push` first", svc, sha)
	}

	fmt.Printf("deploying %s @ %s → %d host(s) (env: %s)\n", svc, sha, len(targets), env)
	for _, t := range targets {
		fmt.Printf("  host: %s  folder: %s  unit: %s\n", t.HostDNS, t.Folder, t.Unit)
	}
	if dryRun {
		fmt.Println("(dry-run: artifact present, skipping delivery)")
		return nil
	}

	sshKeyPath, err = resolveSSHKey(sshKeyPath)
	if err != nil {
		return err
	}

	payload, cleanup, err := downloadArtifact(ctx, store, svc, sha)
	if err != nil {
		return err
	}
	defer cleanup()

	for _, t := range targets {
		if err := sshDeliver(ctx, t, payload, sshKeyPath); err != nil {
			return err
		}
		if err := store.SetDeployment(ctx, svc, env, t.HostDNS, sha, time.Now()); err != nil {
			return fmt.Errorf("record manifest entry for %s: %w", t.HostDNS, err)
		}
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

	fmt.Printf("%-45s  %-12s  %s\n", "HOST", "SHA", "DEPLOYED")
	for _, h := range hosts {
		d := m.Deployments[h]
		sha := d.SHA
		if len(sha) > 12 {
			sha = sha[:12]
		}
		fmt.Printf("%-45s  %-12s  %s\n", h, sha, d.DeployedAt.Format(time.RFC3339))
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

func resolveSSHKey(sshKeyPath string) (string, error) {
	if sshKeyPath == "" {
		sshKeyPath = os.Getenv("INFORGE_DEPLOY_KEY")
	}
	if sshKeyPath == "" {
		return "", fmt.Errorf("SSH deploy key required: pass --ssh-key or set INFORGE_DEPLOY_KEY")
	}
	return sshKeyPath, nil
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
	if out, err := exec.CommandContext(ctx, "tar", "-czf", payload, "-C", dir, ".").CombinedOutput(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("package artifact: %w\n%s", err, out)
	}
	return payload, cleanup, nil
}

// downloadArtifact streams (svc, sha) from the store into a temp file, returning
// its path and a cleanup func.
func downloadArtifact(ctx context.Context, store *release.Store, svc, sha string) (string, func(), error) {
	rc, err := store.GetArtifact(ctx, svc, sha)
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

// sshDeliver scps an already-packaged payload to the target host, extracts it
// into the service folder, and restarts the unit. The folder, service user, and
// unit are provisioned by `inforge deploy`; this only delivers code + restarts.
func sshDeliver(ctx context.Context, target service.DeployTarget, payloadFile, sshKeyPath string) error {
	host := target.HostDNS
	sshUser := target.SSHUser
	if sshUser == "" {
		sshUser = "deploy"
	}
	account := fmt.Sprintf("%s@%s", sshUser, host)
	sshArgs := []string{"-i", sshKeyPath, "-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=yes"}

	fmt.Printf("uploading to %s...\n", host)
	scpArgs := append(sshArgs, payloadFile, account+":/tmp/inforge-payload.tgz")
	if out, err := exec.CommandContext(ctx, "scp", scpArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("upload payload to %s: %w\n%s", host, err, out)
	}

	fmt.Printf("extracting to %s and restarting %s...\n", target.Folder, target.Unit)
	remoteCmd := strings.Join([]string{
		fmt.Sprintf("sudo tar -xzf /tmp/inforge-payload.tgz -C %s", target.Folder),
		"rm -f /tmp/inforge-payload.tgz",
		fmt.Sprintf("sudo systemctl restart %s", target.Unit),
	}, " && ")
	sshRunArgs := append(sshArgs, account, remoteCmd)
	if out, err := exec.CommandContext(ctx, "ssh", sshRunArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("remote deploy to %s: %w\n%s", host, err, out)
	}
	return nil
}

// resolveDeployTargets connects to the infra Pulumi stack for env and returns
// every DeployTarget for svc from the deployDescriptor output (one per region a
// multi-region service fans out to).
func resolveDeployTargets(ctx context.Context, projCfg projectConfig, env, platform string, stackCfg stackConfig, svc string) ([]service.DeployTarget, error) {
	// platform is informational — the backend URL and credentials come from the
	// project config the service CI inherits from the infra setup.
	_ = platform

	s, _, err := upsertStack(ctx, env, projCfg)
	if err != nil {
		return nil, fmt.Errorf("connect to stack %q: %w", env, err)
	}
	if err := applyStackConfig(ctx, s, stackCfg); err != nil {
		return nil, fmt.Errorf("apply stack config: %w", err)
	}
	outputs, err := s.Outputs(ctx)
	if err != nil {
		return nil, fmt.Errorf("read stack outputs: %w", err)
	}
	raw, ok := outputs["deployDescriptor"]
	if !ok {
		return nil, fmt.Errorf("stack %q has no deployDescriptor output — has inforge deploy been run for this environment?", env)
	}
	// The Automation API deserialises pulumi.Any(DeployDescriptor) as
	// map[string]interface{}; round-trip through JSON to get a typed value.
	b, err := json.Marshal(raw.Value)
	if err != nil {
		return nil, fmt.Errorf("marshal deployDescriptor: %w", err)
	}
	var descriptor service.DeployDescriptor
	if err := json.Unmarshal(b, &descriptor); err != nil {
		return nil, fmt.Errorf("parse deployDescriptor: %w", err)
	}

	var targets []service.DeployTarget
	for _, t := range descriptor.Targets {
		if t.Service == svc {
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("service %q not found in deployDescriptor for env %q — is it defined as a ServiceSpec in the infra resources?", svc, env)
	}
	return targets, nil
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

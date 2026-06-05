package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/deployment"
	"github.com/wardnet/inforge/internal/service"
)

func newReleaseCmd() *cobra.Command {
	var env, svc, deployDir, stackConfig, sshKeyPath string
	var dryRun bool

	cmd := &cobra.Command{
		Use:           "release",
		Short:         "Release a service to a target environment",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRelease(cmd.Context(), env, svc, deployDir, stackConfig, sshKeyPath, dryRun)
		},
	}

	cmd.Flags().StringVarP(&env, "env", "e", "", "target environment (e.g. qa, prd) (required)")
	cmd.Flags().StringVarP(&svc, "service", "s", "", "service name to release (required)")
	cmd.Flags().StringVar(&deployDir, "deploy-dir", "./deployments", "path to the deployments directory")
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to the infra stack config file (default: inforge.<env>.yaml)")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "path to the SSH deploy key (overrides INFORGE_DEPLOY_KEY env var)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "resolve and print the deploy target without delivering")

	if err := cmd.MarkFlagRequired("env"); err != nil {
		panic(err)
	}
	if err := cmd.MarkFlagRequired("service"); err != nil {
		panic(err)
	}
	return cmd
}

func runRelease(ctx context.Context, env, svc, deployDir, stackConfigPath, sshKeyPath string, dryRun bool) error {
	// Load deployment manifests.
	cfg, err := deployment.LoadConfig(deployDir)
	if err != nil {
		return err
	}

	desc, err := deployment.LoadServiceDescriptor(deployDir, svc)
	if err != nil {
		return err
	}

	platform, envCfg, err := deployment.Resolve(cfg, desc, svc, env)
	if err != nil {
		return err
	}

	// Resolve stack config path.
	if stackConfigPath == "" {
		stackConfigPath = "inforge." + env + ".yaml"
	}
	stackCfg, err := loadStackConfig(stackConfigPath)
	if err != nil {
		return err
	}

	// Read the deploy descriptor from the Pulumi stack output.
	target, err := resolveDeployTarget(ctx, env, platform, stackCfg, svc)
	if err != nil {
		return err
	}

	artifactPath, err := filepath.Abs(envCfg.ArtifactPath)
	if err != nil {
		return fmt.Errorf("resolve artifact path: %w", err)
	}

	fmt.Printf("releasing %s → %s (env: %s)\n", svc, target.HostDNS, env)
	fmt.Printf("  artifact: %s\n", artifactPath)
	fmt.Printf("  host:     %s\n", target.HostDNS)
	fmt.Printf("  folder:   %s\n", target.Folder)
	fmt.Printf("  unit:     %s\n", target.Unit)
	fmt.Printf("  platform: %s\n", platform)

	if dryRun {
		fmt.Println("(dry-run: skipping delivery)")
		return nil
	}

	// Resolve SSH key.
	if sshKeyPath == "" {
		sshKeyPath = os.Getenv("INFORGE_DEPLOY_KEY")
	}
	if sshKeyPath == "" {
		return fmt.Errorf("SSH deploy key required: pass --ssh-key or set INFORGE_DEPLOY_KEY")
	}

	return deliver(ctx, target, artifactPath, sshKeyPath)
}

// resolveDeployTarget connects to the Pulumi stack for env in the infra project
// and extracts the DeployTarget for the named service from the deployDescriptor
// stack output.
func resolveDeployTarget(ctx context.Context, env, platform string, stackCfg stackConfig, svc string) (service.DeployTarget, error) {
	// The platform value (e.g. "wardnet/infra") is informational — the
	// Pulumi backend URL and credentials come from the stack config, which the
	// service CI inherits from the infra project setup.
	_ = platform

	projCfg, err := loadProjectConfig("inforge.yaml")
	if err != nil {
		return service.DeployTarget{}, fmt.Errorf("load project config: %w (does inforge.yaml exist in the infra project root?)", err)
	}

	s, _, err := upsertStack(ctx, env, projCfg)
	if err != nil {
		return service.DeployTarget{}, fmt.Errorf("connect to stack %q: %w", env, err)
	}

	if err := applyStackConfig(ctx, s, stackCfg); err != nil {
		return service.DeployTarget{}, fmt.Errorf("apply stack config: %w", err)
	}

	outputs, err := s.Outputs(ctx)
	if err != nil {
		return service.DeployTarget{}, fmt.Errorf("read stack outputs: %w", err)
	}

	raw, ok := outputs["deployDescriptor"]
	if !ok {
		return service.DeployTarget{}, fmt.Errorf("stack %q has no deployDescriptor output — has inforge deploy been run for this environment?", env)
	}

	// The Automation API deserialises pulumi.Any(DeployDescriptor) as
	// map[string]interface{}; round-trip through JSON to get a typed value.
	b, err := json.Marshal(raw.Value)
	if err != nil {
		return service.DeployTarget{}, fmt.Errorf("marshal deployDescriptor: %w", err)
	}
	var descriptor service.DeployDescriptor
	if err := json.Unmarshal(b, &descriptor); err != nil {
		return service.DeployTarget{}, fmt.Errorf("parse deployDescriptor: %w", err)
	}

	for _, t := range descriptor.Targets {
		if t.Service == svc {
			return t, nil
		}
	}
	return service.DeployTarget{}, fmt.Errorf("service %q not found in deployDescriptor for env %q — is it defined as a ServiceSpec in the infra resources?", svc, env)
}

// deliver packages the artifact and ships it to the target host via SSH.
func deliver(ctx context.Context, target service.DeployTarget, artifactPath, sshKeyPath string) error {
	payloadFile := filepath.Join(os.TempDir(), "inforge-payload.tgz")
	defer func() { _ = os.Remove(payloadFile) }()

	// Package artifact.
	fmt.Println("packaging artifact...")
	if out, err := exec.CommandContext(ctx, "tar", "-czf", payloadFile, "-C", artifactPath, ".").CombinedOutput(); err != nil {
		return fmt.Errorf("package artifact: %w\n%s", err, out)
	}

	host := target.HostDNS
	// Connect as the host's deploy_user (resolved into the descriptor). Falls
	// back to "deploy" for descriptors produced before SSHUser existed.
	sshUser := target.SSHUser
	if sshUser == "" {
		sshUser = "deploy"
	}
	account := fmt.Sprintf("%s@%s", sshUser, host)
	sshArgs := []string{"-i", sshKeyPath, "-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=yes"}

	// Upload payload.
	fmt.Printf("uploading to %s...\n", host)
	scpArgs := append(sshArgs, payloadFile, account+":/tmp/inforge-payload.tgz")
	if out, err := exec.CommandContext(ctx, "scp", scpArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("upload payload: %w\n%s", err, out)
	}

	// Extract into the service folder and restart the unit. The folder, the
	// no-login service user, and the unit itself are provisioned by
	// `inforge deploy`; release only delivers code and triggers the restart.
	fmt.Printf("deploying to %s and restarting %s...\n", target.Folder, target.Unit)
	remoteCmd := strings.Join([]string{
		fmt.Sprintf("sudo tar -xzf /tmp/inforge-payload.tgz -C %s", target.Folder),
		"rm -f /tmp/inforge-payload.tgz",
		fmt.Sprintf("sudo systemctl restart %s", target.Unit),
	}, " && ")

	sshRunArgs := append(sshArgs, account, remoteCmd)
	if out, err := exec.CommandContext(ctx, "ssh", sshRunArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("remote deploy: %w\n%s", err, out)
	}

	fmt.Printf("released %s successfully\n", target.Service)
	return nil
}

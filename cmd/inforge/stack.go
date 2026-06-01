package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/wardnet/inforge/program"
)

// upsertStack initialises a Pulumi stack for the given environment. For
// git-branch backends it fetches state before creation; the returned pushFn
// commits and pushes updated state after a successful apply. For all other
// backends pushFn is a no-op.
func upsertStack(ctx context.Context, stackName string, projCfg projectConfig) (auto.Stack, func() error, error) {
	backendURL, err := projCfg.backendURL()
	if err != nil {
		return auto.Stack{}, nil, err
	}

	pushFn := func() error { return nil }

	if projCfg.Backend.Type == "git-branch" {
		stateDir, push, gitErr := setupGitBranchBackend(ctx, projCfg.Backend.Branch)
		if gitErr != nil {
			return auto.Stack{}, nil, gitErr
		}
		backendURL = "file://" + stateDir
		pushFn = push
	}

	proj := workspace.Project{
		Name:    tokens.PackageName(projCfg.Name),
		Runtime: workspace.NewProjectRuntimeInfo("go", nil),
		Backend: &workspace.ProjectBackend{URL: backendURL},
	}
	s, err := auto.UpsertStackInlineSource(ctx, stackName, projCfg.Name, program.Run,
		auto.Project(proj),
		auto.WorkDir("."),
	)
	return s, pushFn, err
}

// setupGitBranchBackend fetches the remote state branch and extracts it into
// .pulumi-state/, pointing the Pulumi file backend at that directory. It
// returns the absolute path to use as the backend dir and a push function that
// commits and pushes updated state back to the branch.
func setupGitBranchBackend(ctx context.Context, branch string) (stateDir string, push func() error, err error) {
	if branch == "" {
		branch = "pulumi-state"
	}

	dir := ".pulumi-state"
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		return "", nil, fmt.Errorf("create state dir: %w", mkErr)
	}
	absDir, absErr := filepath.Abs(dir)
	if absErr != nil {
		return "", nil, absErr
	}

	// Fetch the remote branch; log but don't fail — it may not exist on first run.
	if err := exec.Command("git", "fetch", "origin", branch).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: git fetch origin %s: %v (branch may not exist yet)\n", branch, err)
	}

	// Extract the branch tree into absDir via git archive.
	archiveOut, archErr := exec.Command("git", "archive", "--format=tar", "origin/"+branch).Output()
	if archErr == nil && len(archiveOut) > 0 {
		tarCmd := exec.Command("tar", "-x", "-C", absDir)
		tarCmd.Stdin = bytes.NewReader(archiveOut)
		if tarErr := tarCmd.Run(); tarErr != nil {
			return "", nil, fmt.Errorf("extract state archive: %w", tarErr)
		}
	}
	// If archive fails (branch doesn't exist yet) we start with an empty dir.

	push = func() error {
		// Stage all files under the state dir, commit, and push to the state branch.
		// The push uses the refspec HEAD:<branch> so the state branch advances
		// independently of whatever branch the working tree is on.
		type step struct{ args []string }
		steps := []step{
			{[]string{"git", "add", "-f", absDir}},
			{[]string{"git", "commit", "-m", "chore: update pulumi state [skip ci]"}},
			{[]string{"git", "push", "origin", "HEAD:" + branch}},
		}
		for _, step := range steps {
			if out, cerr := exec.CommandContext(ctx, step.args[0], step.args[1:]...).CombinedOutput(); cerr != nil {
				return fmt.Errorf("%s: %w\n%s", strings.Join(step.args, " "), cerr, out)
			}
		}
		return nil
	}

	return absDir, push, nil
}

func applyStackConfig(ctx context.Context, s auto.Stack, stackCfg stackConfig) error {
	if len(stackCfg.Config) == 0 {
		return nil
	}
	cfgMap := make(auto.ConfigMap, len(stackCfg.Config))
	for k, v := range stackCfg.Config {
		cfgMap[k] = auto.ConfigValue{Value: v}
	}
	return s.SetAllConfig(ctx, cfgMap)
}

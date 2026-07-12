package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/wardnet/inforge/internal/types"
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

	s, err := auto.UpsertStackInlineSource(ctx, stackName, projCfg.Name, program.Run,
		auto.Project(inlineProject(projCfg, backendURL)),
		auto.WorkDir("."),
	)
	return s, pushFn, err
}

// inlineProject is the Pulumi project settings every inline-source workspace in
// this CLI runs under (Go runtime, project name, state backend). It is the one
// definition shared by upsertStack, createStack, selectStack, and
// ephemeralWorkspace, so the four entry points can never drift into configuring
// different projects against the same state. backendURL is passed in rather than
// re-derived because upsertStack rewrites it for the git-branch backend.
func inlineProject(projCfg projectConfig, backendURL string) workspace.Project {
	return workspace.Project{
		Name:    tokens.PackageName(projCfg.Name),
		Runtime: workspace.NewProjectRuntimeInfo("go", nil),
		Backend: &workspace.ProjectBackend{URL: backendURL},
	}
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
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return "", nil, fmt.Errorf("create state dir: %w", mkErr)
	}
	absDir, absErr := filepath.Abs(dir)
	if absErr != nil {
		return "", nil, absErr
	}

	// Fetch the remote branch; log but don't fail — it may not exist on first run.
	if err := exec.Command("git", "fetch", "origin", branch).Run(); err != nil { // #nosec G204 -- branch is from the local inforge.yaml git-branch backend config (operator-controlled), defaulted to "pulumi-state" if unset
		fmt.Fprintf(os.Stderr, "warning: git fetch origin %s: %v (branch may not exist yet)\n", branch, err)
	}

	// Extract the branch tree into absDir via git archive.
	archiveOut, archErr := exec.Command("git", "archive", "--format=tar", "origin/"+branch).Output() // #nosec G204 -- branch is from the local inforge.yaml git-branch backend config (operator-controlled)
	if archErr == nil && len(archiveOut) > 0 {
		tarCmd := exec.Command("tar", "-x", "-C", absDir) // #nosec G204 -- absDir is filepath.Abs of the hardcoded ".pulumi-state" directory, not external input
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
			// Same audit as the #nosec below: the command is a hardcoded literal, not reachable by
			// untrusted input. The suppression has to sit on the line directly above the finding —
			// Semgrep only reads that one line, not a comment block above it.
			// nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
			if out, cerr := exec.CommandContext(ctx, step.args[0], step.args[1:]...).CombinedOutput(); cerr != nil { // #nosec G204 -- steps are a hardcoded literal git add/commit/push sequence; the only variable, branch, is from the local backend config
				return fmt.Errorf("%s: %w\n%s", strings.Join(step.args, " "), cerr, out)
			}
		}
		return nil
	}

	return absDir, push, nil
}

// requireObjectBackend enforces the ADR-0028 hard requirement that the ephemeral
// commands run against an object-store Pulumi backend (s3 or r2). The reaper
// enumerates candidate stacks with ListStacks and classifies each from its
// per-stack persisted config; the git-branch backend serialises all state into a
// single branch tree (no per-stack object keying, no concurrent-up isolation) and
// the file backend is single-host, so neither supports the enumerate-and-reap
// model. It fails closed with the fix rather than degrading silently.
func requireObjectBackend(projCfg projectConfig) error {
	switch projCfg.Backend.Type {
	case "s3", "r2":
		return nil
	default:
		t := projCfg.Backend.Type
		if t == "" {
			t = "file"
		}
		return fmt.Errorf("ephemeral environments require an object-store state backend (s3 or r2), but inforge.yaml declares backend.type %q — per-stack object keying and ListStacks enumeration are not available on git-branch/file backends; configure an r2/s3 backend to use `inforge ephemeral`", t)
	}
}

// setProviderDefaults injects the project-level provider defaults into stack config
// so program.Run can resolve effective providers without the project file. It is a
// no-op when no defaults are configured, matching applyStackConfig's empty-guard.
func setProviderDefaults(ctx context.Context, s auto.Stack, d types.ProviderDefaults) error {
	if d.Compute == "" && len(d.Database) == 0 {
		return nil
	}
	b, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("marshal provider defaults: %w", err)
	}
	return s.SetConfig(ctx, "provider_defaults", auto.ConfigValue{Value: string(b)})
}

// setBackups injects the Postgres backup destination (ADR-0036) into stack config so
// program.Run can render each cluster host's backup timer without the project file:
// the bucket name and the resolved R2 endpoint. Resolving the endpoint here (CLI-side)
// validates CLOUDFLARE_ACCOUNT_ID up front, so a misconfiguration fails the command
// rather than mid-apply on the host.
//
// It ALWAYS writes both keys (empty when unconfigured) rather than no-op'ing on an
// absent block: stack config persists across runs, so removing the `backups:` block
// must clear the previously-set values — otherwise program.Run keeps reading a stale
// bucket and cannot be turned off. program.Run treats an empty bucket as unconfigured.
func setBackups(ctx context.Context, s auto.Stack, b backupsConfig) error {
	var bucket, endpoint string
	if b.configured() {
		var err error
		bucket, endpoint, err = b.resolve()
		if err != nil {
			return fmt.Errorf("backups: %w", err)
		}
	}
	if err := s.SetConfig(ctx, "backups_bucket", auto.ConfigValue{Value: bucket}); err != nil {
		return err
	}
	return s.SetConfig(ctx, "backups_endpoint", auto.ConfigValue{Value: endpoint})
}

// ephemeralWorkspace builds a Pulumi LocalWorkspace bound to the project's
// object-store backend, for enumerating stacks during `reap` (ListStacks) without
// first selecting one. The inline program is attached so a stack selected from
// this workspace can later be destroyed. Callers must have already passed
// requireObjectBackend, so the git-branch state-fetch path is never reached here.
func ephemeralWorkspace(ctx context.Context, projCfg projectConfig) (auto.Workspace, error) {
	backendURL, err := projCfg.backendURL()
	if err != nil {
		return nil, err
	}
	return auto.NewLocalWorkspace(ctx,
		auto.Project(inlineProject(projCfg, backendURL)),
		auto.Program(program.Run),
		auto.WorkDir("."),
	)
}

// createStack initialises a NEW Pulumi stack for an ephemeral env, failing if a
// stack with that name already exists. Unlike upsertStack — which SELECTs an
// existing stack of the same name — creation itself is the atomic collision
// guard: two concurrent `ephemeral up` runs with the same slug cannot both
// succeed, so neither can stamp ephemeral+expires_at config onto a stack the
// other (or a permanent env) already owns. This closes the check-then-act race a
// separate "does it exist?" probe would leave open. Ephemeral commands require an
// object-store backend (requireObjectBackend), so the git-branch state path that
// upsertStack handles is unreachable here.
func createStack(ctx context.Context, stackName string, projCfg projectConfig) (auto.Stack, error) {
	backendURL, err := projCfg.backendURL()
	if err != nil {
		return auto.Stack{}, err
	}
	s, err := auto.NewStackInlineSource(ctx, stackName, projCfg.Name, program.Run,
		auto.Project(inlineProject(projCfg, backendURL)),
		auto.WorkDir("."),
	)
	if err != nil {
		if auto.IsCreateStack409Error(err) {
			return auto.Stack{}, fmt.Errorf("a stack named %q already exists — `up` creates a NEW ephemeral env and will not adopt an existing stack (a permanent env or a live ephemeral one); pick a different --slug, or run `inforge ephemeral down %s` first", stackName, stackName)
		}
		return auto.Stack{}, fmt.Errorf("create ephemeral stack %q: %w", stackName, err)
	}
	return s, nil
}

// selectStack SELECTS an existing Pulumi stack, failing if it does not exist.
// The ephemeral teardown paths must never create a stack: an upsert on a typo'd
// slug would mint an empty stack that no `up` owns and no `reap` can classify
// (it carries neither ephemeral nor expires_at), permanently burning that slug —
// createStack would then refuse it forever. Ephemeral commands require an
// object-store backend, so upsertStack's git-branch state path is unreachable here.
func selectStack(ctx context.Context, stackName string, projCfg projectConfig) (auto.Stack, error) {
	backendURL, err := projCfg.backendURL()
	if err != nil {
		return auto.Stack{}, err
	}
	s, err := auto.SelectStackInlineSource(ctx, stackName, projCfg.Name, program.Run,
		auto.Project(inlineProject(projCfg, backendURL)),
		auto.WorkDir("."),
	)
	if err != nil {
		if auto.IsSelectStack404Error(err) {
			return auto.Stack{}, fmt.Errorf("no stack named %q exists — check the slug (`inforge ephemeral reap --dry-run` lists the live ephemeral envs)", stackName)
		}
		return auto.Stack{}, fmt.Errorf("select stack %q: %w", stackName, err)
	}
	return s, nil
}

// stackConfigValue reads a plain config key from a stack's config map, tolerating
// the project-namespace prefix the backend stores keys under (e.g. "ephemeral" is
// persisted as "<project>:ephemeral"). It returns "" when the key is absent.
func stackConfigValue(cfg auto.ConfigMap, projName, key string) string {
	if v, ok := cfg[key]; ok {
		return v.Value
	}
	if v, ok := cfg[projName+":"+key]; ok {
		return v.Value
	}
	return ""
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

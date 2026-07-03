package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/spf13/cobra"
)

func newEphemeralReapCmd(configPath *string) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "reap",
		Short: "Destroy every expired ephemeral environment (cron-friendly)",
		Long: "Enumerate all stacks on the state backend, classify each from its persisted\n" +
			"stack config, and tear down those that are BOTH ephemeral and past their\n" +
			"expires_at deadline — the three-signal guarantee that no permanent stack can\n" +
			"ever be reaped. Destroys by default; --dry-run only lists. Requires r2/s3.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runEphemeralReap(cmd.Context(), *configPath, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list expired ephemeral envs without destroying them")
	return cmd
}

// reapCandidate is one stack the reaper classified as an expired ephemeral env.
// reason is set only for a fail-safe reap (an ephemeral stack whose expires_at is
// missing or unreadable) so the operator sees why an env with no usable deadline
// was torn down.
type reapCandidate struct {
	name    string
	srcEnv  string
	expires time.Time
	reason  string
	// stack is the handle classifyStack already selected (and read config from),
	// reused by reapStack so a reaped env is selected once, not twice, per run.
	stack auto.Stack
}

func runEphemeralReap(ctx context.Context, configPath string, dryRun bool) error {
	projCfg, err := loadProjectConfig(configPath)
	if err != nil {
		return err
	}
	if err := requireObjectBackend(projCfg); err != nil {
		return err
	}
	// A non-dry-run destroy re-runs the inline program; pin the agent version
	// exactly as `up`/`down` do.
	if err := os.Setenv("INFORGE_VERSION", version); err != nil {
		return fmt.Errorf("set INFORGE_VERSION: %w", err)
	}

	ws, err := ephemeralWorkspace(ctx, projCfg)
	if err != nil {
		return fmt.Errorf("open project workspace: %w", err)
	}
	summaries, err := ws.ListStacks(ctx)
	if err != nil {
		return fmt.Errorf("list stacks: %w", err)
	}

	now := time.Now()
	var candidates []reapCandidate
	var warnings []string
	for _, sum := range summaries {
		cand, ok, err := classifyStack(ctx, ws, projCfg.Name, sum.Name, now)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", sum.Name, err))
			continue
		}
		if ok {
			candidates = append(candidates, cand)
		}
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: skipping stack %s\n", w)
	}

	if len(candidates) == 0 {
		fmt.Println("no expired ephemeral environments to reap")
		return nil
	}

	if dryRun {
		fmt.Printf("%d expired ephemeral env(s) would be reaped:\n", len(candidates))
		for _, c := range candidates {
			fmt.Printf("  %s (source %s, %s)\n", c.name, c.srcEnv, c.deadlineNote())
		}
		fmt.Println("(dry-run: nothing destroyed)")
		return nil
	}

	var failures []string
	for _, c := range candidates {
		fmt.Printf("reaping %s (source %s, %s)\n", c.name, c.srcEnv, c.deadlineNote())
		if err := reapStack(ctx, c); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", c.name, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("reap: %d env(s) failed to tear down:\n  - %s", len(failures), strings.Join(failures, "\n  - "))
	}
	fmt.Printf("reaped %d ephemeral env(s)\n", len(candidates))
	return nil
}

// classifyStack reads a stack's persisted config and decides whether it is an
// expired ephemeral env. The three-signal rule (ADR-0028): reap iff
// ephemeral == "true" AND expires_at is in the past — both written only by `up`,
// so a permanent stack (which sets neither) can never match. A malformed
// expires_at is surfaced as an error (the caller warns + skips) rather than
// silently treated as expired or not.
func classifyStack(ctx context.Context, ws auto.Workspace, projName, name string, now time.Time) (reapCandidate, bool, error) {
	s, err := auto.SelectStack(ctx, name, ws)
	if err != nil {
		return reapCandidate{}, false, fmt.Errorf("select: %w", err)
	}
	cfg, err := s.GetAllConfig(ctx)
	if err != nil {
		return reapCandidate{}, false, fmt.Errorf("read config: %w", err)
	}
	expires, reap, reason := reapDecision(
		stackConfigValue(cfg, projName, cfgKeyEphemeral),
		stackConfigValue(cfg, projName, cfgKeyExpiresAt),
		now,
	)
	if !reap {
		return reapCandidate{}, false, nil
	}
	return reapCandidate{
		name:    name,
		srcEnv:  stackConfigValue(cfg, projName, cfgKeySourceEnvironment),
		expires: expires,
		reason:  reason,
		stack:   s,
	}, true, nil
}

// reapDecision is the pure three-signal classification (ADR-0028): a stack is
// reaped iff its ephemeral config is exactly "true" AND its expires_at is in the
// past. A non-ephemeral stack is NEVER reaped — the strong signal that protects
// every permanent env. An ephemeral stack whose expires_at is missing or
// unreadable is reaped **fail-safe** (reap=true with a `reason`): the stack is
// unambiguously a disposable preview, and bounding its billing matters more than
// keeping a deadline-less env alive — the alternative (skip-forever) would leak
// it indefinitely. The deadline is returned (zero for a fail-safe reap) so the
// caller can report when it lapsed.
func reapDecision(ephemeralVal, expiresRaw string, now time.Time) (expires time.Time, reap bool, reason string) {
	if ephemeralVal != "true" {
		return time.Time{}, false, ""
	}
	if expiresRaw == "" {
		return time.Time{}, true, "missing expires_at — reaping fail-safe"
	}
	expires, err := parseExpiresAt(expiresRaw)
	if err != nil {
		return time.Time{}, true, fmt.Sprintf("unreadable expires_at %q — reaping fail-safe", expiresRaw)
	}
	return expires, now.After(expires), ""
}

// reapStack runs the down path on one expired candidate: re-assert the identity
// config (so the destroy resolves the same source tree), then destroy + remove.
// It refuses a candidate with no source_environment — exactly like `down`
// (runEphemeralDown) — because the destroy re-runs the inline program over the
// source tree, and without it the loaders would default to resources/<slug>/
// (which never existed) and fail mid-destroy, leaving a billable orphan retried
// on every reap. Surfacing it as a failure forces a manual teardown instead.
func reapStack(ctx context.Context, c reapCandidate) error {
	if c.srcEnv == "" {
		return fmt.Errorf("ephemeral but has no %s config — refusing to auto-destroy (its source tree can't be resolved); tear it down manually", cfgKeySourceEnvironment)
	}
	// Reuse the stack handle classifyStack already selected — no second SelectStack.
	if err := c.stack.SetAllConfig(ctx, map[string]auto.ConfigValue{
		cfgKeyEnvironment:       {Value: c.name},
		cfgKeySourceEnvironment: {Value: c.srcEnv},
	}); err != nil {
		return fmt.Errorf("re-assert config: %w", err)
	}
	return destroyEphemeralStack(ctx, c.stack, c.name)
}

// deadlineNote renders a candidate's expiry for display: the lapsed deadline, or
// the fail-safe reason when there was no usable expires_at.
func (c reapCandidate) deadlineNote() string {
	if c.reason != "" {
		return c.reason
	}
	return "expired " + c.expires.Format(time.RFC3339)
}

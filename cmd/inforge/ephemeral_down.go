package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"
	"github.com/spf13/cobra"
)

func newEphemeralDownCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "down <slug>",
		Short:         "Tear down an ephemeral environment and remove its stack",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEphemeralDown(cmd.Context(), *configPath, args[0])
		},
	}
	return cmd
}

func runEphemeralDown(ctx context.Context, configPath, slug string) error {
	projCfg, err := loadProjectConfig(configPath)
	if err != nil {
		return err
	}
	if err := requireObjectBackend(projCfg); err != nil {
		return err
	}
	// The inline program re-runs during destroy to resolve the graph; it provisions
	// services from the released binary version, so pin it exactly as `up` did.
	if err := os.Setenv("INFORGE_VERSION", version); err != nil {
		return fmt.Errorf("set INFORGE_VERSION: %w", err)
	}

	s, _, err := upsertStack(ctx, slug, projCfg)
	if err != nil {
		return fmt.Errorf("select ephemeral stack %q: %w", slug, err)
	}

	cfg, err := s.GetAllConfig(ctx)
	if err != nil {
		return fmt.Errorf("read stack config for %q: %w", slug, err)
	}
	// The persisted source mapping is the only way `down` can locate the source's
	// resource tree for the destroy program. Its absence means this stack was not
	// created by `ephemeral up` (or its config was lost) — refuse rather than run
	// the program against the wrong (defaulted) source.
	srcEnv := stackConfigValue(cfg, projCfg.Name, cfgKeySourceEnvironment)
	if srcEnv == "" {
		return fmt.Errorf("stack %q has no %s config — it does not look like an ephemeral env created by `inforge ephemeral up`", slug, cfgKeySourceEnvironment)
	}
	// Re-assert the identity keys so a destroy resolves the same source tree and
	// identity `up` used, even if the persisted config drifted.
	if err := s.SetAllConfig(ctx, map[string]auto.ConfigValue{
		cfgKeyEnvironment:       {Value: slug},
		cfgKeySourceEnvironment: {Value: srcEnv},
	}); err != nil {
		return fmt.Errorf("re-assert ephemeral config for %q: %w", slug, err)
	}

	fmt.Printf("tearing down ephemeral env %q (source %q)\n", slug, srcEnv)
	return destroyEphemeralStack(ctx, s, slug)
}

// destroyEphemeralStack runs a Pulumi destroy on s, rendering the engine event
// stream through the shared Printer (mirroring runStackUp), then removes the stack
// from the backend so a reaped/torn-down env leaves no orphaned stack object. It
// is shared by `down` and `reap`.
func destroyEphemeralStack(ctx context.Context, s auto.Stack, slug string) error {
	_, destroyErr := streamEngineRun(os.Stdout, fmt.Sprintf("Destroying (%s):\n\n", slug),
		func(ch chan events.EngineEvent, progress, errProgress io.Writer) error {
			_, err := s.Destroy(ctx,
				optdestroy.EventStreams(ch),
				optdestroy.ProgressStreams(progress),
				optdestroy.ErrorProgressStreams(errProgress),
			)
			return err
		})
	if destroyErr != nil {
		return fmt.Errorf("destroy ephemeral env %q: %w", slug, destroyErr)
	}

	if err := s.Workspace().RemoveStack(ctx, slug); err != nil {
		return fmt.Errorf("remove ephemeral stack %q after destroy: %w", slug, err)
	}
	fmt.Printf("ephemeral env %q torn down and stack removed.\n", slug)
	return nil
}

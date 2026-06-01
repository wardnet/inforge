package main

import (
	"context"
	"fmt"
	"os"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
	"github.com/spf13/cobra"
)

func newPreviewCmd(configPath *string) *cobra.Command {
	var stack, stackConfig string

	cmd := &cobra.Command{
		Use:           "preview",
		Short:         "Preview infrastructure changes for a stack",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPreview(cmd.Context(), stack, stackConfig, *configPath)
		},
	}

	cmd.Flags().StringVarP(&stack, "stack", "s", "", "stack name / environment (required)")
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to stack config (default: inforge.<stack>.yaml)")
	if err := cmd.MarkFlagRequired("stack"); err != nil {
		panic(err)
	}
	return cmd
}

func runPreview(ctx context.Context, stackName, stackConfigPath, configPath string) error {
	projCfg, err := loadProjectConfig(configPath)
	if err != nil {
		return err
	}

	if stackConfigPath == "" {
		stackConfigPath = "inforge." + stackName + ".yaml"
	}
	stackCfg, err := loadStackConfig(stackConfigPath)
	if err != nil {
		return err
	}

	s, err := upsertStack(ctx, stackName, projCfg)
	if err != nil {
		return fmt.Errorf("initialise stack: %w", err)
	}

	if err := applyStackConfig(ctx, s, stackCfg); err != nil {
		return fmt.Errorf("set stack config: %w", err)
	}

	_, err = s.Preview(ctx,
		optpreview.ProgressStreams(os.Stdout),
		optpreview.ErrorProgressStreams(os.Stderr),
	)
	if err != nil {
		return fmt.Errorf("preview: %w", err)
	}
	return nil
}

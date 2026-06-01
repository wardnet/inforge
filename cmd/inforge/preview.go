package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
	"github.com/spf13/cobra"
)

func newPreviewCmd(configPath *string) *cobra.Command {
	var stack, stackConfig, output string
	var allowMultiple bool

	cmd := &cobra.Command{
		Use:           "preview",
		Short:         "Preview infrastructure changes for a stack",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPreview(cmd.Context(), stack, stackConfig, *configPath, output, allowMultiple)
		},
	}

	cmd.Flags().StringVarP(&stack, "stack", "s", "", "stack name / environment (required)")
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to stack config (default: inforge.<stack>.yaml)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: '' (default human) or 'json'")
	cmd.Flags().BoolVar(&allowMultiple, "allow-multiple", false, "allow running when multiple environments have changes")
	if err := cmd.MarkFlagRequired("stack"); err != nil {
		panic(err)
	}
	return cmd
}

func runPreview(ctx context.Context, stackName, stackConfigPath, configPath, output string, allowMultiple bool) error {
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

	s, _, err := upsertStack(ctx, stackName, projCfg)
	if err != nil {
		return fmt.Errorf("initialise stack: %w", err)
	}

	if err := applyStackConfig(ctx, s, stackCfg); err != nil {
		return fmt.Errorf("set stack config: %w", err)
	}

	// When emitting machine-readable JSON, route Pulumi progress to stderr so
	// stdout carries only the JSON summary (> /tmp/preview.json in CI).
	progressOut := os.Stdout
	if output == "json" {
		progressOut = os.Stderr
	}
	result, err := s.Preview(ctx,
		optpreview.ProgressStreams(progressOut),
		optpreview.ErrorProgressStreams(os.Stderr),
	)
	if err != nil {
		return fmt.Errorf("preview: %w", err)
	}

	if output == "json" {
		counts := make(map[string]int, len(result.ChangeSummary))
		for op, n := range result.ChangeSummary {
			counts[string(op)] = n
		}
		return printChangeSummaryJSON(stackName, counts)
	}
	return nil
}

// changeSummaryOutput is the JSON structure emitted by --output json.
type changeSummaryOutput struct {
	Environment string         `json:"environment"`
	Summary     map[string]int `json:"summary"`
}

func printChangeSummaryJSON(env string, counts map[string]int) error {
	out := changeSummaryOutput{Environment: env, Summary: counts}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

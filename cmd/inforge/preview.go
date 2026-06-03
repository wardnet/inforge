package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/output"
)

func newPreviewCmd(configPath *string) *cobra.Command {
	var stack, stackConfig, format string
	var allowMultiple bool

	cmd := &cobra.Command{
		Use:           "preview",
		Short:         "Preview infrastructure changes for a stack",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPreview(cmd.Context(), stack, stackConfig, *configPath, format, allowMultiple)
		},
	}

	cmd.Flags().StringVarP(&stack, "stack", "s", "", "stack name / environment (required)")
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to stack config (default: inforge.<stack>.yaml)")
	cmd.Flags().StringVarP(&format, "output", "o", "", "output format: '' (default human) or 'json'")
	cmd.Flags().BoolVar(&allowMultiple, "allow-multiple", false, "allow running when multiple environments have changes")
	if err := cmd.MarkFlagRequired("stack"); err != nil {
		panic(err)
	}
	return cmd
}

func runPreview(ctx context.Context, stackName, stackConfigPath, configPath, format string, allowMultiple bool) error {
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

	if format == "json" {
		// JSON mode: route Pulumi progress to stderr so stdout carries only the
		// JSON summary (> /tmp/preview.json in CI).
		result, previewErr := s.Preview(ctx,
			optpreview.ProgressStreams(os.Stderr),
			optpreview.ErrorProgressStreams(os.Stderr),
		)
		if previewErr != nil {
			return fmt.Errorf("preview: %w", previewErr)
		}
		counts := make(map[string]int, len(result.ChangeSummary))
		for op, n := range result.ChangeSummary {
			counts[string(op)] = n
		}
		return printChangeSummaryJSON(stackName, counts)
	}

	// Human mode: stream structured per-resource output.
	ch := output.NewEventChannel()
	done := make(chan struct{})
	go func() {
		output.Stream(ch, os.Stdout)
		close(done)
	}()

	fmt.Fprintf(os.Stdout, "Previewing (%s):\n\n", stackName)
	_, previewErr := s.Preview(ctx,
		optpreview.EventStreams(ch),
		optpreview.ErrorProgressStreams(os.Stderr),
	)
	<-done

	if previewErr != nil {
		return fmt.Errorf("preview: %w", previewErr)
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

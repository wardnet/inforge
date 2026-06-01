package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/spf13/cobra"
)

func newDeployCmd(configPath *string) *cobra.Command {
	var stack, stackConfig, output string
	var yes, allowMultiple bool

	cmd := &cobra.Command{
		Use:           "deploy",
		Short:         "Deploy infrastructure changes for a stack",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDeploy(cmd.Context(), stack, stackConfig, *configPath, output, yes, allowMultiple)
		},
	}

	cmd.Flags().StringVarP(&stack, "stack", "s", "", "stack name / environment (required)")
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to stack config (default: inforge.<stack>.yaml)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: '' (default human) or 'json'")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "auto-approve without prompt")
	cmd.Flags().BoolVar(&allowMultiple, "allow-multiple", false, "allow running when multiple environments have changes")
	if err := cmd.MarkFlagRequired("stack"); err != nil {
		panic(err)
	}
	return cmd
}

func runDeploy(ctx context.Context, stackName, stackConfigPath, configPath, output string, yes, allowMultiple bool) error {
	if !yes {
		fmt.Printf("Deploy stack %q? Type 'yes' to confirm: ", stackName)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		if strings.TrimSpace(scanner.Text()) != "yes" {
			fmt.Println("deploy cancelled")
			return nil
		}
	}

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

	s, pushState, err := upsertStack(ctx, stackName, projCfg)
	if err != nil {
		return fmt.Errorf("initialise stack: %w", err)
	}

	if err := applyStackConfig(ctx, s, stackCfg); err != nil {
		return fmt.Errorf("set stack config: %w", err)
	}

	// When emitting machine-readable JSON, route Pulumi progress to stderr so
	// stdout carries only the JSON summary (> /tmp/deploy.json in CI).
	progressOut := os.Stdout
	if output == "json" {
		progressOut = os.Stderr
	}
	result, err := s.Up(ctx,
		optup.ProgressStreams(progressOut),
		optup.ErrorProgressStreams(os.Stderr),
	)
	if err != nil {
		return fmt.Errorf("deploy: %w", err)
	}

	// Push state to the git-branch backend after a successful apply.
	if pushErr := pushState(); pushErr != nil {
		return fmt.Errorf("push state: %w", pushErr)
	}

	if output == "json" {
		counts := make(map[string]int)
		if result.Summary.ResourceChanges != nil {
			for op, n := range *result.Summary.ResourceChanges {
				counts[string(op)] = n
			}
		}
		return printChangeSummaryJSON(stackName, counts)
	}
	return nil
}

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/output"
)

func newDeployCmd(configPath *string) *cobra.Command {
	var stack, stackConfig, format string
	var yes, allowMultiple bool

	cmd := &cobra.Command{
		Use:           "deploy",
		Short:         "Deploy infrastructure changes for a stack",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDeploy(cmd.Context(), stack, stackConfig, *configPath, format, yes, allowMultiple)
		},
	}

	cmd.Flags().StringVarP(&stack, "stack", "s", "", "stack name / environment (required)")
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to stack config (default: inforge.<stack>.yaml)")
	cmd.Flags().StringVarP(&format, "output", "o", "", "output format: '' (default human) or 'json'")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "auto-approve without prompt")
	cmd.Flags().BoolVar(&allowMultiple, "allow-multiple", false, "allow running when multiple environments have changes")
	if err := cmd.MarkFlagRequired("stack"); err != nil {
		panic(err)
	}
	return cmd
}

func runDeploy(ctx context.Context, stackName, stackConfigPath, configPath, format string, yes, allowMultiple bool) error {
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

	if format == "json" {
		// JSON mode: route Pulumi progress to stderr so stdout carries only the
		// JSON summary (> /tmp/deploy.json in CI).
		result, upErr := s.Up(ctx,
			optup.ProgressStreams(os.Stderr),
			optup.ErrorProgressStreams(os.Stderr),
		)
		if upErr != nil {
			return fmt.Errorf("deploy: %w", upErr)
		}
		if pushErr := pushState(); pushErr != nil {
			return fmt.Errorf("push state: %w", pushErr)
		}
		counts := make(map[string]int)
		if result.Summary.ResourceChanges != nil {
			for op, n := range *result.Summary.ResourceChanges {
				counts[string(op)] = n
			}
		}
		return printChangeSummaryJSON(stackName, counts)
	}

	// Human mode: stream structured per-resource output.
	// The buffered channel decouples the Pulumi engine's blocking event sends
	// from the consumer goroutine so a slow writer never stalls the engine.
	ch := output.NewEventChannel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		output.Stream(ch, os.Stdout)
	}()

	_, _ = fmt.Fprintf(os.Stdout, "Deploying (%s):\n\n", stackName)
	_, upErr := s.Up(ctx,
		optup.EventStreams(ch),
		optup.ErrorProgressStreams(os.Stderr),
	)
	wg.Wait()

	if upErr != nil {
		return fmt.Errorf("deploy: %w", upErr)
	}
	return pushState()
}

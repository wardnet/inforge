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
	var stack, stackConfig string
	var yes bool

	cmd := &cobra.Command{
		Use:           "deploy",
		Short:         "Deploy infrastructure changes for a stack",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDeploy(cmd.Context(), stack, stackConfig, *configPath, yes)
		},
	}

	cmd.Flags().StringVarP(&stack, "stack", "s", "", "stack name / environment (required)")
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to stack config (default: inforge.<stack>.yaml)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "auto-approve without prompt")
	if err := cmd.MarkFlagRequired("stack"); err != nil {
		panic(err)
	}
	return cmd
}

func runDeploy(ctx context.Context, stackName, stackConfigPath, configPath string, yes bool) error {
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

	s, err := upsertStack(ctx, stackName, projCfg)
	if err != nil {
		return fmt.Errorf("initialise stack: %w", err)
	}

	if err := applyStackConfig(ctx, s, stackCfg); err != nil {
		return fmt.Errorf("set stack config: %w", err)
	}

	_, err = s.Up(ctx,
		optup.ProgressStreams(os.Stdout),
		optup.ErrorProgressStreams(os.Stderr),
	)
	if err != nil {
		return fmt.Errorf("deploy: %w", err)
	}
	return nil
}

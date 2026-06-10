package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/output"
)

func newDeployCmd(configPath *string) *cobra.Command {
	var stack, stackConfig, format, report string
	var yes, allowMultiple bool

	cmd := &cobra.Command{
		Use:           "deploy",
		Short:         "Deploy infrastructure changes for a stack",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDeploy(cmd.Context(), stack, stackConfig, *configPath, format, report, yes, allowMultiple)
		},
	}

	cmd.Flags().StringVarP(&stack, "stack", "s", "", "stack name / environment (required)")
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to stack config (default: inforge.<stack>.yaml)")
	cmd.Flags().StringVarP(&format, "output", "o", "", "output format: '' (default human) or 'json'")
	cmd.Flags().StringVar(&report, "report", "", "write a markdown run report to this path (default: a temp file)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "auto-approve without prompt")
	cmd.Flags().BoolVar(&allowMultiple, "allow-multiple", false, "allow running when multiple environments have changes")
	if err := cmd.MarkFlagRequired("stack"); err != nil {
		panic(err)
	}
	return cmd
}

func runDeploy(ctx context.Context, stackName, stackConfigPath, configPath, format, reportPath string, yes, allowMultiple bool) error {
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

	// Pin the inforge-bootstrap download to this CLI build. program.Run reads
	// inforge_version (stack config) / INFORGE_VERSION; the inline program runs
	// in this process, so the env var threads the version through. A "dev" build
	// has no release asset and fails service provisioning with a clear error.
	if err := os.Setenv("INFORGE_VERSION", version); err != nil {
		return fmt.Errorf("set INFORGE_VERSION: %w", err)
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

	// A single Printer renders the engine's event stream — per-resource lines plus
	// an end-of-run summary (counts + failures) — replacing Pulumi's raw progress
	// tree. ProgressStreams is explicitly discarded so the tree never appears
	// regardless of SDK defaults, and ErrorProgressStreams is captured into a
	// buffer (never streamed live) so the two renderers can't duplicate each
	// other's output. In human mode the Printer owns stdout; in JSON mode the
	// machine summary owns stdout, so the human log goes to stderr.
	jsonMode := format == "json"
	humanW := io.Writer(os.Stdout)
	if jsonMode {
		humanW = os.Stderr
	}

	// The buffered channel decouples the Pulumi engine's blocking event sends
	// from the consumer goroutine so a slow writer never stalls the engine.
	p := output.NewPrinter(humanW)
	ch := output.NewEventChannel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range ch {
			p.Handle(ev)
		}
	}()

	if !jsonMode {
		_, _ = fmt.Fprintf(os.Stdout, "Deploying (%s):\n\n", stackName)
	}
	var errBuf bytes.Buffer
	_, upErr := s.Up(ctx,
		optup.EventStreams(ch),
		optup.ProgressStreams(io.Discard),
		optup.ErrorProgressStreams(&errBuf),
	)
	wg.Wait()
	p.Finish()

	// Always produce the run report (file + $GITHUB_STEP_SUMMARY if set), on
	// success or failure, so CI can surface it without any GitHub API call here.
	writeReport("deploy", stackName, p, reportPath)

	if upErr != nil {
		// Still emit the JSON summary on failure so a consumer parsing stdout gets
		// the counts and the failure list (stdout is otherwise empty).
		if jsonMode {
			_ = printChangeSummaryJSON(stackName, p.Changes(), p.Failures())
		}
		// A per-resource failure is already explained in the Printer summary; only
		// dump the raw engine error stream when nothing else accounts for the
		// failure (e.g. a config error or plugin crash before any resource op).
		if len(p.Failures()) == 0 && errBuf.Len() > 0 {
			_, _ = fmt.Fprint(os.Stderr, errBuf.String())
		}
		return fmt.Errorf("deploy: %w", upErr)
	}

	if pushErr := pushState(); pushErr != nil {
		return fmt.Errorf("push state: %w", pushErr)
	}
	if jsonMode {
		return printChangeSummaryJSON(stackName, p.Changes(), p.Failures())
	}
	return nil
}

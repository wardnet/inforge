package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/output"
)

func newPreviewCmd(configPath, dir *string) *cobra.Command {
	var stackConfig, format, report string
	var allowMultiple bool

	cmd := &cobra.Command{
		Use:           "preview <env>",
		Short:         "Preview infrastructure changes for an environment",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPreview(cmd.Context(), args[0], stackConfig, *configPath, *dir, format, report, allowMultiple)
		},
	}

	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to stack config (default: inforge.<env>.yaml)")
	cmd.Flags().StringVarP(&format, "output", "o", "", "output format: '' (default human) or 'json'")
	cmd.Flags().StringVar(&report, "report", "", "write a markdown run report to this path (default: a temp file)")
	cmd.Flags().BoolVar(&allowMultiple, "allow-multiple", false, "allow running when multiple environments have changes")
	return cmd
}

func runPreview(ctx context.Context, stackName, stackConfigPath, configPath, dir, format, reportPath string, allowMultiple bool) error {
	projCfg, err := loadProjectConfig(configPath)
	if err != nil {
		return err
	}

	// Pin the inforge-agent download to this CLI build, same as deploy/ephemeral.
	// program.Run reads inforge_version (stack config) / INFORGE_VERSION; without
	// this, preview resolves "dev" and every service's provision-command script
	// (which embeds the version) diffs against a real pinned deploy — a spurious
	// "update" on every service resource, unrelated to any actual change.
	if err := os.Setenv("INFORGE_VERSION", version); err != nil {
		return fmt.Errorf("set INFORGE_VERSION: %w", err)
	}

	stackCfg, err := resolveStackConfig(stackConfigPath, stackName)
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

	// The CLI-derived keys program.Run reads — `dir` included, so a --dir preview
	// plans the tree it was pointed at rather than ./resources.
	if err := setDerivedStackConfig(ctx, &s, dir, projCfg); err != nil {
		return fmt.Errorf("set derived stack config: %w", err)
	}

	// A single Printer renders the engine's event stream (per-resource lines plus
	// an end-of-run summary), replacing Pulumi's raw progress tree. ProgressStreams
	// is discarded and ErrorProgressStreams is buffered (not streamed live) so the
	// two renderers can't duplicate each other. In human mode the Printer owns
	// stdout; in JSON mode the machine summary owns stdout, so the human log goes
	// to stderr. The buffered channel decouples the engine's blocking event sends
	// from the consumer goroutine so a slow writer never stalls the engine.
	jsonMode := format == "json"
	humanW := io.Writer(os.Stdout)
	if jsonMode {
		humanW = os.Stderr
	}

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
		_, _ = fmt.Fprintf(os.Stdout, "Previewing (%s):\n\n", stackName)
	}
	var errBuf bytes.Buffer
	_, previewErr := s.Preview(ctx,
		optpreview.EventStreams(ch),
		optpreview.ProgressStreams(io.Discard),
		optpreview.ErrorProgressStreams(&errBuf),
	)
	wg.Wait()
	p.Finish()

	writeReport("preview", stackName, p, reportPath)

	if previewErr != nil {
		if jsonMode {
			_ = printChangeSummaryJSON(stackName, p.Changes(), p.Failures())
		}
		if len(p.Failures()) == 0 && errBuf.Len() > 0 {
			_, _ = fmt.Fprint(os.Stderr, errBuf.String())
		}
		return fmt.Errorf("preview: %w", previewErr)
	}
	if jsonMode {
		return printChangeSummaryJSON(stackName, p.Changes(), p.Failures())
	}
	return nil
}

// changeSummaryOutput is the JSON structure emitted by --output json. Failures
// is populated when one or more resource operations failed, so the deploy
// workflow can report what failed — not just the success counts.
type changeSummaryOutput struct {
	Environment string           `json:"environment"`
	Summary     map[string]int   `json:"summary"`
	Failed      int              `json:"failed,omitempty"`
	Failures    []output.Failure `json:"failures,omitempty"`
}

func printChangeSummaryJSON(env string, counts map[string]int, failures []output.Failure) error {
	if counts == nil {
		counts = map[string]int{}
	}
	out := changeSummaryOutput{
		Environment: env,
		Summary:     counts,
		Failed:      len(failures),
		Failures:    failures,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

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

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/output"
)

func newDeployCmd(configPath, dir *string) *cobra.Command {
	var stackConfig, format, report, sshKeyPath string
	var yes, allowMultiple bool

	cmd := &cobra.Command{
		Use:           "deploy <env>",
		Short:         "Deploy infrastructure changes for an environment",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(cmd.Context(), args[0], stackConfig, *configPath, *dir, format, report, sshKeyPath, yes, allowMultiple)
		},
	}

	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to stack config (default: inforge.<env>.yaml)")
	cmd.Flags().StringVarP(&format, "output", "o", "", "output format: '' (default human) or 'json'")
	cmd.Flags().StringVar(&report, "report", "", "write a markdown run report to this path (default: a temp file)")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "path to the SSH deploy key for the mesh baseline trigger (overrides INFORGE_DEPLOY_KEY)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "auto-approve without prompt")
	cmd.Flags().BoolVar(&allowMultiple, "allow-multiple", false, "allow running when multiple environments have changes")
	return cmd
}

func runDeploy(ctx context.Context, stackName, stackConfigPath, configPath, dir, format, reportPath, sshKeyPath string, yes, allowMultiple bool) error {
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

	// Pin the inforge-agent download to this CLI build. program.Run reads
	// inforge_version (stack config) / INFORGE_VERSION; the inline program runs
	// in this process, so the env var threads the version through. A "dev" build
	// has no release asset and fails service provisioning with a clear error.
	if err := os.Setenv("INFORGE_VERSION", version); err != nil {
		return fmt.Errorf("set INFORGE_VERSION: %w", err)
	}

	stackCfg, err := resolveStackConfig(stackConfigPath, stackName)
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

	if err := setProviderDefaults(ctx, s, projCfg.Providers); err != nil {
		return fmt.Errorf("set provider defaults: %w", err)
	}

	if err := setBackups(ctx, s, projCfg.Backups); err != nil {
		return fmt.Errorf("set backups config: %w", err)
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

	header := ""
	if !jsonMode {
		header = fmt.Sprintf("Deploying (%s):\n\n", stackName)
	}
	p, upErr := streamEngineRun(humanW, header, func(ch chan events.EngineEvent, progress, errProgress io.Writer) error {
		_, err := s.Up(ctx,
			optup.EventStreams(ch),
			optup.ProgressStreams(progress),
			optup.ErrorProgressStreams(errProgress),
		)
		return err
	})

	// Always produce the run report (file + $GITHUB_STEP_SUMMARY if set), on
	// success or failure, so CI can surface it without any GitHub API call here.
	writeReport("deploy", stackName, p, reportPath)

	if upErr != nil {
		// Still emit the JSON summary on failure so a consumer parsing stdout gets
		// the counts and the failure list (stdout is otherwise empty).
		if jsonMode {
			_ = printChangeSummaryJSON(stackName, p.Changes(), p.Failures())
		}
		return fmt.Errorf("deploy: %w", upErr)
	}

	if pushErr := pushState(); pushErr != nil {
		return fmt.Errorf("push state: %w", pushErr)
	}

	// The mesh leaf baseline (ADR-0035): mint real mesh material and SSH-push it
	// directly to each mesh host, so the proxies the up just (re)configured
	// leave their placeholders now rather than waiting for the next `inforge
	// pki renew`. The config source honors the stack's source_environment (an
	// ephemeral stack reads its SOURCE env's tree — same split program.Run
	// applies; see rule ephemeral-identity-vs-config-source); its identity is
	// the stack name. Progress goes to humanW (stderr in JSON mode), and the
	// JSON change summary is emitted even when the baseline fails — the up
	// itself succeeded, and a consumer parsing stdout must still get the counts.
	configEnv := stackName
	if v, cfgErr := s.GetConfig(ctx, cfgKeySourceEnvironment); cfgErr == nil && v.Value != "" {
		configEnv = v.Value
	}

	// The mesh baseline SSHes into each host and so needs the deploy key as a FILE,
	// but a CI deploy normally supplies only the key MATERIAL — the very same
	// deploy_private_key / INFORGE_DEPLOY_PRIVATE_KEY the `up` above just used to
	// provision every host over SSH. Materialize that to a private temp file so a
	// deploy configured with a single deploy key succeeds without ALSO exporting
	// INFORGE_DEPLOY_KEY (a path). --ssh-key / INFORGE_DEPLOY_KEY still win.
	meshKey, cleanupKey, keyErr := resolveDeployKeyFile(ctx, s, sshKeyPath)
	if cleanupKey != nil {
		defer cleanupKey()
	}
	var baseErr error
	if keyErr != nil {
		baseErr = fmt.Errorf("mesh baseline: %w", keyErr)
	} else {
		baseErr = meshBaseline(ctx, dir, configEnv, stackName, meshKey, humanW)
	}

	if jsonMode {
		if err := printChangeSummaryJSON(stackName, p.Changes(), p.Failures()); err != nil {
			return err
		}
	}
	return baseErr
}

// resolveDeployKeyFile resolves the SSH deploy key FILE the mesh baseline needs.
// It defers to the shared resolveSSHKey (--ssh-key → INFORGE_DEPLOY_KEY →
// INFORGE_DEPLOY_PRIVATE_KEY material), but — unlike the standalone commands — a
// deploy can also read the stack-config `deploy_private_key`, program.Run's FIRST
// material source. So it checks that between INFORGE_DEPLOY_KEY and the env material,
// preserving program.Run's precedence (stack config beats the env var). The returned
// cleanup is never nil; a caller can always `defer` it.
func resolveDeployKeyFile(ctx context.Context, s auto.Stack, sshKeyPath string) (string, func(), error) {
	// An explicit path (flag or INFORGE_DEPLOY_KEY) wins over any material source.
	if sshKeyPath != "" || os.Getenv("INFORGE_DEPLOY_KEY") != "" {
		return resolveSSHKey(sshKeyPath)
	}
	// Deploy-only: the stack-config material, checked before the shared resolver's
	// INFORGE_DEPLOY_PRIVATE_KEY fallback.
	if v, err := s.GetConfig(ctx, "deploy_private_key"); err == nil && v.Value != "" {
		path, werr := writeTempKeyFile(v.Value)
		if werr != nil {
			return "", func() {}, fmt.Errorf("materialize deploy key: %w", werr)
		}
		return path, func() { _ = os.Remove(path) }, nil
	}
	return resolveSSHKey(sshKeyPath)
}

// writeTempKeyFile writes SSH private-key material to a fresh 0600 temp file (its
// path is returned) so the CLI can hand `ssh -i` a file. A trailing newline is
// ensured — OpenSSH rejects a key file that lacks one.
func writeTempKeyFile(material string) (string, error) {
	if !strings.HasSuffix(material, "\n") {
		material += "\n"
	}
	f, err := os.CreateTemp("", "inforge-deploy-key-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if err := f.Chmod(0o600); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	if _, err := f.WriteString(material); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// streamEngineRun renders a Pulumi engine event stream through the shared Printer
// — per-resource lines plus an end-of-run summary — replacing Pulumi's raw
// progress tree. It is the one place the engine-output plumbing lives, shared by
// `deploy`, `ephemeral up`, and `ephemeral down`/`reap`: it owns the buffered
// event channel (decoupling the engine's blocking sends from the draining
// goroutine so a slow writer never stalls the engine), discards the raw progress
// tree, and buffers ErrorProgressStreams so the two renderers never duplicate
// output. run wires the provided channel + writers into an s.Up/s.Destroy call
// and returns its error. On failure with no per-resource Failure recorded (e.g. a
// config error before any op), the buffered engine error stream is dumped to
// stderr. It returns the Printer (for report/summary access) and run's error.
func streamEngineRun(w io.Writer, header string, run func(ch chan events.EngineEvent, progress, errProgress io.Writer) error) (*output.Printer, error) {
	p := output.NewPrinter(w)
	ch := output.NewEventChannel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range ch {
			p.Handle(ev)
		}
	}()

	if header != "" {
		_, _ = fmt.Fprint(w, header)
	}
	var errBuf bytes.Buffer
	runErr := run(ch, io.Discard, &errBuf)
	wg.Wait()
	p.Finish()

	// The raw engine error stream is the fallback when the Printer recorded no
	// per-resource Failure (e.g. a config error before any op). Write it to w — the
	// same stream the Printer summary it substitutes for uses — so a caller whose
	// human output is stdout (ephemeral up/down) doesn't have this one line split
	// off to stderr. In JSON-mode deploy w is already stderr, so behaviour there is
	// unchanged.
	if runErr != nil && len(p.Failures()) == 0 && errBuf.Len() > 0 {
		_, _ = fmt.Fprint(w, errBuf.String())
	}
	return p, runErr
}

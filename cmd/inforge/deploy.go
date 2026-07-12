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
	var yes bool

	cmd := &cobra.Command{
		Use:           "deploy <env>",
		Short:         "Deploy infrastructure changes for an environment",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(cmd.Context(), args[0], stackConfig, *configPath, *dir, format, report, sshKeyPath, yes)
		},
	}

	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to stack config (default: inforge.<env>.yaml)")
	cmd.Flags().StringVarP(&format, "output", "o", "", "output format: '' (default human) or 'json'")
	cmd.Flags().StringVar(&report, "report", "", "write a markdown run report to this path (default: a temp file)")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "path to the SSH deploy key for the mesh baseline trigger (overrides INFORGE_DEPLOY_KEY)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "auto-approve without prompt")
	return cmd
}

func runDeploy(ctx context.Context, stackName, stackConfigPath, configPath, dir, format, reportPath, sshKeyPath string, yes bool) error {
	if !yes {
		confirmed, err := confirmDeploy(os.Stdin, stackName)
		if err != nil {
			return err
		}
		if !confirmed {
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

	// The CLI-derived keys program.Run reads, `dir` among them: without it the root
	// --dir would move only the CLI-side steps (the mesh baseline below) while the
	// Pulumi program kept deploying ./resources.
	if err := setDerivedStackConfig(ctx, &s, dir, projCfg); err != nil {
		return fmt.Errorf("set derived stack config: %w", err)
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

	// The checkpoint is pushed on failure too — see persistState. It is non-nil
	// whenever upErr is, so it is the single error return for both.
	stateErr := persistState(upErr, pushState)

	if upErr != nil && jsonMode {
		// Still emit the JSON summary on failure so a consumer parsing stdout gets
		// the counts and the failure list (stdout is otherwise empty).
		_ = printChangeSummaryJSON(stackName, p.Changes(), p.Failures())
	}
	if stateErr != nil {
		return stateErr
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

// confirmDeploy gates a deploy that did not pass --yes on an interactive "yes".
// A non-interactive stdin (a CI runner, a pipe, </dev/null) can never answer, and
// reading EOF must NOT read as "cancelled, exit 0" — a CI job that forgot --yes
// would then report a green deploy having applied nothing. It is a hard error.
func confirmDeploy(in *os.File, stackName string) (bool, error) {
	info, err := in.Stat()
	if err != nil {
		return false, fmt.Errorf("stat stdin: %w", err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return false, fmt.Errorf("deploy %q needs confirmation but stdin is not a terminal — pass --yes to approve non-interactively", stackName)
	}

	return promptYes(in, stackName)
}

// promptYes asks for the literal "yes" on an already-known-interactive reader.
// Split from confirmDeploy's stdin check so the answer handling is testable
// without a pty.
func promptYes(in io.Reader, stackName string) (bool, error) {
	fmt.Printf("Deploy stack %q? Type 'yes' to confirm: ", stackName)
	scanner := bufio.NewScanner(in)
	scanner.Scan()
	// A read error is not an answer: treating it as "not yes" would cancel with
	// exit 0, the same false-green this function exists to prevent. A clean EOF
	// (Ctrl-D) leaves Err() nil and correctly cancels.
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	return strings.TrimSpace(scanner.Text()) == "yes", nil
}

// persistState pushes the state checkpoint and folds its error into the up's, if
// any. It runs even when the up FAILED: a partially-failed up still created real
// resources, and the checkpoint recording them lives only in the local state dir
// until pushState commits it. Dropping it on failure orphans those resources — the
// next deploy sees no state for them and creates them again.
func persistState(upErr error, pushState func() error) error {
	pushErr := pushState()
	switch {
	case upErr != nil && pushErr != nil:
		// Both are wrapped (%w twice): a caller matching on either — the up's engine
		// error or the state-push failure — still finds it with errors.Is/As.
		return fmt.Errorf("deploy: %w (push state also failed: %w)", upErr, pushErr)
	case upErr != nil:
		return fmt.Errorf("deploy: %w", upErr)
	case pushErr != nil:
		return fmt.Errorf("push state: %w", pushErr)
	}
	return nil
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

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	iapp "github.com/wardnet/inforge/internal/app"
	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/release"
	"github.com/wardnet/inforge/internal/remote"
	"github.com/wardnet/inforge/internal/service"
)

// release delivery — the workload-agnostic half of `inforge releases deploy`
// (services) and `inforge release app` (front-ends). Both resolve targets from a
// stack descriptor, pull the artifact by SHA from the R2 store, and apply it on
// each host; they differ only in which targets they resolve and the per-host
// apply script (a service extracts + restarts its unit; an app extracts into a
// per-SHA dir, atomically swaps `current`, and reloads nginx). deliverRelease is
// that shared seam — see ADR-0016 / ADR-0026.

// remotePayloadPath is where the orchestrator uploads the artifact tarball on
// every target before running its apply script (shared by service + app).
const remotePayloadPath = "/tmp/inforge-payload.tgz"

// deliveryTarget is one host the orchestrator delivers to, plus the
// workload-specific remote command that applies the uploaded payload.
type deliveryTarget struct {
	host        string // SSH host (public DNS name)
	sshUser     string // connect-as account (the host's deploy_user)
	applyScript string // remote shell run after the payload is uploaded
	describe    string // human label for the apply step (folder+unit / bundle path)
	arch        string // detected CPU arch (services) or "" (apps — architecture-agnostic)
	payloadFile string // local path scp'd to this target; "" skips the upload (app rollback)
}

// deliverRelease is the shared release orchestrator both the service and app
// release paths drive (the delivery-adapter seam): for each resolved target it
// uploads its own payloadFile over scp — skipped when empty, which is how an
// app rollback re-points an already-delivered bundle without re-fetching it, and
// how a mixed-arch service fleet delivers a DIFFERENT payload to different
// targets in one call — runs the target's apply script over ssh, then records
// the delivered SHA (and arch) in the per-env manifest under slug.
func deliverRelease(ctx context.Context, store *release.Store, slug, env, sha string, targets []deliveryTarget, sshKeyPath string) error {
	for _, t := range targets {
		account := fmt.Sprintf("%s@%s", t.sshUser, t.host)
		if t.payloadFile != "" {
			fmt.Printf("uploading to %s...\n", t.host)
			scpArgs := append(append([]string{}, sshArgs(sshKeyPath)...), t.payloadFile, account+":"+remotePayloadPath)
			if out, err := exec.CommandContext(ctx, "scp", scpArgs...).CombinedOutput(); err != nil { // #nosec G204 -- scp binary hardcoded; account/host come from the deploy descriptor's resolved targets and remotePayloadPath is a hardcoded constant
				return fmt.Errorf("upload payload to %s: %w\n%s", t.host, err, out)
			}
		}
		if t.describe != "" {
			fmt.Printf("%s on %s...\n", t.describe, t.host)
		} else {
			fmt.Printf("applying %s on %s...\n", sha, t.host)
		}
		sshRunArgs := append(append([]string{}, sshArgs(sshKeyPath)...), account, t.applyScript)
		if out, err := exec.CommandContext(ctx, "ssh", sshRunArgs...).CombinedOutput(); err != nil { // #nosec G204 -- ssh binary hardcoded; applyScript is built internally by serviceApplyScript/appApplyScript from quoted config values, and account is from the resolved deploy target
			return fmt.Errorf("remote deploy to %s: %w\n%s", t.host, err, out)
		}
		if err := store.SetDeployment(ctx, slug, env, t.host, sha, t.arch, time.Now()); err != nil {
			return fmt.Errorf("record manifest entry for %s: %w", t.host, err)
		}
	}
	return nil
}

// probeHostArch detects host's CPU architecture over SSH (`uname -m`, mapped
// via mapUnameArch to the same amd64/arm64 vocabulary release artifacts are
// pushed under) — modeled directly on sshReadHostPubKey (sshpush.go): the same
// exec.CommandContext(ctx, "ssh", args...).Output() + trim idiom, reusing the
// shared sshArgs helper rather than introducing a second SSH-transport pattern.
func probeHostArch(ctx context.Context, host, sshUser, sshKeyPath string) (string, error) {
	account := fmt.Sprintf("%s@%s", sshUser, host)
	args := append(append([]string{}, sshArgs(sshKeyPath)...), account, "uname -m")
	out, err := exec.CommandContext(ctx, "ssh", args...).Output() // #nosec G204 -- ssh binary hardcoded; account/keyPath are the resolved deploy target / operator-supplied key, the same trust boundary as sshReadHostPubKey
	if err != nil {
		return "", fmt.Errorf("probe arch on %s: %w", host, err)
	}
	return mapUnameArch(strings.TrimSpace(string(out)))
}

// archResolution is the outcome of probing a set of targets: which arch each
// host was detected as, and the sorted set of distinct archs actually needed.
type archResolution struct {
	archOf   map[string]string // HostDNS -> probed arch
	archList []string          // sorted distinct archs across every target
}

// probeAndVerifyArch probes every target's host architecture (once per host,
// via probeHostArch) and verifies EVERY distinct arch actually needed has
// already been pushed for (svc, sha) — collecting ALL missing archs into one
// error (naming the host(s), the detected arch, and the exact R2 key, with a
// hint to push it) before returning, so a fleet missing two archs is fully
// diagnosable in one run. This is safe to call for a dry-run: it never
// downloads anything.
func probeAndVerifyArch(ctx context.Context, store *release.Store, svc, sha string, targets []service.DeployTarget, sshKeyPath string) (archResolution, error) {
	archOf := map[string]string{}
	hostsByArch := map[string][]string{} // arch -> HostDNS list, for the error message
	for _, t := range targets {
		arch, err := probeHostArch(ctx, t.HostDNS, t.SSHUser, sshKeyPath)
		if err != nil {
			return archResolution{}, fmt.Errorf("detect arch for %s: %w", t.HostDNS, err)
		}
		archOf[t.HostDNS] = arch
		hostsByArch[arch] = append(hostsByArch[arch], t.HostDNS)
	}

	archList := make([]string, 0, len(hostsByArch))
	for arch := range hostsByArch {
		archList = append(archList, arch)
	}
	sort.Strings(archList)

	var missing []string
	for _, arch := range archList {
		exists, err := store.ArtifactExists(ctx, svc, sha, arch)
		if err != nil {
			return archResolution{}, err
		}
		if !exists {
			missing = append(missing, fmt.Sprintf(
				"host(s) %s detected as %s — looked for %s (run `inforge releases push --arch %s` first)",
				strings.Join(hostsByArch[arch], ", "), arch, release.ArtifactKey(svc, sha, arch), arch))
		}
	}
	if len(missing) > 0 {
		return archResolution{}, fmt.Errorf("artifact %s/%s missing for %d arch(es):\n  %s", svc, sha, len(missing), strings.Join(missing, "\n  "))
	}
	return archResolution{archOf: archOf, archList: archList}, nil
}

// downloadArchPayloads downloads each distinct needed arch's artifact exactly
// once (never once per host) and builds the ready-to-deliver targets from a
// prior probeAndVerifyArch result. The returned cleanup func releases every
// downloaded temp file; call it exactly once, even on error (it is safe to
// call with partial downloads).
func downloadArchPayloads(ctx context.Context, store *release.Store, svc, sha string, targets []service.DeployTarget, res archResolution) ([]deliveryTarget, func(), error) {
	payloadOf := map[string]string{}
	var cleanups []func()
	cleanup := func() {
		for _, c := range cleanups {
			c()
		}
	}
	for _, arch := range res.archList {
		payload, archCleanup, err := downloadArtifact(ctx, store, svc, sha, arch)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		cleanups = append(cleanups, archCleanup)
		payloadOf[arch] = payload
	}
	return serviceDeliveryTargets(targets, res.archOf, payloadOf), cleanup, nil
}

// serviceApplyScript is adapter #1: the remote command that applies a service
// payload — extract it into the service folder and restart the inforge-managed
// unit. It reproduces the historical `sshDeliver` body verbatim so the refactor
// behind the seam is behaviour-preserving.
func serviceApplyScript(t service.DeployTarget) string {
	return strings.Join([]string{
		fmt.Sprintf("sudo tar -xzf %s -C %s", remotePayloadPath, remote.Quote(t.Folder)),
		fmt.Sprintf("rm -f %s", remotePayloadPath),
		fmt.Sprintf("sudo systemctl restart %s", remote.Quote(t.Unit)),
	}, " && ")
}

// serviceDeliveryTargets adapts resolved service deploy targets to the shared
// orchestrator's targets. SSHUser is always set by BuildDeployDescriptor (it
// defaults to "deploy"), so it is used directly. archOf maps HostDNS -> the
// arch probeHostArch detected for that host; payloadOf maps arch -> the ONE
// downloaded payload file for that arch (populated once per distinct arch
// actually needed, not once per host) — so a mixed-arch fleet delivers the
// correct binary to each target in a single deliverRelease call.
func serviceDeliveryTargets(targets []service.DeployTarget, archOf, payloadOf map[string]string) []deliveryTarget {
	out := make([]deliveryTarget, 0, len(targets))
	for _, t := range targets {
		arch := archOf[t.HostDNS]
		out = append(out, deliveryTarget{
			host:        t.HostDNS,
			sshUser:     t.SSHUser,
			applyScript: serviceApplyScript(t),
			describe:    fmt.Sprintf("extracting to %s and restarting %s", t.Folder, t.Unit),
			arch:        arch,
			payloadFile: payloadOf[arch],
		})
	}
	return out
}

// appReleaseScript is adapter #2 (fresh release): extract the uploaded bundle
// into its per-SHA directory, atomically swap `current` to it, validate + reload
// nginx, then GC old bundles. Extraction is idempotent — re-releasing a SHA
// re-populates the same directory.
func appReleaseScript(t iapp.DeployTarget, sha string) string {
	bundleDir := iapp.BundleDir(t.App, sha)
	return strings.Join([]string{
		fmt.Sprintf("sudo mkdir -p %s", remote.Quote(bundleDir)),
		fmt.Sprintf("sudo tar -xzf %s -C %s", remotePayloadPath, remote.Quote(bundleDir)),
		fmt.Sprintf("rm -f %s", remotePayloadPath),
		// Validate BEFORE swapping `current`: if `nginx -t` fails, the swap never
		// runs (&&), so the served doc root and `current` can't diverge.
		"sudo nginx -t",
		iapp.SwapCurrentScript(t.App, sha),
		"sudo systemctl reload nginx",
		iapp.GCReleasesScript(t.App),
	}, " && ")
}

// appRollbackScript repoints `current` at a bundle already present on the host
// (no payload upload, no store fetch): it asserts the <sha> directory exists,
// swaps `current`, and reloads nginx. It fails loudly when the SHA was never
// delivered to this host rather than leaving `current` dangling.
func appRollbackScript(t iapp.DeployTarget, sha string) string {
	bundleDir := iapp.BundleDir(t.App, sha)
	return strings.Join([]string{
		fmt.Sprintf("{ test -d %s || { echo \"bundle %s not present on %s — release it before rolling back\" >&2; exit 1; }; }",
			remote.Quote(bundleDir), sha, t.App),
		// Validate before the swap (see appReleaseScript).
		"sudo nginx -t",
		iapp.SwapCurrentScript(t.App, sha),
		"sudo systemctl reload nginx",
	}, " && ")
}

// appDeliveryTargets adapts resolved app deploy targets to the orchestrator's
// targets, choosing the fresh-release or rollback apply script. SSHUser is always
// set by BuildDeployDescriptor (it defaults to "deploy"), so it is used directly.
// Apps are architecture-agnostic (arch always ""); payload is the one
// downloaded bundle to scp to every target — "" for rollback, which never
// uploads (the bundle is already on the host).
func appDeliveryTargets(targets []iapp.DeployTarget, sha, payload string, rollback bool) []deliveryTarget {
	out := make([]deliveryTarget, 0, len(targets))
	for _, t := range targets {
		script, verb, pf := appReleaseScript(t, sha), "releasing", payload
		if rollback {
			script, verb, pf = appRollbackScript(t, sha), "rolling back", ""
		}
		out = append(out, deliveryTarget{
			host:        t.IngressHostDNS,
			sshUser:     t.SSHUser,
			applyScript: script,
			describe:    fmt.Sprintf("%s %s", verb, iapp.BundleDir(t.App, sha)),
			payloadFile: pf,
		})
	}
	return out
}

// appArtifactSlug is the R2 key namespace for an app's bundles. Apps are
// namespaced under `app/` so an app and a service of the same name never collide
// in the store — mirroring the on-host `/srv/wardnet/app/<name>` segmentation.
func appArtifactSlug(name string) string { return "app/" + name }

// --- inforge release app ------------------------------------------------------

func newReleaseCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Deliver a front-end (app) release to its ingress host",
	}
	cmd.AddCommand(newReleaseAppCmd(configPath))
	return cmd
}

func newReleaseAppCmd(configPath *string) *cobra.Command {
	var sha, bundleDir, stackConfig, sshKeyPath string
	var rollback, dryRun bool
	cmd := &cobra.Command{
		Use:   "app <env> <name>",
		Short: "Release (or roll back) a static app bundle to its ingress host",
		Long: "Deliver an app's static bundle to its ingress host and atomically swap\n" +
			"the served document root. With --bundle the local SPA build is packaged and\n" +
			"pushed to the release store first; otherwise the bundle for --sha must already\n" +
			"be in the store. --rollback re-points `current` at a SHA already on the host\n" +
			"without re-fetching it.",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReleaseApp(cmd.Context(), *configPath, args[0], args[1], sha, bundleDir, stackConfig, sshKeyPath, rollback, dryRun)
		},
	}
	cmd.Flags().StringVar(&sha, "sha", "", "bundle SHA to release (default: $GITHUB_SHA)")
	cmd.Flags().StringVar(&bundleDir, "bundle", "", "local SPA build directory to package + push before releasing")
	cmd.Flags().BoolVar(&rollback, "rollback", false, "re-point `current` at a SHA already on the host (no store fetch; incompatible with --bundle)")
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to the infra stack config file (default: inforge.<env>.yaml)")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "path to the SSH deploy key (overrides INFORGE_DEPLOY_KEY)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "resolve targets and verify the artifact without delivering")
	return cmd
}

func runReleaseApp(ctx context.Context, configPath, env, name, sha, bundleDir, stackConfigPath, sshKeyPath string, rollback, dryRun bool) error {
	if rollback && bundleDir != "" {
		return fmt.Errorf("--rollback re-points an existing on-host bundle and cannot push --bundle")
	}
	sha, err := resolveSHA(sha)
	if err != nil {
		return err
	}

	projCfg, err := loadProjectConfig(configPath)
	if err != nil {
		return err
	}
	stackCfg, err := resolveStackConfig(stackConfigPath, env)
	if err != nil {
		return err
	}

	targets, err := resolveAppDeployTargets(ctx, projCfg, env, stackCfg, name)
	if err != nil {
		return err
	}

	store, err := newArtifactStore(ctx, projCfg)
	if err != nil {
		return err
	}
	slug := appArtifactSlug(name)

	// Rollback never touches the store: the bundle is already on the host, so we
	// only re-point `current` and reload. (We still resolve + verify the SSH key
	// and record the manifest, so `releases list`-style history stays accurate.)
	if rollback {
		fmt.Printf("rolling back %s @ %s → %d host(s) (env: %s)\n", name, sha, len(targets), env)
		for _, t := range targets {
			fmt.Printf("  host: %s  fqdn: %s  path: %s\n", t.IngressHostDNS, t.FQDN, iapp.BundleDir(name, sha))
		}
		if dryRun {
			fmt.Println("(dry-run: skipping rollback)")
			return nil
		}
		var cleanupKey func()
		sshKeyPath, cleanupKey, err = resolveSSHKey(sshKeyPath)
		if err != nil {
			return err
		}
		defer cleanupKey()
		return deliverRelease(ctx, store, slug, env, sha, appDeliveryTargets(targets, sha, "", true), sshKeyPath)
	}

	fmt.Printf("releasing %s @ %s → %d host(s) (env: %s)\n", name, sha, len(targets), env)
	for _, t := range targets {
		fmt.Printf("  host: %s  fqdn: %s  path: %s\n", t.IngressHostDNS, t.FQDN, iapp.BundleDir(name, sha))
	}

	// A dry-run must not mutate the store, so it neither packages+pushes --bundle
	// nor prunes — it only reports what would happen.
	if dryRun {
		if bundleDir != "" {
			fmt.Printf("(dry-run: would package + push %s then deliver, skipping)\n", bundleDir)
			return nil
		}
		exists, err := store.ArtifactExists(ctx, slug, sha, release.NoArch)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("bundle %s/%s.tar.gz not found in release store — pass --bundle <dir> to package and push it", slug, sha)
		}
		fmt.Println("(dry-run: artifact present, skipping delivery)")
		return nil
	}

	// Optional push: package the local build and upload it under the app slug, so
	// `release app --bundle ./dist` is a single build→deliver step.
	if bundleDir != "" {
		if err := pushAppBundle(ctx, store, slug, sha, bundleDir, projCfg.Artifacts.Keep); err != nil {
			return err
		}
	}

	exists, err := store.ArtifactExists(ctx, slug, sha, release.NoArch)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("bundle %s/%s.tar.gz not found in release store — pass --bundle <dir> to package and push it", slug, sha)
	}

	var cleanupKey func()
	sshKeyPath, cleanupKey, err = resolveSSHKey(sshKeyPath)
	if err != nil {
		return err
	}
	defer cleanupKey()
	payload, cleanup, err := downloadArtifact(ctx, store, slug, sha, release.NoArch)
	if err != nil {
		return err
	}
	defer cleanup()

	return deliverRelease(ctx, store, slug, env, sha, appDeliveryTargets(targets, sha, payload, false), sshKeyPath)
}

// pushAppBundle packages a local SPA build directory and uploads it to the store
// under the app slug, then prunes — the app analogue of `inforge releases push`,
// but taking an explicit local directory (an app has no service deploy descriptor
// to resolve an artifact path from).
func pushAppBundle(ctx context.Context, store *release.Store, slug, sha, bundleDir string, keep int) error {
	info, err := os.Stat(bundleDir)
	if err != nil {
		return fmt.Errorf("bundle dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("bundle dir %q is not a directory", bundleDir)
	}

	fmt.Printf("packaging bundle from %s...\n", bundleDir)
	payload, cleanup, err := packageDir(ctx, bundleDir)
	if err != nil {
		return err
	}
	defer cleanup()

	f, err := os.Open(payload) // #nosec G304 -- payload is an internally generated os.CreateTemp() name
	if err != nil {
		return fmt.Errorf("open payload: %w", err)
	}
	defer func() { _ = f.Close() }()

	fmt.Printf("uploading %s/%s.tar.gz...\n", slug, sha)
	if err := store.PutArtifact(ctx, slug, sha, release.NoArch, f); err != nil {
		return err
	}
	deleted, err := store.Prune(ctx, slug, keep)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: prune incomplete: %v\n", err)
	}
	if len(deleted) > 0 {
		fmt.Printf("pruned %d old bundle(s): %s\n", len(deleted), strings.Join(deleted, ", "))
	}
	return nil
}

// resolveDescriptorTargets connects to the infra Pulumi stack for env, reads the
// named deploy-descriptor stack output, and returns its targets matching keep. It
// is the shared resolver behind both the service (deployDescriptor) and app
// (appDeployDescriptor) release paths — they differ only in the output key,
// target type, and match predicate, not in the connect → read → decode plumbing.
func resolveDescriptorTargets[T any](ctx context.Context, projCfg projectConfig, env, outputKey string, stackCfg stackConfig, keep func(T) bool) ([]T, error) {
	s, _, err := upsertStack(ctx, env, projCfg)
	if err != nil {
		return nil, fmt.Errorf("connect to stack %q: %w", env, err)
	}
	if err := applyStackConfig(ctx, s, stackCfg); err != nil {
		return nil, fmt.Errorf("apply stack config: %w", err)
	}
	outputs, err := s.Outputs(ctx)
	if err != nil {
		return nil, fmt.Errorf("read stack outputs: %w", err)
	}
	if _, ok := outputs[outputKey]; !ok {
		return nil, fmt.Errorf("stack %q has no %s output — has inforge deploy been run for this environment?", env, outputKey)
	}
	// decodeTargets owns the pulumi.Any-as-map JSON round-trip (shared with the
	// ephemeral replicate path); here we additionally filter to the caller's keep
	// predicate after the (already error-checked) presence of the output.
	all, err := decodeTargets[T](outputs, outputKey)
	if err != nil {
		return nil, err
	}
	var targets []T
	for _, t := range all {
		if keep(t) {
			targets = append(targets, t)
		}
	}
	return targets, nil
}

// resolveAppDeployTargets returns every app.DeployTarget for name from the
// appDeployDescriptor output (one per region a multi-region app fans out to).
func resolveAppDeployTargets(ctx context.Context, projCfg projectConfig, env string, stackCfg stackConfig, name string) ([]iapp.DeployTarget, error) {
	targets, err := resolveDescriptorTargets(ctx, projCfg, env, "appDeployDescriptor", stackCfg,
		func(t iapp.DeployTarget) bool { return t.App == name })
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("app %q not found in appDeployDescriptor for env %q — is it defined as an AppSpec in the infra resources?", name, env)
	}
	// See resolveDeployTargets's identical check: a mixed global+regional match
	// signals a same-named app declared in both scopes, which `inforge validate`
	// should already reject.
	if hasMixedScope(targets, func(t iapp.DeployTarget) string { return t.Scope }, pki.ScopeGlobal) {
		return nil, fmt.Errorf("app %q matches both a global and a regional target in appDeployDescriptor for env %q — this is an app-name collision across scopes; rename one of the two AppSpecs (run `inforge validate` to locate them)", name, env)
	}
	return targets, nil
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/spf13/cobra"
	iapp "github.com/wardnet/inforge/internal/app"
	"github.com/wardnet/inforge/internal/loader"
	"github.com/wardnet/inforge/internal/release"
	"github.com/wardnet/inforge/internal/service"
	"github.com/wardnet/inforge/internal/types"
)

func newEphemeralUpCmd(configPath, dir *string) *cobra.Command {
	var from, slug, ttl, stackConfig, sshKeyPath string
	cmd := &cobra.Command{
		Use:   "up --from <src> [--slug <s>] [--ttl <dur>]",
		Short: "Provision a fresh ephemeral clone of a source env and replicate its live releases",
		Long: "Provision a fresh, network-segregated clone of --from's resource definition\n" +
			"under a generated slug identity, then deliver the exact service/app SHAs\n" +
			"currently live in the source env. The env self-expires after --ttl (default\n" +
			"2h) and is torn down by `inforge ephemeral reap`. Requires an r2/s3 backend.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runEphemeralUp(cmd.Context(), *configPath, *dir, from, slug, ttl, stackConfig, sshKeyPath)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "source environment to clone (required)")
	cmd.Flags().StringVar(&slug, "slug", "", "ephemeral env identity slug (default: auto-generated eph-XXXX)")
	cmd.Flags().StringVar(&ttl, "ttl", "", "time-to-live as a Go duration, e.g. 90m or 2h (default 2h, capped by ephemeral.maxTtl)")
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to the source stack config (default: inforge.<src>.yaml)")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "path to the SSH deploy key for release replication (overrides INFORGE_DEPLOY_KEY)")
	mustRequire(cmd, "from")
	return cmd
}

func runEphemeralUp(ctx context.Context, configPath, dir, from, slugFlag, ttlFlag, stackConfigPath, sshKeyPath string) error {
	projCfg, err := loadProjectConfig(configPath)
	if err != nil {
		return err
	}
	if err := requireObjectBackend(projCfg); err != nil {
		return err
	}
	if err := checkSourceDefined(dir, from); err != nil {
		return err
	}

	slug := slugFlag
	if slug == "" {
		slug, err = generateSlug()
		if err != nil {
			return err
		}
	}
	if err := validateSlug(slug); err != nil {
		return err
	}
	if slug == from {
		return fmt.Errorf("slug %q must differ from the source env %q — the ephemeral identity is distinct from its config source", slug, from)
	}

	maxTTL, err := projCfg.Ephemeral.maxTTL()
	if err != nil {
		return err
	}
	ttl, err := resolveTTL(ttlFlag, maxTTL)
	if err != nil {
		return err
	}
	expiresAt := expiresAtEpoch(time.Now(), ttl)

	// Pin the agent download to this CLI build, exactly as `inforge deploy`.
	if err := os.Setenv("INFORGE_VERSION", version); err != nil {
		return fmt.Errorf("set INFORGE_VERSION: %w", err)
	}

	// The source stack config flows the deploy_private_key (and any other source
	// stack config) into the ephemeral stack; the ephemeral identity keys are
	// layered on top below.
	stackCfg, err := resolveStackConfig(stackConfigPath, from)
	if err != nil {
		return err
	}

	// Create-only (not upsert): `up` must never adopt an existing stack — a slug
	// colliding with a permanent env's stack name would stamp ephemeral +
	// expires_at onto that real env and make it reaper-eligible. Creation is the
	// atomic guard, so concurrent same-slug ups can't both win (the three-signal
	// safety assumes ephemeral config is written only to fresh ephemeral stacks).
	s, err := createStack(ctx, slug, projCfg)
	if err != nil {
		return err
	}
	if err := applyStackConfig(ctx, s, stackCfg); err != nil {
		return fmt.Errorf("apply source stack config: %w", err)
	}

	// Persist the identity↔source mapping BEFORE the Pulumi run, so a crashed `up`
	// still leaves a reap-able stack (ephemeral + expires_at are the reaper's only
	// signals). This stack config is the single carrier of the source mapping that
	// `down`/`reap` read back.
	idCfg := map[string]auto.ConfigValue{
		cfgKeyEnvironment:       {Value: slug},
		cfgKeySourceEnvironment: {Value: from},
		cfgKeyEphemeral:         {Value: "true"},
		cfgKeyExpiresAt:         {Value: expiresAt},
	}
	if err := s.SetAllConfig(ctx, idCfg); err != nil {
		return fmt.Errorf("persist ephemeral identity config: %w", err)
	}

	// The CLI-derived keys, written AFTER the source stack config: `dir` is always
	// re-asserted, so a source stack config's own `dir` key can never override the
	// tree checkSourceDefined just validated — program.Run must read exactly the tree
	// `up` verified, not a source-provided one.
	if err := setDerivedStackConfig(ctx, &s, dir, projCfg); err != nil {
		return fmt.Errorf("set derived stack config: %w", err)
	}

	fmt.Printf("spinning up ephemeral env %q from source %q (ttl %s, expires_at %s)\n", slug, from, ttl, expiresAt)
	if err := runStackUp(ctx, s, slug); err != nil {
		return err
	}

	// The mesh leaf baseline (ADR-0035) under the ephemeral identity: mint the
	// clone's mesh material (source config, slug identity) and SSH-push it
	// directly to each ephemeral mesh host, so the replicated services below
	// start against proxies already holding real leaves.
	if err := meshBaseline(ctx, dir, from, slug, sshKeyPath, os.Stdout); err != nil {
		return err
	}

	// Replicate the source's live releases onto the freshly provisioned clone.
	if err := replicateReleases(ctx, s, projCfg, dir, from, slug, sshKeyPath); err != nil {
		return err
	}

	fmt.Printf("\nephemeral env %q is up (source %q, expires_at %s).\n", slug, from, expiresAt)
	fmt.Printf("tear down early with: inforge ephemeral down %s\n", slug)
	return nil
}

// runStackUp runs a Pulumi up on s, rendering the engine event stream through the
// shared Printer (streamEngineRun) exactly as `inforge deploy` does. It is the
// provision half of `up`.
func runStackUp(ctx context.Context, s auto.Stack, label string) error {
	_, upErr := streamEngineRun(os.Stdout, fmt.Sprintf("Provisioning (%s):\n\n", label),
		func(ch chan events.EngineEvent, progress, errProgress io.Writer) error {
			_, err := s.Up(ctx,
				optup.EventStreams(ch),
				optup.ProgressStreams(progress),
				optup.ErrorProgressStreams(errProgress),
			)
			return err
		})
	if upErr != nil {
		return fmt.Errorf("provision ephemeral env %q: %w", label, upErr)
	}
	return nil
}

// replicateReleases delivers, onto the freshly provisioned ephemeral env, the
// exact artifact SHAs currently live in the source env (decision #6, ADR-0028):
// for every service and app defined in the source config it reads the source's
// per-host manifest, resolves each ephemeral host's source counterpart, and
// delivers that host's SHA via the same deliverRelease transport the normal
// release path uses — writing the ephemeral env's own manifest.<slug>.yaml.
//
// It is faithful per-host (each ephemeral host gets the SHA its source
// counterpart runs) and skip-and-reports a workload not yet deployed in the
// source (no manifest entry) rather than failing the `up` — an undeployed app
// keeps its placeholder seed. Genuine delivery failures (a missing artifact, an
// SSH error) are collected and returned so CI sees a non-zero exit, while the
// env stays up (and TTL-reapable).
func replicateReleases(ctx context.Context, s auto.Stack, projCfg projectConfig, dir, srcEnv, slug, sshKeyPath string) error {
	sw, err := loadSourceWorkloads(dir, srcEnv)
	if err != nil {
		return err
	}
	services := sw.services()
	if len(services) == 0 && len(sw.apps) == 0 {
		fmt.Println("\nno services or apps defined in source — nothing to replicate")
		return nil
	}

	store, err := newArtifactStore(ctx, projCfg)
	if err != nil {
		return fmt.Errorf("replicate releases: %w", err)
	}

	outputs, err := s.Outputs(ctx)
	if err != nil {
		return fmt.Errorf("read ephemeral stack outputs: %w", err)
	}
	svcTargets, err := decodeTargets[service.DeployTarget](outputs, "deployDescriptor")
	if err != nil {
		return err
	}
	appTargets, err := decodeTargets[iapp.DeployTarget](outputs, "appDeployDescriptor")
	if err != nil {
		return err
	}
	svcByName := groupBy(svcTargets, func(t service.DeployTarget) string { return t.Service })
	appByName := groupBy(appTargets, func(t iapp.DeployTarget) string { return t.App })

	sshKeyPath, cleanupKey, err := resolveSSHKey(sshKeyPath)
	if err != nil {
		return err
	}
	defer cleanupKey()

	fmt.Printf("\nreplicating source releases (%d service(s), %d app(s)) onto %q:\n", len(services), len(sw.apps), slug)
	var failures []string

	for _, svc := range services {
		if err := replicateService(ctx, store, dir, srcEnv, slug, sshKeyPath, svc, svcByName[svc.Name], sw); err != nil {
			failures = append(failures, fmt.Sprintf("service %s: %v", svc.Name, err))
		}
	}
	for _, a := range sw.apps {
		if err := replicateApp(ctx, store, srcEnv, slug, sshKeyPath, a, appByName[a.Name]); err != nil {
			failures = append(failures, fmt.Sprintf("app %s: %v", a.Name, err))
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("replicate releases: %d workload(s) failed:\n  - %s", len(failures), strings.Join(failures, "\n  - "))
	}
	return nil
}

// replicateService delivers a single source service's per-host SHAs onto its
// ephemeral counterparts. A service with no source manifest entry, or no
// ephemeral target, is skipped (reported, not failed). Mesh leaves are minted
// under the ephemeral identity before delivery so the unit restarts into a
// provider that already holds its leaf.
func replicateService(ctx context.Context, store *release.Store, dir, srcEnv, slug, sshKeyPath string, svc types.ServiceSpec, targets []service.DeployTarget, sw sourceWorkloads) error {
	if len(targets) == 0 {
		fmt.Printf("  service %s: no ephemeral target (not in deploy descriptor) — skipped\n", svc.Name)
		return nil
	}
	srcManifest, _, exists, err := store.LoadManifest(ctx, svc.Name, srcEnv)
	if err != nil {
		return err
	}
	if !exists || len(srcManifest.Deployments) == 0 {
		fmt.Printf("  service %s: not deployed in source — provisioned only, skipped\n", svc.Name)
		return nil
	}

	bySHA, missing := groupTargetsBySHA(targets, srcManifest,
		func(t service.DeployTarget) string { return t.HostDNS }, slug, srcEnv)
	for _, host := range missing {
		fmt.Printf("  service %s: host %s has no source SHA — skipped\n", svc.Name, host)
	}
	if len(bySHA) == 0 {
		return nil
	}

	// A mesh service's leaf is minted under the ephemeral identity (slug) from the
	// SOURCE's intermediate, written to the slug-scoped workspace the deploy just
	// provisioned — so the restart below lands a fresh leaf rather than crash-loop.
	if err := mintReplicatedServiceLeaf(ctx, dir, srcEnv, slug, svc.Name, sshKeyPath, sw); err != nil {
		return err
	}

	for _, sha := range slices.Sorted(maps.Keys(bySHA)) {
		group := bySHA[sha]
		archRes, err := probeAndVerifyArch(ctx, store, svc.Name, sha, group, sshKeyPath)
		if err != nil {
			return err
		}
		deliveryTargets, cleanup, err := downloadArchPayloads(ctx, store, svc.Name, sha, group, archRes)
		if err != nil {
			return err
		}
		err = deliverRelease(ctx, store, svc.Name, slug, sha, deliveryTargets, sshKeyPath)
		cleanup()
		if err != nil {
			return err
		}
		fmt.Printf("  service %s: delivered %s to %d host(s)\n", svc.Name, sha, len(group))
	}
	return nil
}

// replicateApp delivers a single source app's per-host bundle SHAs onto its
// ephemeral counterparts, mirroring replicateService. An app not yet released in
// the source keeps the placeholder seed the deploy provisioned.
func replicateApp(ctx context.Context, store *release.Store, srcEnv, slug, sshKeyPath string, a types.AppSpec, targets []iapp.DeployTarget) error {
	if len(targets) == 0 {
		fmt.Printf("  app %s: no ephemeral target (not in deploy descriptor) — skipped\n", a.Name)
		return nil
	}
	artifactSlug := appArtifactSlug(a.Name)
	srcManifest, _, exists, err := store.LoadManifest(ctx, artifactSlug, srcEnv)
	if err != nil {
		return err
	}
	if !exists || len(srcManifest.Deployments) == 0 {
		fmt.Printf("  app %s: not released in source — placeholder only, skipped\n", a.Name)
		return nil
	}

	bySHA, missing := groupTargetsBySHA(targets, srcManifest,
		func(t iapp.DeployTarget) string { return t.IngressHostDNS }, slug, srcEnv)
	for _, host := range missing {
		fmt.Printf("  app %s: host %s has no source SHA — skipped\n", a.Name, host)
	}

	for _, sha := range slices.Sorted(maps.Keys(bySHA)) {
		group := bySHA[sha]
		payload, cleanup, err := downloadArtifact(ctx, store, artifactSlug, sha, release.NoArch)
		if err != nil {
			return err
		}
		err = deliverRelease(ctx, store, artifactSlug, slug, sha, appDeliveryTargets(group, sha, payload, false), sshKeyPath)
		cleanup()
		if err != nil {
			return err
		}
		fmt.Printf("  app %s: delivered %s to %d host(s)\n", a.Name, sha, len(group))
	}
	return nil
}

// mintReplicatedServiceLeaf mints a fresh leaf for a replicated mtls_files:
// service under the EPHEMERAL identity (slug) from the SOURCE env's
// intermediate, writing it to the slug-scoped /<svc>/mtls the ephemeral deploy
// provisioned — the ephemeral analogue of mintReleasedServiceLeaf. configEnv
// (the source) selects the pki.enc.yaml/variables/regions; identityEnv (the
// slug) stamps the SPIFFE ID and the on-host leaf.age this SSH-pushes to. Any service without
// mtls_files is a no-op — its mesh copy is written by the ephemeral post-up
// baseline, not per replicated service (ADR-0033). The source resource set is
// taken from the already-loaded sourceWorkloads (no re-read).
func mintReplicatedServiceLeaf(ctx context.Context, dir, srcEnv, slug, svc, sshKeyPath string, sw sourceWorkloads) error {
	// renewMeshCertsAs no-ops (before touching the store or INFORGE_SECRETS_KEY)
	// when the named service has no service-side mtls files to mint.
	global := types.Resources{Service: sw.globalSvcs}
	regional := types.Resources{Service: sw.regionalSvcs}
	count, err := renewMeshCertsAs(ctx, dir, srcEnv, slug, global, regional, svc, sshKeyPath, os.Stdout)
	if err != nil {
		return fmt.Errorf("mint mesh leaf for %s under %s: %w", svc, slug, err)
	}
	if count > 0 {
		fmt.Printf("  service %s: minted %d mesh leaf certificate(s) under %s\n", svc, count, slug)
	}
	return nil
}

// sourceWorkloads is the source env's resource set loaded ONCE for replication:
// the deduplicated service/app union `up` iterates, plus the raw per-scope service
// slices the scope-aware mesh-leaf mint needs (so it never re-reads the tree).
type sourceWorkloads struct {
	apps         []types.AppSpec     // dedup union, for iteration
	globalSvcs   []types.ServiceSpec // raw global scope (also feeds mesh mint scope)
	regionalSvcs []types.ServiceSpec // raw regional scope (also feeds mesh mint scope)
}

// services returns the deduplicated service union to iterate for replication —
// derived from the per-scope slices rather than stored, so the two can never
// drift. A name in both scopes (it should not, per validation) collapses to its
// first occurrence (regional before global).
func (sw sourceWorkloads) services() []types.ServiceSpec {
	return dedupByName(append(append([]types.ServiceSpec{}, sw.regionalSvcs...), sw.globalSvcs...),
		func(s types.ServiceSpec) string { return s.Name })
}

// loadSourceWorkloads loads the source env's config once: the raw per-scope
// service slices (for replication and the scope-aware mesh mint) and the
// deduplicated app union to iterate.
func loadSourceWorkloads(dir, srcEnv string) (sourceWorkloads, error) {
	regional, err := loader.LoadResources(srcEnv, dir)
	if err != nil {
		return sourceWorkloads{}, err
	}
	global, err := loader.LoadGlobalResources(srcEnv, dir)
	if err != nil {
		return sourceWorkloads{}, err
	}
	return sourceWorkloads{
		apps: dedupByName(append(append([]types.AppSpec{}, regional.App...), global.App...),
			func(a types.AppSpec) string { return a.Name }),
		globalSvcs:   global.Service,
		regionalSvcs: regional.Service,
	}, nil
}

// groupTargetsBySHA maps each delivery target to the SHA its SOURCE counterpart
// runs, by rewriting the target's host DNS into the source's (env-label swap) and
// looking it up in the source manifest. Targets whose source host has no manifest
// entry are returned in `missing` (their host DNS) to skip-and-report. The result
// groups targets by SHA so each distinct SHA is delivered once.
func groupTargetsBySHA[T any](targets []T, srcManifest release.Manifest, hostDNS func(T) string, identityEnv, srcEnv string) (bySHA map[string][]T, missing []string) {
	bySHA = map[string][]T{}
	for _, t := range targets {
		srcKey := sourceHostDNS(hostDNS(t), identityEnv, srcEnv)
		d, ok := srcManifest.Deployments[srcKey]
		if !ok || d.SHA == "" {
			missing = append(missing, hostDNS(t))
			continue
		}
		bySHA[d.SHA] = append(bySHA[d.SHA], t)
	}
	return bySHA, missing
}

// sourceHostDNS rewrites an ephemeral host DNS name into its source counterpart
// by swapping the env label of HostFQDN ("<compute>.vm.<env>[.<slug>].<base>",
// env at index 2) from identityEnv back to srcEnv. Compute, region slug, and base
// domain — which the clone shares with its source — are preserved, so the source
// manifest (keyed by the source's host DNS) can be looked up by the ephemeral
// target's host. Only the env label is touched.
func sourceHostDNS(ephHostDNS, identityEnv, srcEnv string) string {
	labels := strings.Split(ephHostDNS, ".")
	if len(labels) > 2 && labels[2] == identityEnv {
		labels[2] = srcEnv
	}
	return strings.Join(labels, ".")
}

// decodeTargets reads a deploy-descriptor stack output and returns its targets.
// The Automation API deserialises pulumi.Any(descriptor) as a map, so the value
// is round-tripped through JSON into typed targets (mirroring
// resolveDescriptorTargets). A missing output yields no targets — an env with no
// services (or no apps) simply exports an empty descriptor.
func decodeTargets[T any](outputs auto.OutputMap, key string) ([]T, error) {
	raw, ok := outputs[key]
	if !ok {
		return nil, nil
	}
	b, err := json.Marshal(raw.Value)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", key, err)
	}
	var descriptor struct {
		Targets []T `json:"targets"`
	}
	if err := json.Unmarshal(b, &descriptor); err != nil {
		return nil, fmt.Errorf("parse %s: %w", key, err)
	}
	return descriptor.Targets, nil
}

// groupBy buckets items by a string key.
func groupBy[T any](items []T, key func(T) string) map[string][]T {
	out := map[string][]T{}
	for _, it := range items {
		k := key(it)
		out[k] = append(out[k], it)
	}
	return out
}

// dedupByName keeps the first item per name, preserving order.
func dedupByName[T any](items []T, name func(T) string) []T {
	seen := map[string]bool{}
	out := make([]T, 0, len(items))
	for _, it := range items {
		n := name(it)
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, it)
	}
	return out
}

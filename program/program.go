// Package program is the Pulumi program that turns an environment's resolved
// resources into a deployment. It is used as an inline program via the
// Automation API in the inforge CLI.
package program

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"sort"
	"strings"

	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
	"github.com/wardnet/inforge/internal/agent"
	"github.com/wardnet/inforge/internal/app"
	"github.com/wardnet/inforge/internal/dbbackup"
	"github.com/wardnet/inforge/internal/grant"
	"github.com/wardnet/inforge/internal/hostpaths"
	"github.com/wardnet/inforge/internal/hostsecrets"
	"github.com/wardnet/inforge/internal/loader"
	"github.com/wardnet/inforge/internal/manifest"
	"github.com/wardnet/inforge/internal/meshcert"
	"github.com/wardnet/inforge/internal/meshpaths"
	"github.com/wardnet/inforge/internal/meshplan"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/otelcol"
	"github.com/wardnet/inforge/internal/postgres"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/registry"
	iremote "github.com/wardnet/inforge/internal/remote"
	"github.com/wardnet/inforge/internal/service"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
	"gopkg.in/yaml.v3"
)

// globalScope is the reserved region key for the region-less global slice: the
// slot its outputs land in (computeOutputs["global"], …) and the realization key
// its providers are extracted under. It is NOT an abstract region — it carries an
// empty slug, so naming.Resource/ResourceInstance produce region-less names.
const globalScope = "global"

// Run is the Pulumi program entry point, passed to the Automation API as an
// inline program source.
func Run(ctx *pulumi.Context) error {
	cfg := config.New(ctx, "")
	// The environment is the Pulumi stack name (deploy/preview/reconcile upsert
	// the stack as the env). Default to it so a per-env inforge.<env>.yaml is not
	// required just to restate the env; an explicit `environment` config still
	// wins for anyone who sets it.
	env := cfg.Get("environment")
	if env == "" {
		env = ctx.Stack()
	}
	dir := "./resources"
	if d := cfg.Get("dir"); d != "" {
		dir = d
	}

	// srcEnv is the config SOURCE — the resources/<srcEnv>/ tree, secrets, and PKI
	// store this stack reads its definition from. It decouples WHAT to deploy from
	// the identity it deploys UNDER (ADR-0028): an ephemeral env clones a source
	// env's definition while keeping its own slug identity (env). srcEnv defaults
	// to env, so a static env reads its own config and is byte-for-byte unchanged.
	srcEnv := cfg.Get("source_environment")
	if srcEnv == "" {
		srcEnv = env
	}
	// Ephemeral-env labels (ADR-0028) stamped on every cloud resource, and the
	// slug segment the ephemeral AppFQDN exception carries. A static env has
	// ephemeral=false and an empty ephemeralSlug, so nothing downstream changes.
	eph := tags.Ephemeral{Enabled: cfg.GetBool("ephemeral"), ExpiresAt: cfg.Get("expires_at")}
	ephemeralSlug := ""
	if eph.Enabled {
		ephemeralSlug = env
	}

	vars, err := loader.LoadVariables(srcEnv, dir)
	if err != nil {
		return err
	}

	// The deploy SSH private key is a deploy-time secret used purely to SSH the
	// host and realize host-level resources (tls-termination, service units). It
	// is injected here from stack config (deploy_private_key) or
	// INFORGE_DEPLOY_PRIVATE_KEY — never read from variables.yaml. Empty in
	// preview, where no remote command runs.
	deployPrivateKey := cfg.Get("deploy_private_key")
	if deployPrivateKey == "" {
		deployPrivateKey = os.Getenv("INFORGE_DEPLOY_PRIVATE_KEY")
	}
	vars.SSH.DeployPrivateKey = deployPrivateKey

	// inforgeVersion pins the inforge-agent release asset each host downloads
	// during service provisioning. It is injected by the CLI (which knows its own
	// build version) via stack config / INFORGE_VERSION — the same pattern as
	// deploy_private_key — and defaults to "dev", which has no release asset and so
	// fails service provisioning at up time with a clear error.
	inforgeVersion := cfg.Get("inforge_version")
	if inforgeVersion == "" {
		inforgeVersion = os.Getenv("INFORGE_VERSION")
	}
	if inforgeVersion == "" {
		inforgeVersion = "dev"
	}

	regionTable, globalBlock, err := loader.LoadRegionTable(srcEnv, dir)
	if err != nil {
		return err
	}
	// Decode project-level provider defaults from stack config. The defaults are
	// serialised by the CLI (setProviderDefaults) so the program can resolve effective
	// providers without the project file.
	var providerDefaults types.ProviderDefaults
	if raw := cfg.Get("provider_defaults"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &providerDefaults); err != nil {
			return fmt.Errorf("decode provider_defaults: %w", err)
		}
	}
	// Which regions deploy comes from regions.yaml. Iterate in sorted order so
	// resource creation is deterministic across runs (the table is a map).
	regionNames := sortedKeys(regionTable)
	// The resource set is defined ONCE and instantiated into every region; the
	// region slug baked into each cloud name keeps instances unique per region.
	res, err := loader.LoadResources(srcEnv, dir)
	if err != nil {
		return err
	}
	// The global slice is instantiated once, region-less, before any region — its
	// outputs land in the "global" slot so a regional secrets ref:database/global/…
	// can resolve against them. Optional: an absent global/ dir yields an empty set.
	globalRes, err := loader.LoadGlobalResources(srcEnv, dir)
	if err != nil {
		return err
	}

	// Encrypted secret values (ADR-0017) are decrypted once, up front and
	// provider-neutrally, then threaded to every region's secrets provisioning
	// via AllOutputs. Nil unless some service declares a `vault:` secret.
	encSecrets, err := decryptEncryptedSecrets(res, globalRes, dir, srcEnv, ctx.DryRun())
	if err != nil {
		return err
	}

	// Granted PKI material (ADR-0025 slice C) is resolved the same way: once, up
	// front, from the env's committed pki.enc.yaml. Nil unless some service
	// declares a `pki/*` grant.
	pkiMaterial, err := decryptPKIGrantMaterial(res, globalRes, dir, srcEnv, ctx.DryRun())
	if err != nil {
		return err
	}

	desc, err := service.BuildDeployDescriptor(env, vars.BaseDomain, res, globalRes, regionTable)
	if err != nil {
		return err
	}
	ctx.Export("deployDescriptor", pulumi.Any(desc))

	// The app deploy descriptor mirrors the service one: it is the contract the app
	// release path (slice D) resolves an app's ingress host, deploy path, FQDN, and
	// SPA flag from. Derived purely from resolved resources, exported once.
	appDesc, err := app.BuildDeployDescriptor(env, vars.BaseDomain, res, globalRes, regionTable, ephemeralSlug)
	if err != nil {
		return err
	}
	ctx.Export("appDeployDescriptor", pulumi.Any(appDesc))

	// The mesh deploy descriptor is the mesh sibling: one target per mesh host
	// (regional + global), the contract the deploy CLI's post-up baseline step
	// uses to trigger each host's material pull (ADR-0033).
	meshDesc, err := meshplan.BuildDeployDescriptor(env, vars.BaseDomain, res, globalRes, regionTable)
	if err != nil {
		return err
	}
	ctx.Export("meshDeployDescriptor", pulumi.Any(meshDesc))

	// The db deploy descriptor is the database sibling: one target per logical
	// database (all clusters × all regions + global), the contract the
	// `inforge db backup | list-backups | restore` commands resolve a database's
	// cluster host, SSH user, TCP port, and R2 region segment from (ADR-0036).
	dbDesc, err := buildDBDeployDescriptor(env, vars.BaseDomain, res, globalRes, regionTable)
	if err != nil {
		return err
	}
	ctx.Export("dbDeployDescriptor", pulumi.Any(dbDesc))

	// Env-scoped Hetzner SSH keys (wardnet-<env>-key-{user,deploy}) carry no
	// region slug, so every scope's compute provider would register the same URN.
	// Share one cache across all registries this run builds so the keys register
	// exactly once (see .agents/rules/ssh-keys-register-once-across-scopes.md).
	sshKeyCache := registry.NewSSHKeyCache()

	registries := make(map[string]registry.ProviderRegistry, len(regionNames))
	for _, region := range regionNames {
		registries[region] = registry.BuildRegistry(ctx, regionTable[region].Providers, regionTable[region].Dns, vars.SSH, regionTable, ctx.Project(), env, region, eph, sshKeyCache)
	}

	// networkOutputs: region → specName+"/"+subnetName → NetworkOutputs. The
	// region-less global slice lands under the reserved "global" key.
	networkOutputs := map[string]map[string]types.NetworkOutputs{}
	computeOutputs := map[string]map[string]types.ComputeOutputs{}
	databaseOutputs := map[string]map[string]types.DatabaseOutputs{}

	// scope is one realization unit: the region-less global slice or one region.
	// Both realize through the identical pipeline — createInfra, then DNS records →
	// app seeds → ingress → service secrets → services — and differ only in their
	// output-map key, the naming slug (empty for global → region-less names), the
	// provider registry, the DNS authority records are written into, and which
	// resource set deploys.
	type scope struct {
		key       string // output-map key: globalScope or the region name
		slug      string // naming slug; "" for the global slice (region-less)
		reg       registry.ProviderRegistry
		authority *regions.DnsAuthority
		res       types.Resources
	}

	// The global slice is realized FIRST so a regional secrets ref:database/global/<name>
	// resolves against computeOutputs[globalScope]/databaseOutputs[globalScope], which
	// createInfra populates below. The global slice realizes against the regions.yaml
	// global providers block, keyed under globalScope so the per-region provider config
	// lookup (ExtractRegionConfigs) still resolves. Its DNS authority is the placement
	// region's (ADR-0023): the derived host/service records are region-less but
	// env-scoped (<svc>.svc.<env>, <compute>.vm.<env>), so they can't collide with the
	// slug-bearing regional records (<svc>.svc.<env>.<slug>) in that same zone. The one
	// cross-scope collision a shared zone still allows is a *literal* vanity/apex FQDN
	// (e.g. account.<base>) declared identically on both a global service and a service
	// in the placement region — operator-avoidable, not yet validated (see PR notes).
	var scopes []scope
	if globalBlock != nil && globalRes.HasAny() {
		authority := regionTable[globalBlock.PlacementRegion].Dns
		// Defence in depth for the validate-time guard (globalNeedsDNS): if validate was
		// bypassed and the global slice realizes DNS records (any compute → a <vm> record,
		// plus service/app records) but the placement region declares no dns: authority,
		// createDNSRecords would silently no-op and a tls-termination service's ACME
		// challenge would then fail mid-apply. Fail fast with the same actionable message.
		if authority == nil && len(derivedRecords(globalRes, env, "", vars.BaseDomain, ephemeralSlug)) > 0 {
			return fmt.Errorf("global slice realizes DNS records but its placementRegion %q has no dns: authority — declare a dns: block on that region in regions.yaml", globalBlock.PlacementRegion)
		}
		globalReg := registry.BuildRegistry(ctx, globalBlock.Providers, authority, vars.SSH, regionTable, ctx.Project(), env, globalScope, eph, sshKeyCache)
		scopes = append(scopes, scope{key: globalScope, slug: "", reg: globalReg, authority: authority, res: globalRes})
	}
	for _, region := range regionNames {
		slug, err := regionTable.Slug(region)
		if err != nil {
			return err
		}
		scopes = append(scopes, scope{key: region, slug: slug, reg: registries[region], authority: regionTable[region].Dns, res: res})
	}

	// Instantiate each scope's referenceable infrastructure (network, compute,
	// database) first, in scope order — global before regions, so a regional
	// ref:database/global/<name> sees the global outputs already populated.
	for _, sc := range scopes {
		if err := createInfra(ctx, sc.reg, sc.res, env, sc.key, sc.slug, vars.BaseDomain, providerDefaults, networkOutputs, computeOutputs, databaseOutputs); err != nil {
			return err
		}
	}

	// Env-level observability (ADR-0031): when otlp_endpoint is configured, every VM
	// gets the host-metrics collector. The OTLP Basic-auth credential is an inforge
	// RESERVED secret (ADR-0031) — it lives in the store's `reserved:` namespace,
	// not a service container, and is read directly here (no service references it,
	// so it is independent of the `vault:` decrypt path). base64 it once into the
	// header value and mark it secret so it is encrypted in Pulumi state. An
	// endpoint set with no credential is a hard misconfiguration (fail at up,
	// skipped in preview).
	var obsAuthB64 pulumi.StringOutput
	if vars.Observability.OTLPEndpoint != "" {
		authRaw, err := decryptReservedSecret(dir, srcEnv, otelcol.AuthSecretNamespace, otelcol.AuthSecretKey, ctx.DryRun())
		if err != nil {
			return err
		}
		if authRaw == "" && !ctx.DryRun() {
			return fmt.Errorf("observability: otlp_endpoint is set but secrets.enc.yaml is missing or has no %s/%s credential — run `inforge secret set %s %s %s --reserved` and commit the store", otelcol.AuthSecretNamespace, otelcol.AuthSecretKey, srcEnv, otelcol.AuthSecretNamespace, otelcol.AuthSecretKey)
		}
		obsAuthB64 = pulumi.ToSecret(pulumi.String(base64.StdEncoding.EncodeToString([]byte(authRaw)))).(pulumi.StringOutput)
	}

	// Env-level Grafana dashboards (ADR-0038): when grafana_url is configured, this env's
	// built-in dashboards are pushed via the pulumiverse/grafana provider. Org-global, so
	// realized ONCE here (not per scope) and env-prefixed. A no-op when grafana_url is empty.
	hasDatabase := len(res.DatabaseCluster) > 0 || len(globalRes.DatabaseCluster) > 0
	if err := realizeGrafana(ctx, dir, srcEnv, env, vars.Observability, hasDatabase, ctx.DryRun()); err != nil {
		return err
	}

	// Self-hosted Postgres backups (ADR-0036): the destination R2 bucket + endpoint are
	// threaded from inforge.yaml (setBackups); the backup-scoped R2 credential is an
	// inforge RESERVED secret (two keys mirroring the AWS_* chain), decrypted once here
	// and marked secret so the on-host EnvironmentFile write is encrypted in Pulumi
	// state. The credential is decrypted optimistically; whether its ABSENCE is fatal is
	// decided per-scope in provisionDatabaseBackups, which knows if any database in that
	// scope actually enables backups — so a bucket set fleet-wide never forces a
	// credential onto a scope whose databases all opt out.
	backupsBucket := cfg.Get("backups_bucket")
	backupsEndpoint := cfg.Get("backups_endpoint")
	var backupsAccessKey, backupsSecretKey pulumi.StringOutput
	backupsCredsPresent := false
	if backupsBucket != "" {
		accessRaw, err := decryptReservedSecret(dir, srcEnv, dbbackup.AuthSecretNamespace, dbbackup.AccessKeyIDKey, ctx.DryRun())
		if err != nil {
			return err
		}
		secretRaw, err := decryptReservedSecret(dir, srcEnv, dbbackup.AuthSecretNamespace, dbbackup.SecretAccessKeyKey, ctx.DryRun())
		if err != nil {
			return err
		}
		backupsCredsPresent = accessRaw != "" && secretRaw != ""
		backupsAccessKey = pulumi.ToSecret(pulumi.String(accessRaw)).(pulumi.StringOutput)
		backupsSecretKey = pulumi.ToSecret(pulumi.String(secretRaw)).(pulumi.StringOutput)
	}

	// Post-process each scope through the host-level pipeline. The order within a
	// scope is load-bearing: DNS records first (ACME HTTP-01 needs the A-record to
	// exist), then app seeds + ingress (nginx/ACME), then service secrets, then the
	// services that depend on them.
	for _, sc := range scopes {
		if err := createDNSRecords(ctx, sc.reg, sc.authority, sc.res, computeOutputs[sc.key], env, sc.slug, vars.BaseDomain, ephemeralSlug); err != nil {
			return err
		}

		// gates memoizes one cloud-init readiness gate per host in this scope. Both
		// ingress realization and service provisioning SSH the same hosts, so the gate
		// they each depend on must be the same resource — share the map (per scope).
		gates := map[string]pulumi.Resource{}
		// Attach each host's private network AFTER its cloud-init readiness gate, then
		// record the resulting private IP on the host's compute outputs. The private
		// NIC is deliberately not attached at server-create (see
		// .agents/rules/attach-private-network-after-cloud-init-gate.md): deferring past
		// first boot avoids the cloud-init >= 25.3 Hetzner init-local race. This runs
		// before realizeIngress — the sole PrivateIP consumer — and memoizes each
		// host's gate for the later provisioning passes to reuse.
		if err := attachPrivateNetworks(ctx, sc.reg, sc.res, computeOutputs[sc.key], gates, providerDefaults, vars.SSH.DeployPrivateKey, env, sc.slug); err != nil {
			return err
		}
		// Seed each app's placeholder bundle on its ingress host first, so its server
		// block and ACME cert provision before the first release (slice D delivers
		// bundles). realizeIngress's nginx reload DependsOn these seeds (appSeeds) so a
		// reload never serves an app's document root before `current` exists.
		appSeeds, err := provisionApps(ctx, sc.res, computeOutputs[sc.key], gates, vars.SSH.DeployPrivateKey, env, sc.slug)
		if err != nil {
			return err
		}
		if err := realizeIngress(ctx, sc.reg, sc.res, computeOutputs[sc.key], gates, appSeeds, vars.SSH.DeployPrivateKey, env, sc.slug, vars.BaseDomain, ephemeralSlug, providerDefaults); err != nil {
			return err
		}
		// The east-west mesh proxy (ADR-0032) materializes on every host running a
		// pki: service. It needs the scope's private IPs (attached above) and the
		// global slice's outputs (realized first, so its public IPs are ready for a
		// regional scope's cross-scope targets).
		if err := realizeMesh(ctx, sc.reg, sc.res, globalRes, computeOutputs, sc.key, sc.slug, regionNames, gates, vars.SSH.DeployPrivateKey, env, providerDefaults); err != nil {
			return err
		}
		// Self-hosted database clusters (ADR-0036): install Postgres on each cluster's
		// host, provision its data volume + logical databases, and register a
		// per-database DBRoleProvisioner into databaseOutputs. Runs after the private
		// network is attached (it needs the host's private IP) and before service
		// secrets (grants resolve against databaseOutputs). The global slice runs first
		// (scopes are global-first), so a regional grant on a global database resolves.
		dbHostTails, err := provisionDatabaseClusters(ctx, sc.reg, sc.res, computeOutputs[sc.key], databaseOutputs[sc.key], gates, providerDefaults, vars.SSH.DeployPrivateKey, env, sc.key, sc.slug)
		if err != nil {
			return err
		}
		// Per-database backup timers (ADR-0036) land on each cluster host after the
		// cluster is up (DependsOn dbHostTails). No-op when no backups bucket is
		// configured and every database opted out.
		if err := provisionDatabaseBackups(ctx, sc.res, computeOutputs[sc.key], dbHostTails, backupsBucket, backupsEndpoint, backupsCredsPresent, backupsAccessKey, backupsSecretKey, vars.SSH.DeployPrivateKey, env, sc.slug); err != nil {
			return err
		}
		all := types.AllOutputs{Compute: computeOutputs, Database: databaseOutputs, Encrypted: encSecrets, PKI: pkiMaterial}
		serviceSecrets, err := provisionServiceSecrets(ctx, sc.res, all, env, sc.key, sc.slug)
		if err != nil {
			return err
		}
		if err := provisionServices(ctx, sc.res, computeOutputs[sc.key], serviceSecrets, gates, vars.SSH.DeployPrivateKey, env, sc.key, sc.slug, vars.BaseDomain, inforgeVersion); err != nil {
			return err
		}
		if err := provisionObservability(ctx, sc.res, computeOutputs[sc.key], gates, dbHostTails, vars.Observability, obsAuthB64, vars.SSH.DeployPrivateKey, env, sc.slug); err != nil {
			return err
		}
	}

	return nil
}

// createInfra instantiates one scope's network, compute, and database resources
// into the output maps under the key `region`, using `slug` for naming (empty for
// the global scope → region-less names). It is the shared body of the per-region
// instantiation and the global-first pass: the same resource pipeline keyed by a
// different (region, slug) pair. Compute domains and manifests are scoped by the
// same slug, so global hosts get region-less FQDNs and regional hosts keep their
// slug-scoped ones.
func createInfra(
	ctx *pulumi.Context, reg registry.ProviderRegistry, res types.Resources, env, region, slug, baseDomain string,
	defaults types.ProviderDefaults,
	networkOutputs map[string]map[string]types.NetworkOutputs,
	computeOutputs map[string]map[string]types.ComputeOutputs,
	databaseOutputs map[string]map[string]types.DatabaseOutputs,
) error {
	networkOutputs[region] = map[string]types.NetworkOutputs{}
	computeOutputs[region] = map[string]types.ComputeOutputs{}
	databaseOutputs[region] = map[string]types.DatabaseOutputs{}

	// Derive each host's inbound firewall port plan up front: the firewall is a
	// pure consumer of this set, and cp.Create (below) runs before ingress
	// realization, so the derivation cannot wait for the routes. It is computed
	// from static spec data (which ingress fronts which service, co-located or not),
	// so no outputs are involved.
	fwPlan := firewallPlanByHost(res, region == globalScope)

	for _, spec := range res.Network {
		np, err := reg.Network(types.ResolveProvider(spec.Provider, "network", "", defaults))
		if err != nil {
			return err
		}
		subnetMap, err := np.Create(ctx, spec, env, region)
		if err != nil {
			return err
		}
		for subnetName, out := range subnetMap {
			networkOutputs[region][spec.Name+"/"+subnetName] = out
		}
	}

	for _, spec := range res.Compute {
		man, err := assembleManifest(spec, env, region, slug)
		if err != nil {
			return err
		}

		netOut, err := resolveNetworkOutput(spec, res.Network, networkOutputs[region])
		if err != nil {
			return fmt.Errorf("compute %s/%s: %w", region, spec.Name, err)
		}

		cp, err := reg.Compute(types.ResolveProvider(spec.Provider, "compute", "", defaults))
		if err != nil {
			return err
		}
		for i := 1; i <= spec.InstanceCount; i++ {
			key := naming.SpecKey(spec.Name, i)
			// The host's SSH / cloud-init domain is derived from the bare compute
			// name with the "vm" segment — deterministic, never a free-form record.
			domain := naming.HostFQDN(env, slug, spec.Name, baseDomain)
			out, err := cp.Create(ctx, spec, netOut, env, region, domain, man, fwPlan[key])
			if err != nil {
				return err
			}
			computeOutputs[region][key] = out
		}
	}

	// Database clusters are NOT realized here: a self-hosted cluster needs each host's
	// private IP + cloud-init gate (attached in the host-level pipeline), so it is
	// realized by provisionDatabaseClusters after attachPrivateNetworks and before
	// provisionServiceSecrets, populating databaseOutputs[region] there (ADR-0036).
	return nil
}

// cloudInitGate returns the per-host cloud-init readiness gate, creating it on
// first use for a host and memoizing it in gates by canonical host specKey. It
// connects as ROOT — Hetzner injects the deploy public key into root's
// authorized_keys at server creation (via the server's SshKeys), so root SSH
// works before cloud-init has finished creating the asynchronous deploy_user —
// and blocks on `cloud-init status --wait`, which exits non-zero if cloud-init
// failed (so the gate fails the deploy loudly rather than racing it). It carries
// no Triggers, so Pulumi runs it about once per host (first provision); every
// per-host SSH command DependsOn it, so none races deploy_user creation. In
// preview command.remote never connects, so there is no behavior change there.
func cloudInitGate(ctx *pulumi.Context, gates map[string]pulumi.Resource, hostKey string, host types.ComputeOutputs, deployPrivateKey, env, slug string) (pulumi.Resource, error) {
	if g, ok := gates[hostKey]; ok {
		return g, nil
	}
	conn := iremote.Connection(host.PublicIP, "root", deployPrivateKey)
	// The gate is the one truly per-host command resource (all sibling commands are
	// per service/terminator), so its logical name keys on the canonical host
	// specKey — yielding a stable, unique Pulumi name, one per host.
	name := naming.Resource(env, slug, "vm", hostKey) + "-cloudinit-ready"
	const wait = "cloud-init status --wait"
	gate, err := remote.NewCommand(ctx, name, &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(wait),
		Update:     pulumi.String(wait),
	})
	if err != nil {
		return nil, fmt.Errorf("host %q: cloud-init readiness gate: %w", hostKey, err)
	}
	gates[hostKey] = gate
	return gate, nil
}

// attachPrivateNetworks attaches every host's private network AFTER its cloud-init
// readiness gate, then records the assigned private IP on the host's compute outputs.
// The private NIC is not attached at server-create: on cloud-init >= 25.3 a NIC
// present at first boot races the Hetzner init-local network path and crashes it,
// leaving a sticky `cloud-init status: error` that fails the gate (see
// .agents/rules/attach-private-network-after-cloud-init-gate.md). Attaching after the
// gate (first boot done) lets the image's hotplug path configure the NIC cleanly.
//
// It runs once per scope, before any pass that consumes a backend's PrivateIP
// (realizeIngress), and creates each host's gate through the shared, memoized `gates`
// map so the later provisioning passes reuse the same gate resource.
func attachPrivateNetworks(ctx *pulumi.Context, reg registry.ProviderRegistry, res types.Resources, computeOut map[string]types.ComputeOutputs, gates map[string]pulumi.Resource, defaults types.ProviderDefaults, deployPrivateKey, env, slug string) error {
	for _, spec := range res.Compute {
		cp, err := reg.Compute(types.ResolveProvider(spec.Provider, "compute", "", defaults))
		if err != nil {
			return err
		}
		for i := 1; i <= spec.InstanceCount; i++ {
			key := naming.SpecKey(spec.Name, i)
			host, ok := computeOut[key]
			if !ok {
				return fmt.Errorf("attach network: no compute output for host %q", key)
			}
			gate, err := cloudInitGate(ctx, gates, key, host, deployPrivateKey, env, slug)
			if err != nil {
				return err
			}
			privateIP, err := cp.AttachNetwork(ctx, spec, i, []pulumi.Resource{gate})
			if err != nil {
				return err
			}
			host.PrivateIP = privateIP
			computeOut[key] = host
		}
	}
	return nil
}

// provisionServices writes the host-side scaffolding for each service in a
// region over SSH: the systemd unit (service.Unit) and the service folder, plus
// the no-login service user when one is declared. This is the raw/systemd
// delivery path; a future container path would dispatch through a provider.
//
// It never starts the unit. ExecStart=<folder>/run does not exist until
// `inforge release` delivers code, so a start here would fail and abort the
// whole `pulumi up`. The unit is written, daemon-reloaded, and enabled (for
// boot persistence); release performs the first real start with code present.
// Connection details and the preview/up guard mirror realizeIngress.
func provisionServices(ctx *pulumi.Context, res types.Resources, computeOut map[string]types.ComputeOutputs, serviceSecrets map[string]serviceMaterial, gates map[string]pulumi.Resource, deployPrivateKey, env, region, slug, baseDomain, inforgeVersion string) error {
	if len(res.Service) == 0 {
		return nil
	}
	// The unit's ExecStart is inforge-agent, downloaded per host pinned to
	// this inforge version. A "dev" build publishes no release asset, so fail
	// the deploy with a clear message rather than emitting a doomed download.
	// Enforced only at up time; preview never runs the command.
	if !ctx.DryRun() && inforgeVersion == "dev" {
		return fmt.Errorf("cannot provision services: inforge build is 'dev' — no inforge-agent release asset to download; deploy with a released inforge binary")
	}
	canonical := naming.CanonicalComputeKeys(res.Compute)
	deployUserByCompute := naming.DeployUsersByHost(res.Compute)
	// Each pki: service's loopback egress port (its INFORGE_MESH_URL, ADR-0032),
	// matching the listener the mesh proxy binds for it.
	meshEgressPorts := meshEgressPortsByService(res, canonical)

	for _, svc := range res.Service {
		hostKey, ok := canonical[svc.Host]
		if !ok {
			return fmt.Errorf("service %q: host %q does not resolve to a host", svc.Name, svc.Host)
		}
		host, ok := computeOut[hostKey]
		if !ok {
			return fmt.Errorf("service %q: host %q has no compute output (available: %v)", svc.Name, hostKey, sortedKeys(computeOut))
		}
		deployUser := deployUserByCompute[hostKey]
		// Enforced only at up time; during preview command.remote never connects.
		if !ctx.DryRun() {
			if deployUser == "" {
				return fmt.Errorf("service %q: host %q has no deploy_user; inforge needs one to SSH and provision the unit", svc.Name, svc.Host)
			}
			if deployPrivateKey == "" {
				return fmt.Errorf("service %q: no deploy private key configured (set the deploy_private_key stack config or INFORGE_DEPLOY_PRIVATE_KEY)", svc.Name)
			}
		}
		// Every per-host SSH command waits on the host's cloud-init readiness gate
		// so it never races the asynchronous deploy_user creation.
		gate, err := cloudInitGate(ctx, gates, hostKey, host, deployPrivateKey, env, slug)
		if err != nil {
			return err
		}
		if err := provisionService(ctx, svc, host, deployUser, deployPrivateKey, env, slug, inforgeVersion, gate); err != nil {
			return err
		}
		// Every service gets a descriptor.yaml. A service with resolved env/grant
		// secrets — or with pki-grant file material — also gets a host-key-encrypted
		// secrets.age (ADR-0035); a service with neither gets a static descriptor
		// with no env (mtls_files: still gets its files: entries in the descriptor —
		// its leaf.age is delivered later by `inforge pki renew`, not here).
		if material := serviceSecrets[svc.Name]; !material.empty() {
			if err := deliverServiceSecrets(ctx, svc, host, material, deployUser, deployPrivateKey, env, region, slug, baseDomain, hostKey, meshEgressPorts[svc.Name], gate); err != nil {
				return err
			}
		} else {
			if err := deliverServiceDescriptor(ctx, svc, host, deployUser, deployPrivateKey, env, region, slug, baseDomain, hostKey, meshEgressPorts[svc.Name], gate); err != nil {
				return err
			}
		}
	}
	return nil
}

// provisionService writes one service's unit + folder (+ no-login user) on its
// host. The unit is enabled but never started here (see provisionServices).
func provisionService(ctx *pulumi.Context, svc types.ServiceSpec, host types.ComputeOutputs, deployUser, deployPrivateKey, env, slug, inforgeVersion string, gate pulumi.Resource) error {
	conn := iremote.Connection(host.PublicIP, deployUser, deployPrivateKey)
	createScript := serviceProvisionScript(svc, inforgeVersion)
	name := naming.Resource(env, slug, "svc", svc.Name)
	if _, err := remote.NewCommand(ctx, name+"-provision", &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(createScript),
		// Update (not replace) when the script changes: the script is idempotent
		// and re-runs in place. Without Update, a Triggers change replaces the
		// resource, running Create AND Delete (disable --now) on the same unit —
		// e.g. when a later slice changes the unit's ExecStart for every service.
		Update:   pulumi.String(createScript),
		Delete:   pulumi.String(serviceDeprovisionScript(svc)),
		Triggers: pulumi.Array{pulumi.String(createScript)},
		// DeleteBeforeReplace (resource option below): if a replace is ever
		// forced anyway (e.g. Connection changes because the host was
		// recreated), Pulumi's default create-before-delete order would run
		// this Delete SECOND — disabling and rm -f'ing the SAME unit path the
		// Create step just installed, since both old and new target the
		// identical host/unit name. That silently leaves the unit permanently
		// missing despite a "successful" apply (confirmed in production
		// against wardnet-infrastructure's tenants service). Matches the
		// DeleteBeforeReplace already used by deliverServiceSecrets/
		// deliverServiceDescriptor's remote.Command calls for the same reason.
	}, pulumi.DependsOn([]pulumi.Resource{gate}), pulumi.DeleteBeforeReplace(true)); err != nil {
		return fmt.Errorf("service %q: provision unit: %w", svc.Name, err)
	}
	return nil
}

// provisionObservability installs the host VM-metrics collector on every VM in this
// region (ADR-0031), gated on the env defining observability config: it is a no-op
// when obs.OTLPEndpoint is empty. The agent is always-on otherwise — every host
// gets it, no per-compute opt-in — and stamps the same cloud/host resource identity
// (ADR-0030) inforge injects into app telemetry, so host metrics correlate with app
// telemetry on host.id. It is scoped to regional hosts, matching provisionServices
// (global-placement hosts are not service-provisioned in this loop either).
//
// authB64 is the base64 OTLP Basic-auth value, already marked secret by the caller;
// the credential write is built inside an ApplyT over it so the secret is encrypted
// in Pulumi state (never written as plaintext), mirroring deliverServiceSecrets.
func provisionObservability(ctx *pulumi.Context, res types.Resources, computeOut map[string]types.ComputeOutputs, gates map[string]pulumi.Resource, dbHostTails map[string]pulumi.Resource, obs types.ObservabilityConfig, authB64 pulumi.StringOutput, deployPrivateKey, env, slug string) error {
	if obs.OTLPEndpoint == "" {
		return nil
	}
	deployUserByCompute := naming.DeployUsersByHost(res.Compute)
	// Postgres cluster ports per host, single-sourced with the realization + firewall
	// (ADR-0037); used to add a postgresql receiver per co-located cluster.
	ports := clusterPortsByHost(res, naming.CanonicalComputeKeys(res.Compute))
	for _, hostKey := range sortedKeys(computeOut) {
		host := computeOut[hostKey]
		deployUser := deployUserByCompute[hostKey]
		if !ctx.DryRun() {
			if deployUser == "" {
				return fmt.Errorf("observability: host %q has no deploy_user; inforge needs one to SSH and install the collector", hostKey)
			}
			if deployPrivateKey == "" {
				return fmt.Errorf("observability: no deploy private key configured (set the deploy_private_key stack config or INFORGE_DEPLOY_PRIVATE_KEY)")
			}
		}
		gate, err := cloudInitGate(ctx, gates, hostKey, host, deployPrivateKey, env, slug)
		if err != nil {
			return err
		}
		conn := iremote.Connection(host.PublicIP, deployUser, deployPrivateKey)
		name := naming.Resource(env, slug, "otelcol", hostKey)

		install := otelcol.InstallScript(otelcol.DefaultVersion)
		installCmd, err := remote.NewCommand(ctx, name+"-install", &remote.CommandArgs{
			Connection: conn,
			Create:     pulumi.String(install),
			Update:     pulumi.String(install),
			Triggers:   pulumi.Array{pulumi.String(install)},
		}, pulumi.DependsOn([]pulumi.Resource{gate}))
		if err != nil {
			return fmt.Errorf("observability: host %q: install collector: %w", hostKey, err)
		}

		// Postgres metrics (ADR-0037): one postgresql receiver per self-hosted cluster on
		// this host, scraping the local instance as a per-cluster pg_monitor monitoring
		// role. Mint the role over the cluster's local peer auth (DependsOn the cluster
		// being up via dbHostTails), then hand the collector a target + the role's
		// password file. A cluster whose databases all opted out (metrics: false) gets no
		// receiver. The config command waits on every mint so the collector never restarts
		// with a receiver whose role does not exist yet.
		var pgTargets []otelcol.PostgresTarget
		pgPasswords := []interface{}{}
		configDeps := []pulumi.Resource{installCmd}
		for _, cluster := range sortedKeys(ports[hostKey]) {
			var dbNames []string
			for _, d := range databasesOfCluster(res, cluster) {
				if d.MetricsEnabled() {
					dbNames = append(dbNames, d.Database)
				}
			}
			if len(dbNames) == 0 {
				continue
			}
			port := ports[hostKey][cluster]
			roleName := naming.Resource(env, slug, "dbrole", cluster+"-otelmon")
			pw, err := random.NewRandomPassword(ctx, roleName+"-password", &random.RandomPasswordArgs{
				Length:  pulumi.Int(32),
				Special: pulumi.Bool(false),
			})
			if err != nil {
				return fmt.Errorf("observability: host %q cluster %q: monitor password: %w", hostKey, cluster, err)
			}
			mintScript := pw.Result.ApplyT(func(p string) string {
				return postgres.MintMonitorRoleScript(port, roleName, p, dbNames)
			}).(pulumi.StringOutput)
			mintDeps := []pulumi.Resource{}
			if tail := dbHostTails[hostKey]; tail != nil {
				mintDeps = append(mintDeps, tail)
			}
			mintCmd, err := remote.NewCommand(ctx, roleName+"-mint", &remote.CommandArgs{
				Connection: conn,
				Create:     mintScript,
				Update:     mintScript,
				Triggers:   pulumi.Array{mintScript},
				// The monitor role owns no objects, so DROP OWNED just revokes its
				// pg_monitor membership + CONNECT grants; postgres.OSUser is the bootstrap
				// superuser the REASSIGN targets (a no-op here).
				Delete: pulumi.String(postgres.DropRoleScript(port, roleName, postgres.OSUser)),
				// DeleteBeforeReplace: same reasoning as provisionService's
				// remote.Command — a forced replace with the default
				// create-before-delete order would mint the role then
				// immediately DROP it (same roleName, same host), silently
				// leaving the monitor role missing after a "successful" apply.
			}, pulumi.DependsOn(mintDeps), pulumi.DeleteBeforeReplace(true))
			if err != nil {
				return fmt.Errorf("observability: host %q cluster %q: mint monitor role: %w", hostKey, cluster, err)
			}
			configDeps = append(configDeps, mintCmd)
			pgTargets = append(pgTargets, otelcol.PostgresTarget{
				Cluster:      cluster,
				Port:         port,
				Username:     roleName,
				PasswordFile: otelcol.MonitorPasswordPath(cluster),
				Databases:    dbNames,
			})
			pgPasswords = append(pgPasswords, pw.Result)
		}

		config, err := otelcol.Render(obs.OTLPEndpoint, otelcol.Attributes{
			HostID:           naming.Resource(env, slug, "vm", hostKey),
			CloudProvider:    host.CloudProvider,
			CloudRegion:      host.CloudRegion,
			AvailabilityZone: host.AvailabilityZone,
			MachineType:      host.MachineType,
			Environment:      env,
			RegionSlug:       slug,
		}, pgTargets)
		if err != nil {
			return fmt.Errorf("observability: host %q: render config: %w", hostKey, err)
		}
		// The OTLP credential and each monitor-role password are secret; build the whole
		// write+apply script inside an ApplyT over them so the command's Create is
		// encrypted in state. Order: write the 0600 OTLP credential + each 0600 monitor
		// password (owned by the collector user), then the config + enable + restart (a
		// changed ${file:} value is only re-read on start).
		applyInputs := append([]interface{}{authB64}, pgPasswords...)
		applyScript := pulumi.All(applyInputs...).ApplyT(func(vals []interface{}) string {
			parts := []string{otelcol.CredentialScript(vals[0].(string))}
			for i, t := range pgTargets {
				parts = append(parts, otelcol.WriteFileScript(t.PasswordFile, vals[i+1].(string), "0600", otelcol.User+":"+otelcol.User))
			}
			parts = append(parts, otelcol.ApplyScript(config))
			return strings.Join(parts, "\n")
		}).(pulumi.StringOutput)
		if _, err := remote.NewCommand(ctx, name+"-config", &remote.CommandArgs{
			Connection: conn,
			Create:     applyScript,
			Update:     applyScript,
			Triggers:   pulumi.Array{applyScript},
		}, pulumi.DependsOn(configDeps)); err != nil {
			return fmt.Errorf("observability: host %q: configure collector: %w", hostKey, err)
		}
	}
	return nil
}

// provisionApps seeds each app's on-host folder with a placeholder bundle on its
// ingress host, so the app's nginx server block and Let's Encrypt certificate
// provision before the first real release (slice D). It runs once per app over
// SSH, waiting on the ingress host's cloud-init gate (shared with ingress
// realization). It is idempotent and re-runnable: the placeholder bytes are
// rewritten, but the `current` symlink is pointed at the placeholder only when no
// release has already claimed it — so re-running never reverts a deployed app.
func provisionApps(ctx *pulumi.Context, res types.Resources, computeOut map[string]types.ComputeOutputs, gates map[string]pulumi.Resource, deployPrivateKey, env, slug string) (map[string][]pulumi.Resource, error) {
	if len(res.App) == 0 {
		return nil, nil
	}
	// seeded collects each app's placeholder-seed command keyed by ingress host, so
	// the ingress nginx realization can DependsOn them — its reload must not race
	// ahead of the placeholder existing under `current` (else the freshly reloaded
	// server block serves a missing document root until the seed lands).
	seeded := map[string][]pulumi.Resource{}
	canonical := naming.CanonicalComputeKeys(res.Compute)
	deployUserByCompute := naming.DeployUsersByHost(res.Compute)
	for _, ia := range resolveIngressApps(res, canonical) {
		host, ok := computeOut[ia.ingHost]
		if !ok {
			return nil, fmt.Errorf("app %q: ingress host %q has no compute output (available: %v)", ia.app.Name, ia.ingHost, sortedKeys(computeOut))
		}
		deployUser := deployUserByCompute[ia.ingHost]
		// Enforced only at up time; during preview command.remote never connects.
		if !ctx.DryRun() {
			if deployUser == "" {
				return nil, fmt.Errorf("app %q: ingress host %q has no deploy_user; inforge needs one to SSH and seed the app placeholder", ia.app.Name, ia.ingHost)
			}
			if deployPrivateKey == "" {
				return nil, fmt.Errorf("app %q: no deploy private key configured (set the deploy_private_key stack config or INFORGE_DEPLOY_PRIVATE_KEY)", ia.app.Name)
			}
		}
		gate, err := cloudInitGate(ctx, gates, ia.ingHost, host, deployPrivateKey, env, slug)
		if err != nil {
			return nil, err
		}
		conn := iremote.Connection(host.PublicIP, deployUser, deployPrivateKey)
		script := appProvisionScript(ia.app.Name)
		name := naming.Resource(env, slug, "app", ia.app.Name)
		cmd, err := remote.NewCommand(ctx, name+"-provision", &remote.CommandArgs{
			Connection: conn,
			Create:     pulumi.String(script),
			Update:     pulumi.String(script),
			Triggers:   pulumi.Array{pulumi.String(script)},
		}, pulumi.DependsOn([]pulumi.Resource{gate}))
		if err != nil {
			return nil, fmt.Errorf("app %q: seed placeholder: %w", ia.app.Name, err)
		}
		seeded[ia.ingHost] = append(seeded[ia.ingHost], cmd)
	}
	return seeded, nil
}

// appProvisionScript renders the host shell that seeds an app's placeholder
// bundle and points `current` at it on first provision. The placeholder index is
// always (re)written; the symlink is created only when nothing already occupies
// `current`, so a re-run never clobbers a released bundle. All paths are quoted.
func appProvisionScript(name string) string {
	return strings.Join([]string{
		"set -euo pipefail",
		// WriteFileScript creates the placeholder dir (and the app folder) and writes
		// the index; it embeds its own `set -euo pipefail`, harmless when nested.
		iremote.WriteFileScript(app.PlaceholderIndexPath(name), app.PlaceholderIndexHTML),
		// Point current -> placeholder only when nothing is there yet — the seed
		// half of the atomic-current-swap contract, centralized in internal/app so
		// it agrees byte-for-byte with the release path's SwapCurrentScript.
		app.SeedCurrentScript(name),
	}, "\n")
}

// serviceMaterial is everything a service's agent inputs carry beyond the static
// deployment context: the resolved env-var secrets and the projected file
// material. Both halves stay unresolved (pulumi.StringOutput) until
// deliverServiceSecrets awaits them inside the remote command's ApplyT.
//
// The two are delivered by one mechanism but reach the service differently: an
// Env value is injected into the exec'd child's environment, whereas a file's PEM
// is written to the service's tmpfs RuntimeDir and only its PATH is injected (via
// DescriptorFiles). That is why the descriptor's files: map is a separate,
// plan-time-known field — it names keys, never content.
type serviceMaterial struct {
	// Env maps env-var name -> resolved secret value (environment.yaml refs +
	// database grant outputs).
	Env map[string]pulumi.StringOutput
	// DescriptorFiles maps env-var name -> blob file key, the descriptor's files:
	// map. Known at plan time.
	DescriptorFiles map[string]string
	// Files maps blob file key -> PEM content, the blob's Files map.
	Files map[string]pulumi.StringOutput
}

// empty reports whether the service has nothing to deliver — no secrets.age is
// written and it takes the static secret-less descriptor path.
func (m serviceMaterial) empty() bool {
	return len(m.Env) == 0 && len(m.Files) == 0
}

// provisionServiceSecrets resolves each service's agent material (ADR-0035):
// environment.yaml refs (resolveRef) and database grant outputs
// (resolveDatabaseGrants) as env-var secrets, plus pki grant material
// (resolvePKIGrants) as projected files.
//
// A service with none of them yields no entry — it gets a unit + a static
// secret-less descriptor (see deliverServiceDescriptor); an mtls_files: true
// service with no env/grants also takes that path here (its leaf.age is delivered
// later by `inforge pki renew`, not this pass) but still gets its descriptor
// files: entries via renderDescriptor.
func provisionServiceSecrets(ctx *pulumi.Context, res types.Resources, all types.AllOutputs, env, region, slug string) (map[string]serviceMaterial, error) {
	out := map[string]serviceMaterial{}
	for _, svc := range res.Service {
		if len(svc.Environment) == 0 && len(svc.Grants) == 0 {
			continue
		}
		resolved := map[string]pulumi.StringOutput{}
		for key, source := range svc.Environment {
			val, err := resolveRef(source, svc.Container, region, all)
			if err != nil {
				return nil, fmt.Errorf("service %q: resolve ref for secret %q: %w", svc.Name, key, err)
			}
			resolved[key] = val
		}
		grantSecrets, err := resolveDatabaseGrants(ctx, svc, all, env, region, slug)
		if err != nil {
			return nil, fmt.Errorf("service %q: resolve grants: %w", svc.Name, err)
		}
		maps.Copy(resolved, grantSecrets)

		descriptorFiles, blobFiles, err := resolvePKIGrants(ctx, svc, all, env, region)
		if err != nil {
			return nil, fmt.Errorf("service %q: resolve grants: %w", svc.Name, err)
		}

		m := serviceMaterial{Env: resolved, DescriptorFiles: descriptorFiles, Files: blobFiles}
		if !m.empty() {
			out[svc.Name] = m
		}
	}
	return out, nil
}

// resolveDatabaseGrants materializes a service's database/* grants into env-var →
// value-secret outputs (ADR-0025): for each such grant it mints a consumer-scoped
// per-service role on the target database (resolved through AllOutputs the same way
// ref: is, including the global/ redirect) and interpolates each outputs: template
// over the role's connection fields. pki/* grants are not wired here (slice C).
func resolveDatabaseGrants(ctx *pulumi.Context, svc types.ServiceSpec, all types.AllOutputs, env, region, slug string) (map[string]pulumi.StringOutput, error) {
	if len(svc.Grants) == 0 {
		return nil, nil
	}
	out := map[string]pulumi.StringOutput{}
	for _, g := range svc.Grants {
		typ, name, ok := strings.Cut(g.Resource, "/")
		// Only value-field (database) grants are materialized here. File-field
		// grants (pki/*) are projected via the descriptor files: path, not the
		// secrets batch — resolvePKIGrants owns that seam, and it rejects any grant
		// type neither resolver materializes, so a new Grantable cannot silently
		// reach a host as a missing env var.
		if !ok || typ != grantTypeDatabase {
			continue
		}
		// Resolve the target the same way ref: does (shared global/ redirect).
		db, refRegion, dbName, found := types.ResolveScoped(all.Database, region, name)
		if !found {
			return nil, fmt.Errorf("grant %q: database %q not found in region %q", g.Resource, dbName, refRegion)
		}
		if db.RoleProvisioner == nil {
			return nil, fmt.Errorf("grant %q: database %q has no role provisioner", g.Resource, dbName)
		}
		// Consumer-scoped role identity: the consuming service's env+slug, so two
		// regions granting the same (global) database never collide on a role.
		roleName := naming.Resource(env, slug, "dbrole", svc.Name+"-"+dbName)
		gdb := grant.Database{RoleProvisioner: db.RoleProvisioner, RoleName: roleName}
		fields, err := gdb.Grant(ctx, svc.Name, grant.Permission(g.Permission), env, region)
		if err != nil {
			return nil, fmt.Errorf("grant %q: %w", g.Resource, err)
		}
		for envName, tmpl := range g.Outputs {
			secret, err := interpolateGrantOutput(tmpl, fields.Values)
			if err != nil {
				return nil, fmt.Errorf("grant %q output %q: %w", g.Resource, envName, err)
			}
			out[envName] = secret
		}
	}
	return out, nil
}

// interpolateGrantOutput resolves a grant outputs: template over the grant's value
// fields, returning the composed secret as a StringOutput. Validation (slice A) has
// already checked the template parses and references only published fields.
func interpolateGrantOutput(tmpl string, values map[string]pulumi.StringOutput) (pulumi.StringOutput, error) {
	t, err := grant.ParseTemplate(tmpl)
	if err != nil {
		return pulumi.StringOutput{}, err
	}
	// Await only the fields this template references (deduped, stable order) —
	// not the whole value set — so an unreferenced field never gates this output.
	seen := map[string]bool{}
	var names []string
	for _, f := range t.Fields() {
		if seen[f] {
			continue
		}
		seen[f] = true
		if _, ok := values[f]; !ok {
			return pulumi.StringOutput{}, fmt.Errorf("field {%s} is not published by the grant", f)
		}
		names = append(names, f)
	}
	outs := make([]any, len(names))
	for i, n := range names {
		outs[i] = values[n]
	}
	return pulumi.All(outs...).ApplyT(func(args []any) (string, error) {
		resolved := make(map[string]string, len(names))
		for i, n := range names {
			resolved[n] = args[i].(string)
		}
		return t.Interpolate(resolved)
	}).(pulumi.StringOutput), nil
}

// deliverServiceSecrets writes a service's agent inputs onto its host: the
// secret-free descriptor.yaml (0644, fully known at plan time — no Output to
// await) and the host-key-encrypted secrets.age (0600, ADR-0035). It is a
// two-phase output dependency: a command reads the host SSH public key
// (Stdout), then a second command builds the final hostsecrets.Blob (env-var
// name -> resolved plaintext, plus file key -> PEM) inside an ApplyT over the
// pubkey + every resolved Output, hashes it (Blob.Hash — the change-detection
// input, NOT the non-deterministic age ciphertext), age-encrypts it to the host
// key, and writes both files. The reload-or-restart step rides in the SAME
// command's script, so it only runs when Pulumi actually re-executes Create/
// Update — i.e. only when the resolved plaintext hash (or the static
// descriptor content) changed. Connection details and the preview/up guards
// mirror provisionService.
//
// The blob carries both halves of the material: Env values are injected into the
// exec'd child's environment, while Files values are PEMs the agent projects into
// the service's tmpfs RuntimeDir, handing the service only the PATH (the
// descriptor's files: map). Both feed the hash, so rotating a granted PKI's
// material re-writes secrets.age and restarts the service exactly like a changed
// secret value.
func deliverServiceSecrets(ctx *pulumi.Context, svc types.ServiceSpec, host types.ComputeOutputs, material serviceMaterial, deployUser, deployPrivateKey, env, region, slug, baseDomain, computeKey string, meshEgressPort int, gate pulumi.Resource) error {
	conn := iremote.Connection(host.PublicIP, deployUser, deployPrivateKey)
	name := naming.Resource(env, slug, "svc", svc.Name)

	hostKey, err := readHostPubKey(ctx, conn, name+"-hostkey", gate)
	if err != nil {
		return fmt.Errorf("service %q: read host key: %w", svc.Name, err)
	}

	// The descriptor names every env var the service expects (an identity map) and
	// every file it projects (env var -> blob key) — the resolved value and the PEM
	// live only in secrets.age, never here. It is fully static: no Output to await.
	names := sortedKeys(material.Env)
	fileKeys := sortedKeys(material.Files)
	descriptor, err := renderDescriptor(svc, host, names, material.DescriptorFiles, env, region, slug, baseDomain, computeKey, meshEgressPort)
	if err != nil {
		return err
	}

	// One flat await over both halves, env values first, then file PEMs — the same
	// order blobFrom splits them back out in.
	valueOuts := make([]any, 0, len(names)+len(fileKeys))
	for _, n := range names {
		valueOuts = append(valueOuts, material.Env[n])
	}
	for _, k := range fileKeys {
		valueOuts = append(valueOuts, material.Files[k])
	}

	// The Triggers hash depends only on the resolved plaintext, never the host
	// public key — so it stays stable across a host key rotation and only
	// changes when a secret value actually moves.
	hashOut := pulumi.All(valueOuts...).ApplyT(func(args []any) (string, error) {
		blob, err := blobFrom(names, fileKeys, args)
		if err != nil {
			return "", fmt.Errorf("service %q: %w", svc.Name, err)
		}
		hash, err := blob.Hash()
		if err != nil {
			return "", fmt.Errorf("service %q: hash secrets blob: %w", svc.Name, err)
		}
		return hash, nil
	}).(pulumi.StringOutput)

	writeArgs := append([]any{hostKey.Stdout}, valueOuts...)
	writeScript := pulumi.All(writeArgs...).ApplyT(func(args []any) (string, error) {
		pub, _ := args[0].(string)
		if pub == "" {
			return "", fmt.Errorf("service %q: empty host public key while building secrets.age", svc.Name)
		}
		blob, err := blobFrom(names, fileKeys, args[1:])
		if err != nil {
			return "", fmt.Errorf("service %q: %w", svc.Name, err)
		}
		ct, err := hostsecrets.EncryptBlob(blob, pub)
		if err != nil {
			return "", fmt.Errorf("service %q: encrypt secrets blob: %w", svc.Name, err)
		}
		return iremote.WriteFileScript(service.DescriptorPath(svc.Name), descriptor) + "\n" +
			iremote.WriteFileScriptMode(service.SecretsPath(svc.Name), string(ct), "0600") + "\n" +
			reloadOrRestartScript(svc), nil
	}).(pulumi.StringOutput)

	deleteScript := iremote.DeleteFileScript(service.DescriptorPath(svc.Name)) + "\n" +
		iremote.DeleteFileScript(service.SecretsPath(svc.Name))

	if _, err := remote.NewCommand(ctx, name+"-secrets", &remote.CommandArgs{
		Connection: conn,
		Create:     writeScript,
		Update:     writeScript,
		Delete:     pulumi.String(deleteScript),
		// The static descriptor content triggers a rewrite on its own change (e.g.
		// a host/mesh-port change with no secret change); hashOut is the
		// ADR-0035 plaintext-hash trigger for a secret value change. Either
		// changing re-runs Create/Update, which re-issues reloadOrRestartScript.
		Triggers: pulumi.Array{pulumi.String(descriptor), safeTrigger(hashOut)},
		// A Triggers change replaces the resource; with the engine's default
		// create-before-delete the OLD resource's Delete script (recorded in
		// state) would run AFTER the new Create and remove the freshly written
		// files — including across the secrets↔descriptor shape flip, which
		// reuses this URN. Delete-before-replace makes the old files go first
		// and the new write land last.
	}, pulumi.DependsOn([]pulumi.Resource{hostKey}), pulumi.DeleteBeforeReplace(true)); err != nil {
		return fmt.Errorf("service %q: write descriptor/secrets: %w", svc.Name, err)
	}
	return nil
}

// blobFrom resolves the pulumi.All args into the plaintext hostsecrets.Blob the
// host receives. args is the flat await deliverServiceSecrets built: the env
// values in `names` order, then the file PEMs in `fileKeys` order — so the split
// point is len(names) and the two slices must be the SAME ones used to build it.
//
// Every value must be non-empty: a missing or empty resolved secret must never
// reach the host, and an empty PEM would project a zero-byte file the service
// would fail to parse at startup.
func blobFrom(names, fileKeys []string, args []any) (hostsecrets.Blob, error) {
	if len(args) != len(names)+len(fileKeys) {
		return hostsecrets.Blob{}, fmt.Errorf("resolved %d values for %d secrets and %d files while building secrets.age", len(args), len(names), len(fileKeys))
	}
	blob := hostsecrets.Blob{}
	if len(names) > 0 {
		blob.Env = make(map[string]string, len(names))
	}
	for i, n := range names {
		v, _ := args[i].(string)
		if v == "" {
			return hostsecrets.Blob{}, fmt.Errorf("empty resolved value for secret %q while building secrets.age", n)
		}
		blob.Env[n] = v
	}
	if len(fileKeys) > 0 {
		blob.Files = make(map[string]string, len(fileKeys))
	}
	for i, k := range fileKeys {
		v, _ := args[len(names)+i].(string)
		if v == "" {
			return hostsecrets.Blob{}, fmt.Errorf("empty resolved PEM for file %q while building secrets.age", k)
		}
		blob.Files[k] = v
	}
	return blob, nil
}

// safeTrigger turns a possibly-secret, possibly-unknown string output into a
// change-detector safe to use as a remote.Command `Triggers` element: it
// SHA-256-hashes the resolved value and strips the secret marker.
//
// A secret value must NEVER go directly into `Triggers`. During `preview` a
// secret wrapping an UNKNOWN value (e.g. a hash derived from a grant's
// `random.RandomPassword`, which has no value yet) marshals to the Pulumi engine
// error `malformed RPC secret: missing value for "triggers"`, aborting the whole
// preview; and even a secret+known trigger would persist the payload in state.
// Hashing first means the element never carries the secret bytes, and the hash is
// one-way so exposing it leaks nothing; `pulumi.Unsecret` clears the taint the
// input's secretness propagated so the trigger marshals as a plain (possibly
// unknown) value.
func safeTrigger(o pulumi.StringOutput) pulumi.Output {
	return pulumi.Unsecret(o.ApplyT(func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	}))
}

// reloadOrRestartScript renders the systemctl step that applies a changed
// secrets.age (or descriptor) to the running unit: reload (no downtime) when
// the service declares reload:, else restart. It is best-effort (|| true): on
// a service's FIRST deploy the unit is enabled but not yet started (its
// ExecStart payload does not exist until `inforge release` delivers it, see
// provisionService), so this step's first run would otherwise fail and abort
// the whole `pulumi up` — a conservative, deliberately narrower guarantee than
// the ADR's "always restart on change" until a later slice tightens it (e.g.
// by only restarting a unit already known to be active).
func reloadOrRestartScript(svc types.ServiceSpec) string {
	unit := iremote.Quote(service.UnitName(svc.Name))
	cmd := "restart"
	if svc.Reload != "" {
		cmd = "reload"
	}
	return fmt.Sprintf("sudo systemctl %s %s 2>/dev/null || true", cmd, unit)
}

// readHostPubKey registers the remote command that reads a host's SSH public
// key. It is world-readable, so no sudo is needed; its Stdout (with a trailing
// newline agehost.Encrypt trims) is the age recipient secrets.age is
// encrypted to. It is the first per-host SSH command in a delivery path, so it
// waits on the cloud-init gate; the secrets write chains off it transitively.
func readHostPubKey(ctx *pulumi.Context, conn remote.ConnectionArgs, name string, gate pulumi.Resource) (*remote.Command, error) {
	readHostKey := "cat " + hostpaths.SSHHostPubKeyPath
	return remote.NewCommand(ctx, name, &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(readHostKey),
		Update:     pulumi.String(readHostKey),
	}, pulumi.DependsOn([]pulumi.Resource{gate}))
}

// writeHostFile registers the shared write/delete remote.Command shape used
// by every "deliver a single static file to a host" path (a service's
// descriptor.yaml, a mesh host's mesh-descriptor.yaml, …): write on
// create/update, delete on destroy, retrigger on content change, gated on the
// cloud-init gate, and delete-before-replace so a Triggers-driven replacement
// never lets the old Delete script remove the freshly written file after the
// new Create runs (the two logical steps of a "replace" would otherwise race
// in the wrong order). nameSuffix distinguishes the resource name from
// sibling commands against the same host (e.g. "-secrets", "-agent").
func writeHostFile(ctx *pulumi.Context, name, nameSuffix string, conn remote.ConnectionArgs, path, content string, gate pulumi.Resource) (*remote.Command, error) {
	writeScript := iremote.WriteFileScript(path, content)
	deleteScript := iremote.DeleteFileScript(path)
	return remote.NewCommand(ctx, name+nameSuffix, &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(writeScript),
		Update:     pulumi.String(writeScript),
		Delete:     pulumi.String(deleteScript),
		Triggers:   pulumi.Array{pulumi.String(writeScript)},
	}, pulumi.DependsOn([]pulumi.Resource{gate}), pulumi.DeleteBeforeReplace(true))
}

// deliverServiceDescriptor writes a secret-less service's descriptor.yaml (0644)
// onto its host: a single static command, with no env and no secrets.age. The
// descriptor is fully known at plan time, so it needs no ApplyT. Connection
// details and the preview/up guards mirror provisionService.
func deliverServiceDescriptor(ctx *pulumi.Context, svc types.ServiceSpec, host types.ComputeOutputs, deployUser, deployPrivateKey, env, region, slug, baseDomain, computeKey string, meshEgressPort int, gate pulumi.Resource) error {
	conn := iremote.Connection(host.PublicIP, deployUser, deployPrivateKey)
	name := naming.Resource(env, slug, "svc", svc.Name)

	descriptor, err := renderDescriptor(svc, host, nil, nil, env, region, slug, baseDomain, computeKey, meshEgressPort)
	if err != nil {
		return err
	}
	// Same URN as deliverServiceSecrets' command (the two are the same
	// logical resource in different shapes); writeHostFile's delete-before-
	// replace keeps a shape flip or trigger change from letting the old
	// Delete script remove the freshly written descriptor (see
	// deliverServiceSecrets).
	if _, err := writeHostFile(ctx, name, "-secrets", conn, service.DescriptorPath(svc.Name), descriptor, gate); err != nil {
		return fmt.Errorf("service %q: write descriptor: %w", svc.Name, err)
	}
	return nil
}

// renderDescriptor marshals the on-host agent descriptor for a service.
// It builds the agent's own Descriptor struct (imported, not duplicated)
// so the producer can never drift from the consumer's schema. secretNames is
// the sorted set of env-var names the service expects (nil for a secret-less
// service) — an identity map into d.Env; the resolved VALUES live only in the
// host's secrets.age, decrypted at boot (ADR-0035), never here. grantFiles is the
// pki-grant file map (env-var -> blob file key, nil for a service with no pki
// grant), merged into d.Files beside any mtls_files: material — it likewise names
// only keys, never PEM content. The deployment block (region/env/domain/fqdn/host)
// is derived from the deployment context and is present for every service,
// secret-bearing or not. hostKey is the service's resolved compute key
// ("<name>-<NN>", e.g. "bridge-01"); the host id is its full VM resource name.
func renderDescriptor(svc types.ServiceSpec, host types.ComputeOutputs, secretNames []string, grantFiles map[string]string, env, region, slug, baseDomain, hostKey string, meshEgressPort int) (string, error) {
	// The mesh scope (ADR-0032) is the region name, or the literal ScopeGlobal for
	// the global slice — captured BEFORE region is blanked below, since the mesh
	// identity/SNI segment is "global", not empty.
	meshScope := region
	// The global scope is region-less: globalScope is an internal output-map key, not
	// an abstract region, so it must not leak into the on-host descriptor. Surface an
	// empty INFORGE_DEPLOYMENT_REGION (matching the already-empty RegionSlug) rather
	// than the literal "global", which a consumer could mistake for a real region.
	if region == globalScope {
		region = ""
	}
	d := agent.Descriptor{
		Version: agent.SupportedVersion,
		Service: svc.Name,
		Exec:    service.ExecPath(svc.Name),
		User:    svc.User,
		Deployment: agent.Deployment{
			Region:      region,
			RegionSlug:  slug,
			Environment: env,
			BaseDomain:  baseDomain,
			FQDN:        naming.ServiceFQDN(env, slug, svc.Name, baseDomain),
			// Full VM resource name "wardnet-<env>-<slug>-vm-<name>-<NN>" — passing the
			// "<name>-<NN>" hostKey as the name segment yields the same string as
			// naming.ResourceInstance, so the host id matches the cloud server name.
			HostID: naming.Resource(env, slug, "vm", hostKey),
			// Provider-supplied cloud/host resource identity, off the host's own
			// outputs (ADR-0030) — empty for a provider that does not supply them.
			CloudProvider:    host.CloudProvider,
			CloudRegion:      host.CloudRegion,
			AvailabilityZone: host.AvailabilityZone,
			MachineType:      host.MachineType,
		},
	}
	if len(secretNames) > 0 {
		d.Env = make(map[string]string, len(secretNames))
		for _, n := range secretNames {
			d.Env[n] = n
		}
	}
	// Only an mtls_files: opted-in service (a raw mTLS plane outside the mesh,
	// e.g. tunneller node↔node) still receives its own leaf/key/CA-bundle:
	// `inforge pki renew` keeps writing them under a persistent leaf.age and
	// files: advertises them for boot projection (#109). Every other mesh
	// member holds no cert material — the mesh proxy is the sole leaf custodian
	// (ADR-0033). This no longer depends on whether the service also has
	// env/grant secrets (there is no more "bundle" gate).
	if svc.MtlsFiles {
		d.Files = meshcert.DescriptorFiles()
	}
	// A pki/* grant's material rides the same files: projection (ADR-0025 slice C),
	// delivered in the deploy-owned secrets.age rather than the renew-owned
	// leaf.age. The two key namespaces are disjoint by construction ("mtls/" vs
	// grant.FileKey's "pki/"), and validate.checkGrants rejects a grant output that
	// collides with a meshcert.DescriptorFiles() env name — so the merge can
	// neither overwrite mesh material nor be overwritten by it.
	if len(grantFiles) > 0 {
		if d.Files == nil {
			d.Files = make(map[string]string, len(grantFiles))
		}
		maps.Copy(d.Files, grantFiles)
	}
	// A mesh member (any pki: service) gets the east-west endpoint contract
	// (ADR-0032), independent of whether it has a secrets provider: INFORGE_MESH_URL
	// points at its loopback egress endpoint, INFORGE_MESH_SCOPE carries its mesh
	// scope, and INFORGE_MESH_PORT (a callee's inbound port) is set only when the
	// service declares a mesh: block. The egress port is the same one the mesh proxy
	// binds for this service (meshEgressPortsByService).
	if svc.Pki != "" {
		m := &agent.Mesh{
			URL:   fmt.Sprintf("http://127.0.0.1:%d", meshEgressPort),
			Scope: meshScope,
		}
		if svc.Mesh != nil {
			m.Port = svc.Mesh.Port
		}
		d.Mesh = m
	}
	b, err := yaml.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("marshal descriptor for service %q: %w", svc.Name, err)
	}
	return string(b), nil
}

// serviceProvisionScript renders the host shell that downloads inforge-agent,
// writes a service's unit + folder (+ no-login user), reloads systemd, and
// ENABLES the unit. It must never emit a start/restart: the agent's target
// binary (<folder>/run) does not exist until release delivers code, so a start
// would fail the deploy. All caller-supplied values interpolated into the shell
// are quoted.
func serviceProvisionScript(svc types.ServiceSpec, inforgeVersion string) string {
	steps := []string{
		"set -euo pipefail",
		agentDownloadStep(inforgeVersion),
	}
	if svc.User != "" {
		steps = append(steps, fmt.Sprintf(
			"sudo useradd --system --shell /usr/sbin/nologin %s 2>/dev/null || true", iremote.Quote(svc.User)))
	}
	steps = append(steps,
		// Service folder: root-owned, world-readable. The svc user gets r-x (no
		// write); release extracts the payload into it as root.
		fmt.Sprintf("sudo install -d -m 0755 %s", iremote.Quote(service.Folder(svc.Name))),
		iremote.WriteFileScript(service.UnitPath(svc.Name), service.Unit(svc)),
		"sudo systemctl daemon-reload",
		fmt.Sprintf("sudo systemctl enable %s", iremote.Quote(service.UnitName(svc.Name))),
	)
	// An mtls_files: service's leaf.age is delivered later by `inforge pki
	// renew`'s SSH push, which also signals reload-or-restart directly — there
	// is no on-host renewal timer to enable (ADR-0035 removed it).
	return strings.Join(steps, "\n")
}

// agentDownloadStep renders the idempotent shell that downloads the
// inforge-agent raw release binary onto the host, verifies its checksum, and
// installs it at service.AgentBin. The host arch is detected on the host
// (uname -m → Go arch), the version is pinned to the deploying inforge build, and
// the goreleaser raw-asset name scheme is mirrored (inforge-agent_<ver>_linux_<arch>,
// under the v<ver> release tag). The binary's sha256 is verified against the
// release checksums.txt before it is installed as the root ExecStart for every
// service — a tampered or truncated download must never run. curl -fsSL fails the
// deploy clearly on a missing asset; a trap removes the temp files on any exit.
// The version is single-quoted into a shell var so it is injection-safe while
// still composing with the shell-side ${arch} expansion.
func agentDownloadStep(inforgeVersion string) string {
	return strings.Join([]string{
		"ver=" + iremote.Quote(inforgeVersion),
		hostpaths.ArchDetectShell,
		"asset=\"inforge-agent_${ver}_linux_${arch}\"",
		"base=\"https://github.com/wardnet/inforge/releases/download/v${ver}\"",
		"tmp=$(mktemp)",
		"sums=$(mktemp)",
		"trap 'rm -f \"$tmp\" \"$sums\"' EXIT",
		"curl -fsSL \"${base}/${asset}\" -o \"$tmp\"",
		"curl -fsSL \"${base}/checksums.txt\" -o \"$sums\"",
		// Pull the expected sha256 for exactly this asset from the release
		// checksums; an absent line (empty want) is a hard failure.
		"want=$(awk -v f=\"$asset\" '$2==f {print $1}' \"$sums\")",
		"[ -n \"$want\" ] || { echo \"no checksum for $asset in release\" >&2; exit 1; }",
		"got=$(sha256sum \"$tmp\" | awk '{print $1}')",
		"[ \"$want\" = \"$got\" ] || { echo \"checksum mismatch for $asset\" >&2; exit 1; }",
		fmt.Sprintf("sudo install -m 0755 \"$tmp\" %s", iremote.Quote(service.AgentBin)),
	}, "\n")
}

// serviceDeprovisionScript renders the host shell run when a service resource is
// deleted: stop+disable and remove the unit (best-effort). The folder is left
// intact — it may hold delivered payload/data.
func serviceDeprovisionScript(svc types.ServiceSpec) string {
	return strings.Join([]string{
		fmt.Sprintf("sudo systemctl disable --now %s 2>/dev/null || true", iremote.Quote(service.UnitName(svc.Name))),
		fmt.Sprintf("sudo rm -f %s", iremote.Quote(service.UnitPath(svc.Name))),
		"sudo systemctl daemon-reload",
	}, "\n")
}

// realizeIngress installs and configures the nginx proxy on every ingress host —
// the compute an ingress resource references — that fronts at least one service
// route. Realization is driven by the ingress tier (ADR-0026): a service names an
// ingress via its Ingress FK and contributes its Routes; ingressRoutesByHost
// groups those routes under the ingress's host (merging several ingresses that
// share a host). nginx moves OFF the service host onto the ingress host; it
// proxies to the backend over loopback (co-located) or the private network
// (cross-host, using the backend's PrivateIP). FQDNs are env-scoped here so the
// provider stays a pure installer. Hosts are realized in sorted order so the
// resource graph is stable across runs.
func realizeIngress(ctx *pulumi.Context, reg registry.ProviderRegistry, res types.Resources, computeOut map[string]types.ComputeOutputs, gates map[string]pulumi.Resource, appSeeds map[string][]pulumi.Resource, deployPrivateKey, env, slug, baseDomain, ephemeralSlug string, defaults types.ProviderDefaults) error {
	canonical := naming.CanonicalComputeKeys(res.Compute)
	routesByHostKey, _, err := ingressRoutesByHost(res, canonical, env, slug, baseDomain)
	if err != nil {
		return fmt.Errorf("ingress: %w", err)
	}
	appsByHostKey := ingressAppsByHost(res, canonical, slug, baseDomain, ephemeralSlug)
	gatewaysByHostKey := gatewaysByHost(res, canonical, slug, baseDomain, ephemeralSlug)
	// Resolve the ingress-tier services once and feed both derivations below — the
	// health entries and the cross-host backend set — so the service list is walked a
	// single time per host realization.
	ingressSvcs := resolveIngressServices(res, canonical)
	healthByHostKey := ingressHealthByHost(ingressSvcs, env, slug, baseDomain)
	healthPortByHostKey := ingressHealthPortByHost(res, canonical)
	// A backend's private IP is needed whenever a service's route OR health endpoint
	// is cross-host, so resolve cross-host backends once over the whole ingress tier
	// (routes and health-only services alike) rather than from routes only.
	crossHost := ingressCrossHostBackends(ingressSvcs)
	// The gateway health tier (ADR-0034): a gateway-listed service without an
	// ingress FK surfaces its health on the GATEWAY's host — same shared listener,
	// same Host demux, same cross-host private-IP substitution. Merged into the
	// per-host maps so the provider renders one health set per host. Validation
	// (D13) requires a host shared by a gateway and an ingress to declare ONE
	// effective health port; deploy can run unvalidated, so a mismatch here is a
	// hard error — silently keeping the ingress's port would render the listener
	// on one port while the firewall opens the gateway's (dead health, no signal).
	gwHealthSvcs := resolveGatewayHealthServices(res, canonical)
	for gwHost, hs := range gatewayHealthByHost(gwHealthSvcs, env, slug, baseDomain) {
		healthByHostKey[gwHost] = append(healthByHostKey[gwHost], hs...)
		sort.Slice(healthByHostKey[gwHost], func(i, j int) bool {
			return healthByHostKey[gwHost][i].FQDN < healthByHostKey[gwHost][j].FQDN
		})
	}
	for _, gs := range gwHealthSvcs {
		if existing, ok := healthPortByHostKey[gs.gwHost]; ok && existing != gs.gwHealthPort {
			return fmt.Errorf("ingress: host %q renders one health listener but the co-hosted ingress declares health port %d while the gateway declares %d; the two must match", gs.gwHost, existing, gs.gwHealthPort)
		}
		healthPortByHostKey[gs.gwHost] = gs.gwHealthPort
		if !gs.coLocated {
			if crossHost[gs.gwHost] == nil {
				crossHost[gs.gwHost] = map[string]string{}
			}
			crossHost[gs.gwHost][gs.svc.Name] = gs.svcHost
		}
	}
	// An app always serves on :443, so its FQDN must not collide with a :443
	// tls-termination route's SNI (or another app) on the same host — nginx cannot
	// demux two server blocks with one (listen, server_name) and would race two ACME
	// orders for the hostname. The route-vs-route half of this rule lives in
	// ingressRoutesByHost; this closes the app-vs-route/app-vs-app half so the
	// guarantee holds even when `up` runs without a prior `validate`.
	if err := checkAppSNICollisions(routesByHostKey, appsByHostKey, gatewaysByHostKey); err != nil {
		return fmt.Errorf("ingress: %w", err)
	}
	// nginx is installed on an ingress host iff at least one route, app, health
	// endpoint, OR gateway targets it; an app-only, health-only, or gateway-only
	// host still realizes — its server blocks and ACME certs provision alone.
	hostKeys := ingressHostUnion(routesByHostKey, appsByHostKey, healthByHostKey, gatewaysByHostKey)
	if len(hostKeys) == 0 {
		return nil
	}

	deployUserByCompute := naming.DeployUsersByHost(res.Compute)
	providerByCompute := ingressProvidersByHost(res.Compute, defaults)

	for _, hostKey := range hostKeys {
		host, ok := computeOut[hostKey]
		if !ok {
			return fmt.Errorf("ingress: host %q has no compute output (available: %v)", hostKey, sortedKeys(computeOut))
		}
		ip, err := reg.Ingress(providerByCompute[hostKey])
		if err != nil {
			return fmt.Errorf("ingress: host %q: %w", hostKey, err)
		}
		// Wire each cross-host route's backend private IP from its backend host's
		// compute output; co-located routes already carry Backend "127.0.0.1".
		backendIPs := map[string]pulumi.StringOutput{}
		for svcName, backendHostKey := range crossHost[hostKey] {
			backend, ok := computeOut[backendHostKey]
			if !ok {
				return fmt.Errorf("ingress: host %q: service %q backend host %q has no compute output (available: %v)", hostKey, svcName, backendHostKey, sortedKeys(computeOut))
			}
			backendIPs[svcName] = backend.PrivateIP
		}
		// The realization SSHes the ingress host, so it waits on that host's
		// cloud-init gate (shared with service provisioning when co-located).
		gate, err := cloudInitGate(ctx, gates, hostKey, host, deployPrivateKey, env, slug)
		if err != nil {
			return err
		}
		// The reload must wait on this host's app placeholder seeds too, so an
		// app-only or mixed ingress never reloads into a server block whose
		// `current` document root has not been seeded yet.
		deps := append([]pulumi.Resource{gate}, appSeeds[hostKey]...)
		healthPort := healthPortByHostKey[hostKey]
		if healthPort == 0 {
			healthPort = types.DefaultHealthProbesPort
		}
		if err := ip.Realize(ctx, hostKey, host, deployUserByCompute[hostKey], routesByHostKey[hostKey], appsByHostKey[hostKey], healthByHostKey[hostKey], healthPort, gatewaysByHostKey[hostKey], backendIPs, env, deps); err != nil {
			return err
		}
	}
	return nil
}

// ingressHostsByName maps each ingress resource name to the canonical specKey of
// its host. Validation guarantees the host FK resolves; an unresolved one is
// skipped defensively (the caller then finds no routes for it).
func ingressHostsByName(res types.Resources, canonical map[string]string) map[string]string {
	byName := map[string]string{}
	for _, ing := range res.Ingress {
		if hk, ok := canonical[ing.Host]; ok {
			byName[ing.Name] = hk
		}
	}
	return byName
}

// ingressProvidersByHost maps each expanded compute specKey to the provider that
// realizes its ingress proxy — the host's own compute provider (services have no
// provider; the proxy runs on the host).
func ingressProvidersByHost(computes []types.ComputeSpec, defaults types.ProviderDefaults) map[string]string {
	byHost := map[string]string{}
	for _, c := range computes {
		provider := types.ResolveProvider(c.Provider, "compute", "", defaults)
		for i := 1; i <= c.InstanceCount; i++ {
			byHost[naming.SpecKey(c.Name, i)] = provider
		}
	}
	return byHost
}

// ingressFQDNs returns the env-scoped FQDNs one route serves: the auto-derived
// "<svc>.svc" FQDN plus every expanded vanity entry. (A forward route carries no
// vanity, so it yields just the "<svc>.svc" name — a reachable A-record even though
// nginx stream demands no server_name.) This is the single source of truth for a
// service's ingress FQDNs — both ingressRoutesByHost (the tls-termination
// server_names / ACME certs) and derivedRecords (the A-records) call it, so a cert
// FQDN and its A-record can never drift apart.
func ingressFQDNs(svcName string, rt types.RouteSpec, env, slug, baseDomain string) []string {
	fqdns := []string{naming.ServiceFQDN(env, slug, svcName, baseDomain)}
	for _, v := range rt.Vanity {
		fqdns = append(fqdns, naming.ExpandVanity(v, env, slug, baseDomain))
	}
	return fqdns
}

// ingressService is one service resolved to the hosts that matter for the ingress
// tier: the canonical specKey of its ingress's host (ingHost), of its own backend
// host (svcHost), and whether the two are the same (coLocated). It is produced once
// by resolveIngressServices and consumed by every derivation below.
type ingressService struct {
	svc       types.ServiceSpec
	ingHost   string
	svcHost   string
	coLocated bool
}

// resolveIngressServices derives, once, the (service, ingress host, backend host,
// co-located) tuples for every service that exposes routes through a resolvable
// ingress. The firewall plan, the realized nginx routes, and the derived DNS
// records all consume it, so the three cannot drift on the skip guard, the FK
// resolution, or the co-location test (which previously lived as three copies). A
// service with no routes, no ingress, or an unresolvable ingress/host FK is
// skipped — validation guarantees resolution, so the skip is purely defensive.
func resolveIngressServices(res types.Resources, canonical map[string]string) []ingressService {
	ingressHost := ingressHostsByName(res, canonical)
	out := make([]ingressService, 0, len(res.Service))
	for _, svc := range res.Service {
		// A service is part of the ingress tier when it exposes routes OR a health
		// endpoint through its ingress; a health-only service (no routes) still needs
		// its backend resolved and its health server rendered.
		if svc.Ingress == "" || (len(svc.Routes) == 0 && svc.HealthProbesPort == 0) {
			continue
		}
		ingHost, ok := ingressHost[svc.Ingress]
		if !ok {
			continue
		}
		svcHost, ok := canonical[svc.Host]
		if !ok {
			continue
		}
		out = append(out, ingressService{svc: svc, ingHost: ingHost, svcHost: svcHost, coLocated: svcHost == ingHost})
	}
	return out
}

// ingressApp is one app resolved to its ingress's host (ADR-0026, slice C): the
// canonical specKey of the compute its ingress references. It is produced once by
// resolveIngressApps and consumed by the firewall plan, the realized nginx app
// servers, and the derived app DNS records — so the three cannot drift on the skip
// guard or the FK resolution. An app with no ingress, or an unresolvable
// ingress/host FK, is skipped — validation guarantees resolution, so the skip is
// purely defensive.
type ingressApp struct {
	app     types.AppSpec
	ingHost string
}

// resolveIngressApps derives, once, the (app, ingress host) tuples for every app
// served through a resolvable ingress.
func resolveIngressApps(res types.Resources, canonical map[string]string) []ingressApp {
	ingressHost := ingressHostsByName(res, canonical)
	out := make([]ingressApp, 0, len(res.App))
	for _, a := range res.App {
		if a.Ingress == "" {
			continue
		}
		ingHost, ok := ingressHost[a.Ingress]
		if !ok {
			continue
		}
		out = append(out, ingressApp{app: a, ingHost: ingHost})
	}
	return out
}

// ingressAppsByHost groups the typed nginx app servers by the canonical specKey of
// their ingress's host. Each IngressApp carries its fully-resolved FQDN (the clean
// dotted app form), document root (the on-host `current` symlink), and SPA flag, so
// the provider stays a pure renderer. A non-empty result for a host is, together
// with ingressRoutesByHost, a realization trigger — nginx is installed on an
// ingress host iff at least one route or app targets it.
func ingressAppsByHost(res types.Resources, canonical map[string]string, slug, baseDomain, ephemeralSlug string) map[string][]types.IngressApp {
	byHost := map[string][]types.IngressApp{}
	for _, ia := range resolveIngressApps(res, canonical) {
		byHost[ia.ingHost] = append(byHost[ia.ingHost], types.IngressApp{
			Name: ia.app.Name,
			FQDN: naming.AppFQDN(ia.app.Subdomain, slug, baseDomain, ephemeralSlug),
			Root: app.CurrentPath(ia.app.Name),
			Spa:  ia.app.Spa,
		})
	}
	for _, apps := range byHost {
		sort.Slice(apps, func(i, j int) bool { return apps[i].FQDN < apps[j].FQDN })
	}
	return byHost
}

// resolvedGateway is one gateway with its canonical host and fully-resolved
// public FQDN — the single derivation the nginx, DNS, and firewall passes all
// consume so the three can never drift (the gateway analogue of resolveIngressApps;
// rule mesh-host-grouping-is-single-sourced names gateway realization a consumer).
type resolvedGateway struct {
	gw   types.GatewaySpec
	host string // canonical compute specKey
	fqdn string // naming.AppFQDN(subdomain, …) — server_name, ACME cert, and DNS record
}

// resolveGateways resolves each authored gateway to its canonical host and public
// FQDN once. An unresolved host FK is skipped (validation rejects it long before
// this), so every consumer sees the same host/FQDN pair.
func resolveGateways(res types.Resources, canonical map[string]string, slug, baseDomain, ephemeralSlug string) []resolvedGateway {
	out := make([]resolvedGateway, 0, len(res.Gateway))
	for _, gw := range res.Gateway {
		host, ok := canonical[gw.Host]
		if !ok {
			continue
		}
		out = append(out, resolvedGateway{
			gw:   gw,
			host: host,
			fqdn: naming.AppFQDN(gw.Subdomain, slug, baseDomain, ephemeralSlug),
		})
	}
	return out
}

// gatewaysByHost groups the scope's north-south gateway (a scope singleton) under
// its canonical host as the derived types.IngressGateway the public nginx renders
// (ADR-0032/0034), via the shared resolveGateways derivation. The routing table
// is DERIVED, not authored: one route per (listed service, public path glob),
// carrying the raw pattern and the owning service name (the X-Mesh-Target
// value); the mesh resolves the target's location, so no backend is resolved here.
func gatewaysByHost(res types.Resources, canonical map[string]string, slug, baseDomain, ephemeralSlug string) map[string][]types.IngressGateway {
	byHost := map[string][]types.IngressGateway{}
	for _, rg := range resolveGateways(res, canonical, slug, baseDomain, ephemeralSlug) {
		byHost[rg.host] = append(byHost[rg.host], types.IngressGateway{
			Name:             rg.gw.Name,
			FQDN:             rg.fqdn,
			Routes:           toGatewayNginxRoutes(rg.gw.Services, res.Service),
			HealthProbePaths: append([]string(nil), rg.gw.HealthProbePaths...),
		})
	}
	return byHost
}

// toGatewayNginxRoutes derives the gateway's provider-facing nginx routes from
// its listed services' mesh.public_paths (ADR-0034): one route per (service,
// glob). Validation guarantees every listed service resolves, declares >=1
// public path, and that patterns are pairwise non-overlapping across services —
// so the derived table has exactly one owner per request path.
func toGatewayNginxRoutes(serviceNames []string, svcs []types.ServiceSpec) []types.IngressGatewayRoute {
	byName := servicesByName(svcs)
	var out []types.IngressGatewayRoute
	for _, name := range serviceNames {
		svc, ok := byName[name]
		if !ok || svc.Mesh == nil {
			continue // validation rejects this long before deploy
		}
		for _, pattern := range svc.Mesh.PublicPaths {
			out = append(out, types.IngressGatewayRoute{Pattern: pattern, Service: name})
		}
	}
	return out
}

// ingressHealthByHost groups service health endpoints by the canonical specKey of
// their ingress's host. Each IngressHealth carries the service's canonical FQDN (the
// strict server_name / Host the ingress demuxes on), the backend health port, and a
// resolved Backend ("127.0.0.1" co-located; left empty for the provider to fill with
// the backend's private IP cross-host — exactly like a route). A non-empty result for
// a host is, with routes and apps, a realization trigger.
func ingressHealthByHost(svcs []ingressService, env, slug, baseDomain string) map[string][]types.IngressHealth {
	byHost := map[string][]types.IngressHealth{}
	for _, is := range svcs {
		if is.svc.HealthProbesPort == 0 {
			continue
		}
		byHost[is.ingHost] = append(byHost[is.ingHost], healthEntry(is.svc, is.coLocated, env, slug, baseDomain))
	}
	for _, hs := range byHost {
		sort.Slice(hs, func(i, j int) bool { return hs[i].FQDN < hs[j].FQDN })
	}
	return byHost
}

// gatewayHealthService is one service whose health endpoint is surfaced through
// the north-south gateway's public health port (ADR-0034): a gateway-listed
// service with a backend health port and NO ingress FK (a service that also
// names an ingress keeps its health at the ingress — one canonical health
// address per service, or the ServiceFQDN A record would derive at two hosts).
type gatewayHealthService struct {
	svc          types.ServiceSpec
	gwHost       string // canonical specKey of the gateway's host (where the health server renders)
	svcHost      string // canonical specKey of the service's own host
	coLocated    bool
	gwHealthPort int // the gateway's effective public health port — carried here so nginx, firewall, and DNS read ONE derivation
}

// resolveGatewayHealthServices derives, once, the gateway health tier: the
// single source the nginx health entries, the firewall plan, and the derived
// DNS records all consume (the derivedRecords discipline — three consumers, one
// resolver, no drift). Deduped per service across gateways (the gateway is a
// scope singleton, so duplicates only arise from a malformed spec validation
// already rejects).
func resolveGatewayHealthServices(res types.Resources, canonical map[string]string) []gatewayHealthService {
	svcByName := servicesByName(res.Service)
	seen := map[string]bool{}
	var out []gatewayHealthService
	for _, rg := range resolveGateways(res, canonical, "", "", "") {
		for _, name := range rg.gw.Services {
			svc, ok := svcByName[name]
			if !ok || svc.HealthProbesPort == 0 || svc.Ingress != "" || seen[name] {
				continue
			}
			svcHost, ok := canonical[svc.Host]
			if !ok {
				continue
			}
			seen[name] = true
			out = append(out, gatewayHealthService{
				svc:          svc,
				gwHost:       rg.host,
				svcHost:      svcHost,
				coLocated:    svcHost == rg.host,
				gwHealthPort: rg.gw.EffectiveHealthProbesPort(),
			})
		}
	}
	return out
}

// servicesByName indexes a service slice by name — the shared lookup both the
// gateway route derivation and the gateway health resolver build from.
func servicesByName(svcs []types.ServiceSpec) map[string]types.ServiceSpec {
	byName := make(map[string]types.ServiceSpec, len(svcs))
	for _, s := range svcs {
		byName[s.Name] = s
	}
	return byName
}

// healthEntry builds the one types.IngressHealth shape BOTH health tiers render
// (ingress-fronted and gateway-listed): service FQDN demux, backend health
// port, declared probe paths, and the co-located loopback backend (left empty
// cross-host for the provider to substitute the private IP). Single-sourced so
// a new IngressHealth field cannot be threaded into one tier and forgotten in
// the other.
func healthEntry(svc types.ServiceSpec, coLocated bool, env, slug, baseDomain string) types.IngressHealth {
	h := types.IngressHealth{
		Service: svc.Name,
		FQDN:    naming.ServiceFQDN(env, slug, svc.Name, baseDomain),
		Target:  svc.HealthProbesPort,
		Paths:   append([]string(nil), svc.HealthProbePaths...),
	}
	if coLocated {
		h.Backend = "127.0.0.1"
	}
	return h
}

// gatewayHealthByHost groups the gateway health tier under the gateway's host as
// the same types.IngressHealth entries the ingress tier renders (one shared
// health listener per host, Host-demuxed by service FQDN; Backend left empty
// cross-host for the provider to substitute the private IP, exactly like
// ingressHealthByHost).
func gatewayHealthByHost(gsvcs []gatewayHealthService, env, slug, baseDomain string) map[string][]types.IngressHealth {
	byHost := map[string][]types.IngressHealth{}
	for _, gs := range gsvcs {
		byHost[gs.gwHost] = append(byHost[gs.gwHost], healthEntry(gs.svc, gs.coLocated, env, slug, baseDomain))
	}
	for _, hs := range byHost {
		sort.Slice(hs, func(i, j int) bool { return hs[i].FQDN < hs[j].FQDN })
	}
	return byHost
}

// ingressHealthPortByHost maps each ingress host's canonical specKey to the public
// health port nginx exposes there (the ingress's HealthProbesPort, defaulting to 81).
func ingressHealthPortByHost(res types.Resources, canonical map[string]string) map[string]int {
	out := map[string]int{}
	for _, ing := range res.Ingress {
		hk, ok := canonical[ing.Host]
		if !ok {
			continue
		}
		out[hk] = ing.EffectiveHealthProbesPort()
	}
	return out
}

// ingressHealthPortByName maps each ingress resource name to its public health port,
// so the firewall can open the right port for a service that references it.
func ingressHealthPortByName(res types.Resources) map[string]int {
	out := map[string]int{}
	for _, ing := range res.Ingress {
		out[ing.Name] = ing.EffectiveHealthProbesPort()
	}
	return out
}

// ingressCrossHostBackends returns, per ingress host, the backend host specKey of
// every service whose backend is NOT co-located with the ingress — the services for
// which the provider must resolve a private IP (routes and health-only alike). A
// co-located service already carries Backend "127.0.0.1" and needs no entry.
func ingressCrossHostBackends(svcs []ingressService) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, is := range svcs {
		if is.coLocated {
			continue
		}
		if out[is.ingHost] == nil {
			out[is.ingHost] = map[string]string{}
		}
		out[is.ingHost][is.svc.Name] = is.svcHost
	}
	return out
}

// ingressHostUnion returns the sorted union of the three ingress-host maps' keys —
// the hosts that have routes, apps, and/or health endpoints — so realization visits
// each host exactly once in a stable order.
func ingressHostUnion(routes map[string][]types.IngressRoute, apps map[string][]types.IngressApp, health map[string][]types.IngressHealth, gateways map[string][]types.IngressGateway) []string {
	set := map[string]bool{}
	for k := range routes {
		set[k] = true
	}
	for k := range apps {
		set[k] = true
	}
	for k := range health {
		set[k] = true
	}
	for k := range gateways {
		set[k] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkAppSNICollisions rejects an app or gateway whose FQDN collides, on its
// host's :443 listener, with a tls-termination route's SNI, another app, or the
// gateway — a clash nginx cannot demux (two server blocks sharing one
// (listen, server_name)) that would also race two ACME orders for the same
// hostname. Apps and the gateway always serve on :443, so the conflict set per
// host is every :443 tls-termination route FQDN plus the app + gateway FQDNs.
// It mirrors the route-vs-route guard in ingressRoutesByHost for the app side.
func checkAppSNICollisions(routesByHost map[string][]types.IngressRoute, appsByHost map[string][]types.IngressApp, gatewaysByHost map[string][]types.IngressGateway) error {
	hosts := map[string]bool{}
	for k := range appsByHost {
		hosts[k] = true
	}
	for k := range gatewaysByHost {
		hosts[k] = true
	}
	for _, hostKey := range sortedKeys(hosts) {
		owner := map[string]string{} // fqdn -> "service X" | "app Y" | "gateway Z"
		for _, rt := range routesByHost[hostKey] {
			if rt.Type != types.IngressTypeTLSTermination || rt.Listen != 443 {
				continue
			}
			for _, fqdn := range rt.FQDNs {
				owner[fqdn] = "service " + rt.Service
			}
		}
		for _, a := range appsByHost[hostKey] {
			if prev, dup := owner[a.FQDN]; dup {
				return fmt.Errorf("ingress host %q: app %q FQDN %q collides with %s on listen 443; an SNI must be unique per listen port", hostKey, a.Name, a.FQDN, prev)
			}
			owner[a.FQDN] = "app " + a.Name
		}
		for _, g := range gatewaysByHost[hostKey] {
			if prev, dup := owner[g.FQDN]; dup {
				return fmt.Errorf("ingress host %q: gateway %q FQDN %q collides with %s on listen 443; an SNI must be unique per listen port", hostKey, g.Name, g.FQDN, prev)
			}
			owner[g.FQDN] = "gateway " + g.Name
		}
	}
	return nil
}

// firewallPlanByHost derives each host's inbound firewall port plan, keyed by
// canonical compute specKey (ADR-0026 ingress tier). It is purely static (which
// ingress fronts which service, co-located or not) so the firewall can be built
// before any output resolves:
//   - An ingress host opens, publicly, the Listen port of every route of every
//     service that references it, plus :80 when any of those routes is
//     tls-termination (nginx serves the ACME HTTP-01 challenge there).
//   - A backend host (a service whose host differs from its ingress's host) opens
//     each cross-host route's Target port privately, scoped to the host's network
//     CIDR — reachable only from a co-tenant ingress over the private network.
//
// A co-located route needs no rule: nginx reaches the backend over loopback, which
// the firewall never filters. SSH (22) is not included — the firewall always
// permits it. Lists are sorted and de-duplicated so the rendered firewall is stable.
// meshPublic selects how a mesh host's MTLSPort is scoped in firewallPlanByHost:
// false (a regional scope) opens it only to the host's private network CIDR;
// true (the global scope) opens it to the public internet — the global host IS
// the cross-scope mesh gateway a regional mesh dials over the internet (ADR-0032).
func firewallPlanByHost(res types.Resources, meshPublic bool) map[string]types.FirewallPorts {
	canonical := naming.CanonicalComputeKeys(res.Compute)
	netCIDR := networkCIDRByCompute(res, canonical)

	public := map[string]map[int]bool{}
	private := map[string]map[int]bool{}
	addPublic := func(host string, port int) {
		if public[host] == nil {
			public[host] = map[int]bool{}
		}
		public[host][port] = true
	}
	addPrivate := func(host string, port int) {
		if private[host] == nil {
			private[host] = map[int]bool{}
		}
		private[host][port] = true
	}
	// exposed holds each host's service exposed_ports (ADR-0029), proto-aware and
	// deduped. Like a cross-host backend target they are opened only to the host's
	// private CIDR — never the internet.
	exposed := map[string]map[types.ExposedPort]bool{}
	addExposed := func(host string, ep types.ExposedPort) {
		if exposed[host] == nil {
			exposed[host] = map[types.ExposedPort]bool{}
		}
		exposed[host][ep] = true
	}
	healthPortByName := ingressHealthPortByName(res)
	for _, is := range resolveIngressServices(res, canonical) {
		for _, rt := range is.svc.Routes {
			addPublic(is.ingHost, rt.Listen)
			if rt.Type == types.IngressTypeTLSTermination {
				addPublic(is.ingHost, 80) // ACME HTTP-01 on the ingress host
			}
			if !is.coLocated {
				addPrivate(is.svcHost, rt.Target) // backend target reachable over the private net
			}
		}
		// A health endpoint opens the ingress's public health port (default 81) on the
		// ingress host, and the backend health port privately on a cross-host backend.
		if is.svc.HealthProbesPort > 0 {
			addPublic(is.ingHost, healthPortByName[is.svc.Ingress])
			if !is.coLocated {
				addPrivate(is.svcHost, is.svc.HealthProbesPort)
			}
		}
	}
	// An app-serving ingress host opens 443 (HTTPS) and :80 (ACME HTTP-01 +
	// redirect). Apps have no backend, so no private rule is needed.
	for _, ia := range resolveIngressApps(res, canonical) {
		addPublic(ia.ingHost, 443)
		addPublic(ia.ingHost, 80)
	}
	// The north-south gateway host opens the same public pair: daemons HTTPS in
	// on 443, and :80 serves ACME HTTP-01 for the gateway cert. The gateway's
	// backends are reached THROUGH the mesh (its own loopback egress → the
	// callee's MTLSPort, already covered by the mesh rules) — no private rule.
	// Slug/baseDomain don't affect the host resolution the firewall needs, so the
	// empty-args resolveGateways call shares the FK resolution with nginx + DNS.
	for _, rg := range resolveGateways(res, canonical, "", "", "") {
		addPublic(rg.host, 443)
		addPublic(rg.host, 80)
	}
	// The gateway health tier (ADR-0034): the gateway host opens its public health
	// port when >=1 listed (ingress-less) service declares a backend health port,
	// and a cross-host backend opens that port privately to the network CIDR —
	// mirroring the ingress health rules. The port rides on the shared resolver
	// (gs.gwHealthPort) so this rule, the nginx listener, and the DNS record all
	// read one derivation.
	for _, gs := range resolveGatewayHealthServices(res, canonical) {
		addPublic(gs.gwHost, gs.gwHealthPort)
		if !gs.coLocated {
			addPrivate(gs.svcHost, gs.svc.HealthProbesPort)
		}
	}
	// exposed_ports are private binds on the service's own host, with no ingress
	// involvement — so they are read from every service directly (not via
	// resolveIngressServices, which skips ingress-less services). A private-only
	// service contributes only here.
	for _, svc := range res.Service {
		if len(svc.ExposedPorts) == 0 {
			continue
		}
		svcHost, ok := canonical[svc.Host]
		if !ok {
			continue
		}
		for _, ep := range svc.ExposedPorts {
			addExposed(svcHost, ep)
		}
	}
	// A self-hosted database-cluster host opens each co-located cluster's TCP port
	// (postgres.ClusterPort, 5432+) to its private network CIDR only — never the public
	// internet. Peers reach Postgres over the private network; the port assignment is
	// single-sourced with the realization via clusterPortsByHost (ADR-0036, rule
	// exposed-ports-are-private-only).
	for host, byCluster := range clusterPortsByHost(res, canonical) {
		for _, port := range byCluster {
			addPrivate(host, port)
		}
	}
	// The east-west mesh materializes on every host running ≥1 pki: service (ADR-0032);
	// that host's mesh proxy accepts peer mTLS on meshpaths.MTLSPort. A regional mesh
	// host opens it only to its private network (peers reach it over the private net);
	// the global host opens it publicly — it is the cross-scope mesh gateway a regional
	// mesh dials over the internet, which structurally keeps regional meshes private.
	for _, svc := range res.Service {
		if svc.Pki == "" {
			continue
		}
		host, ok := canonical[svc.Host]
		if !ok {
			continue
		}
		if meshPublic {
			addPublic(host, meshpaths.MTLSPort)
		} else {
			addPrivate(host, meshpaths.MTLSPort)
		}
	}

	out := map[string]types.FirewallPorts{}
	hosts := map[string]bool{}
	for h := range public {
		hosts[h] = true
	}
	for h := range private {
		hosts[h] = true
	}
	for h := range exposed {
		hosts[h] = true
	}
	for h := range hosts {
		fp := types.FirewallPorts{Public: sortedInts(public[h]), Private: sortedInts(private[h]), PrivateExposed: sortedExposedPorts(exposed[h])}
		if len(fp.Private) > 0 || len(fp.PrivateExposed) > 0 {
			fp.PrivateSourceCIDR = netCIDR[h]
		}
		out[h] = fp
	}
	return out
}

// sortedExposedPorts returns the set's exposed ports in a stable order (proto, then
// port) so the rendered firewall is deterministic.
func sortedExposedPorts(set map[types.ExposedPort]bool) []types.ExposedPort {
	if len(set) == 0 {
		return nil
	}
	out := make([]types.ExposedPort, 0, len(set))
	for ep := range set {
		out = append(out, ep)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Proto != out[j].Proto {
			return out[i].Proto < out[j].Proto
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// networkCIDRByCompute maps every canonical compute specKey (and bare name) to the
// CIDR of the network it attaches to, so the firewall can scope a backend's private
// target ports to that network. An unresolved network leaves the entry empty.
func networkCIDRByCompute(res types.Resources, canonical map[string]string) map[string]string {
	cidrByNet := map[string]string{}
	for _, n := range res.Network {
		cidrByNet[n.Name] = n.CIDR
	}
	out := map[string]string{}
	for _, c := range res.Compute {
		cidr := cidrByNet[c.Network]
		for i := 1; i <= c.InstanceCount; i++ {
			out[naming.SpecKey(c.Name, i)] = cidr
		}
		out[c.Name] = cidr
	}
	return out
}

// sortedInts returns the keys of a set as a sorted slice (nil for an empty set).
func sortedInts(set map[int]bool) []int {
	if len(set) == 0 {
		return nil
	}
	out := make([]int, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// ingressRoutesByHost derives the typed inbound routes for every service that
// references an ingress, grouped by the canonical specKey of the ingress's host
// (ADR-0026). One IngressRoute is emitted per route:
//   - tls-termination: the route carries every SNI it serves (the auto-derived
//     "<svc>.svc" name plus vanity), rendered as one nginx server block with one
//     ACME certificate.
//   - forward: a stream-forward route to the backend with the PROXY protocol.
//
// A co-located route (service host == ingress host) carries Backend "127.0.0.1"; a
// cross-host route leaves Backend empty and records its service → backend host
// specKey in crossHost[ingHost] so the caller supplies the backend's private IP. A
// non-empty result for a host is the realization trigger: nginx is installed on an
// ingress host iff at least one route targets it. Within a host, routes are sorted
// (by listen port, then service) so realized resources are stable across runs. It
// rejects an SNI claimed by two routes on the same listen port of one ingress host
// — the preview/deploy half of the rule validate also enforces, so the guarantee
// holds even when `up` runs without a prior `validate`.
func ingressRoutesByHost(res types.Resources, canonical map[string]string, env, slug, baseDomain string) (routesByHost map[string][]types.IngressRoute, crossHost map[string]map[string]string, err error) {
	routesByHost = map[string][]types.IngressRoute{}
	crossHost = map[string]map[string]string{}
	for _, is := range resolveIngressServices(res, canonical) {
		for _, rt := range is.svc.Routes {
			var fqdns []string
			if rt.Type == types.IngressTypeTLSTermination {
				fqdns = ingressFQDNs(is.svc.Name, rt, env, slug, baseDomain)
				sort.Strings(fqdns) // canonical server_name / cert SNI order
			}
			route := types.IngressRoute{
				Service: is.svc.Name,
				Type:    rt.Type,
				FQDNs:   fqdns,
				Listen:  rt.Listen,
				Target:  rt.Target,
			}
			if is.coLocated {
				route.Backend = "127.0.0.1"
			} else {
				if crossHost[is.ingHost] == nil {
					crossHost[is.ingHost] = map[string]string{}
				}
				crossHost[is.ingHost][is.svc.Name] = is.svcHost
			}
			routesByHost[is.ingHost] = append(routesByHost[is.ingHost], route)
		}
	}
	// Two services on one ingress can resolve to the same SNI on the same listen
	// port (e.g. the same vanity FQDN), which nginx could not demux. The same FQDN
	// on different listen ports is fine, so the guard keys on (listen, SNI).
	// derivedRecords guards the DNS side, but it is a no-op when the region has no
	// DNS authority, so guard the route side here too.
	for hostKey, routes := range routesByHost {
		seen := map[string]string{} // "listen|fqdn" -> service
		for _, rt := range routes {
			for _, fqdn := range rt.FQDNs {
				key := fmt.Sprintf("%d|%s", rt.Listen, fqdn)
				if prev, dup := seen[key]; dup {
					return nil, nil, fmt.Errorf("ingress host %q has two routes for SNI %q on listen %d (services %s and %s); an SNI must be unique per listen port", hostKey, fqdn, rt.Listen, prev, rt.Service)
				}
				seen[key] = rt.Service
			}
		}
	}
	for _, routes := range routesByHost {
		sort.Slice(routes, func(i, j int) bool {
			if routes[i].Listen != routes[j].Listen {
				return routes[i].Listen < routes[j].Listen
			}
			return routes[i].Service < routes[j].Service
		})
	}
	return routesByHost, crossHost, nil
}

// resolveNetworkOutput returns the NetworkOutputs for the subnet a compute spec
// should attach to. If spec.Subnet is set, it is used as the subnet name;
// otherwise the first subnet of the network spec (by declaration order) is
// used, which is deterministic.
func resolveNetworkOutput(spec types.ComputeSpec, networks []types.NetworkSpec, netOuts map[string]types.NetworkOutputs) (types.NetworkOutputs, error) {
	// Find the network spec by name.
	var netSpec *types.NetworkSpec
	for i := range networks {
		if networks[i].Name == spec.Network {
			netSpec = &networks[i]
			break
		}
	}
	if netSpec == nil {
		return types.NetworkOutputs{}, fmt.Errorf("network %q not found", spec.Network)
	}

	subnetName := spec.Subnet
	if subnetName == "" {
		if len(netSpec.Subnets) == 0 {
			return types.NetworkOutputs{}, fmt.Errorf("network %q has no subnets", spec.Network)
		}
		subnetName = netSpec.Subnets[0].Name
	}

	key := spec.Network + "/" + subnetName
	out, ok := netOuts[key]
	if !ok {
		return types.NetworkOutputs{}, fmt.Errorf("network output %q not found", key)
	}
	return out, nil
}

// assembleManifest builds a compute instance's plain (secret-free) manifest.
// Secrets are no longer baked here — they are delivered to services at runtime by
// inforge-agent — so the manifest carries only the base coordinates.
func assembleManifest(spec types.ComputeSpec, env, region, slug string) (string, error) {
	base := manifest.Base{
		Version:   1,
		Region:    region,
		Namespace: tags.ContainerTag(slug, env, spec.Container),
	}
	return manifest.Generate(base, nil)
}

// derivedRecord is one A-record inforge derives for a region, paired with the
// canonical compute specKey whose public IP it points at. Splitting derivation
// (pure) from creation (the Pulumi side effect) keeps the naming logic unit-
// testable without a provider.
type derivedRecord struct {
	rec     types.DnsRecord
	hostKey string
}

// derivedRecords derives every A-record for a region: one "<compute>.vm" host
// record per compute (its SSH/cloud-init domain, pointing at instance 1), plus one
// record per service ingress FQDN (the auto "<svc>.svc" name and every vanity),
// pointing at the service's INGRESS host — where nginx terminates — not the backend
// (ADR-0026). It shares its FQDN derivation with the TLS routes (ingressFQDNs), so a
// cert and its A-record never drift. The result is deterministic (compute then
// service order).
func derivedRecords(res types.Resources, env, slug, baseDomain, ephemeralSlug string) []derivedRecord {
	var out []derivedRecord
	add := func(fqdn, container, hostKey string) {
		rel := naming.ZoneRelative(fqdn, baseDomain)
		out = append(out, derivedRecord{
			rec: types.DnsRecord{
				Name:       recordResourceName(rel),
				RecordName: rel,
				Container:  container,
			},
			hostKey: hostKey,
		})
	}
	// A-records are port-independent, so a service with several ingress entries
	// derives its "<svc>.svc" name once per entry; dedup by (RecordName, hostKey)
	// so the same name on the same host collapses to one record. A name resolving
	// to two different hosts survives (different hostKey) for createDNSRecords to
	// reject.
	seen := map[string]bool{}
	dedupAdd := func(fqdn, container, hostKey string) {
		rel := naming.ZoneRelative(fqdn, baseDomain)
		if seen[rel+"\x00"+hostKey] {
			return
		}
		seen[rel+"\x00"+hostKey] = true
		add(fqdn, container, hostKey)
	}
	for _, spec := range res.Compute {
		fqdn := naming.HostFQDN(env, slug, spec.Name, baseDomain)
		dedupAdd(fqdn, spec.Container, naming.SpecKey(spec.Name, 1))
	}
	// A service's ingress FQDNs resolve to its INGRESS host (where nginx terminates),
	// not its backend host — so they share resolveIngressServices with the firewall
	// and route derivations and cannot point at a different host than nginx runs on.
	canonical := naming.CanonicalComputeKeys(res.Compute)
	for _, is := range resolveIngressServices(res, canonical) {
		for _, rt := range is.svc.Routes {
			for _, fqdn := range ingressFQDNs(is.svc.Name, rt, env, slug, baseDomain) {
				dedupAdd(fqdn, is.svc.Container, is.ingHost)
			}
		}
		// A health endpoint is addressed by the service FQDN too (the Host the
		// shared health listener demuxes on), so a health-only service — no routes,
		// hence no route-derived record — still gets its A record at the ingress
		// host. For a routed service dedupAdd collapses this with the route record.
		if is.svc.HealthProbesPort > 0 {
			dedupAdd(naming.ServiceFQDN(env, slug, is.svc.Name, baseDomain), is.svc.Container, is.ingHost)
		}
	}
	// A gateway-listed service without an ingress surfaces its health at the
	// GATEWAY's host (ADR-0034) — same shared resolver as the nginx health entries
	// and the firewall, so record, listener, and rules cannot drift. D12 keeps
	// this exclusive with the ingress record above (no two-host derivation).
	for _, gs := range resolveGatewayHealthServices(res, canonical) {
		dedupAdd(naming.ServiceFQDN(env, slug, gs.svc.Name, baseDomain), gs.svc.Container, gs.gwHost)
	}
	// An app's FQDN (the clean dotted form) is a grey-cloud A record at its ingress
	// host — the same host nginx serves it from, sharing resolveIngressApps with the
	// firewall and nginx derivations so the record can never point elsewhere. Proxied
	// defaults to false so Let's Encrypt HTTP-01 reaches the origin (ADR-0026).
	for _, ia := range resolveIngressApps(res, canonical) {
		fqdn := naming.AppFQDN(ia.app.Subdomain, slug, baseDomain, ephemeralSlug)
		dedupAdd(fqdn, ia.app.Container, ia.ingHost)
	}
	// The gateway's FQDN is a grey-cloud A record at its own host (where its nginx
	// terminates daemon TLS) — the SAME resolveGateways derivation gatewaysByHost
	// feeds the server_name/ACME cert from, so record and cert can never drift.
	for _, rg := range resolveGateways(res, canonical, slug, baseDomain, ephemeralSlug) {
		dedupAdd(rg.fqdn, rg.gw.Container, rg.host)
	}
	return out
}

// createDNSRecords creates every derived A-record for a region against its DNS
// authority, each pointing at its host's public IP. When the region declares no
// DNS authority, it is a no-op.
func createDNSRecords(ctx *pulumi.Context, reg registry.ProviderRegistry, authority *regions.DnsAuthority, res types.Resources, computeOut map[string]types.ComputeOutputs, env, slug, baseDomain, ephemeralSlug string) error {
	if authority == nil {
		return nil
	}
	dp, err := reg.DNS(authority.Provider)
	if err != nil {
		return err
	}
	// derivedRecords already collapses a name repeated on one host (a service with
	// several ingress entries), so a duplicate RecordName here means the same name
	// resolving to two different hosts (e.g. two services on different hosts sharing
	// a vanity FQDN) — reject it rather than letting the apply fail mid-way. (An SNI
	// claimed twice on one host is a cert conflict, caught by routesByHost.)
	seen := map[string]string{}
	for _, dr := range derivedRecords(res, env, slug, baseDomain, ephemeralSlug) {
		if prev, dup := seen[dr.rec.RecordName]; dup {
			return fmt.Errorf("dns: record %q is derived more than once (%s and %s); a DNS name must resolve to one host", dr.rec.RecordName, prev, dr.rec.Name)
		}
		seen[dr.rec.RecordName] = dr.rec.Name
		target, ok := computeOut[dr.hostKey]
		if !ok {
			return fmt.Errorf("dns: record %q host %q has no output (available: %v)", dr.rec.RecordName, dr.hostKey, sortedKeys(computeOut))
		}
		if err := dp.CreateRecord(ctx, dr.rec, target); err != nil {
			return err
		}
	}
	return nil
}

// recordResourceName builds a stable, unique resource-name component from a
// record's zone-relative name: dots become dashes ("bridge.svc.prd.use1" ->
// "bridge-svc-prd-use1"), and the apex becomes "apex".
func recordResourceName(zoneRelative string) string {
	if zoneRelative == "@" {
		return "apex"
	}
	return strings.ReplaceAll(zoneRelative, ".", "-")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

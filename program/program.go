// Package program is the Pulumi program that turns an environment's resolved
// resources into a deployment. It is used as an inline program via the
// Automation API in the inforge CLI.
package program

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
	"github.com/wardnet/inforge/internal/agehost"
	"github.com/wardnet/inforge/internal/bootstrapper"
	"github.com/wardnet/inforge/internal/loader"
	"github.com/wardnet/inforge/internal/manifest"
	"github.com/wardnet/inforge/internal/naming"
	iremote "github.com/wardnet/inforge/internal/remote"
	"github.com/wardnet/inforge/internal/registry"
	"github.com/wardnet/inforge/internal/service"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
	"gopkg.in/yaml.v3"
)

// Run is the Pulumi program entry point, passed to the Automation API as an
// inline program source.
func Run(ctx *pulumi.Context) error {
	cfg := config.New(ctx, "")
	env := cfg.Require("environment")
	dir := "./resources"
	if d := cfg.Get("dir"); d != "" {
		dir = d
	}

	vars, err := loader.LoadVariables(env, dir)
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

	// inforgeVersion pins the inforge-bootstrap release asset each host downloads
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

	regionTable, err := loader.LoadRegionTable(env, dir)
	if err != nil {
		return err
	}
	byRegion, err := loader.LoadResources(env, dir)
	if err != nil {
		return err
	}

	desc, err := service.BuildDeployDescriptor(env, vars.BaseDomain, byRegion, regionTable)
	if err != nil {
		return err
	}
	ctx.Export("deployDescriptor", pulumi.Any(desc))

	registries := make(map[string]registry.ProviderRegistry, len(vars.Regions))
	for _, re := range vars.Regions {
		registries[re.Name] = registry.BuildRegistry(ctx, vars.Providers, vars.SSH, regionTable, ctx.Project(), env, re.Name)
	}

	// networkOutputs: region → specName+"/"+subnetName → NetworkOutputs
	networkOutputs := map[string]map[string]types.NetworkOutputs{}
	computeOutputs := map[string]map[string]types.ComputeOutputs{}
	databaseOutputs := map[string]map[string]types.DatabaseOutputs{}

	for _, re := range vars.Regions {
		region := re.Name
		reg := registries[region]
		res := byRegion[region]
		slug, err := regionTable.Slug(region)
		if err != nil {
			return err
		}
		networkOutputs[region] = map[string]types.NetworkOutputs{}
		computeOutputs[region] = map[string]types.ComputeOutputs{}
		databaseOutputs[region] = map[string]types.DatabaseOutputs{}

		for _, spec := range res.Network {
			np, err := reg.Network(spec.Provider)
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

			cp, err := reg.Compute(spec.Provider)
			if err != nil {
				return err
			}
			for i := 1; i <= spec.InstanceCount; i++ {
				key := naming.SpecKey(spec.Name, i)
				domain := naming.RecordFQDN(env, slug, subdomainFor(key, spec.Name, res.DNS), vars.BaseDomain)
				out, err := cp.Create(ctx, spec, netOut, env, region, domain, man)
				if err != nil {
					return err
				}
				computeOutputs[region][key] = out
			}
		}

		for _, spec := range res.Database {
			dp, err := reg.Database(spec.Provider)
			if err != nil {
				return err
			}
			out, err := dp.Create(ctx, spec, env, region)
			if err != nil {
				return err
			}
			databaseOutputs[region][spec.Name] = out
		}
	}

	for _, re := range vars.Regions {
		region := re.Name
		reg := registries[region]
		res := byRegion[region]
		for _, spec := range res.DNS {
			dp, err := reg.DNS(spec.Provider)
			if err != nil {
				return err
			}
			computeOut, ok := computeOutputs[region][spec.Compute]
			if !ok {
				return fmt.Errorf("dns %s/%s: compute %q not found (available: %v)", region, spec.Name, spec.Compute, sortedKeys(computeOutputs[region]))
			}
			if err := dp.Create(ctx, spec, computeOut); err != nil {
				return err
			}
		}

		slug, err := regionTable.Slug(region)
		if err != nil {
			return err
		}
		// gates memoizes one cloud-init readiness gate per host in this region.
		// Both TLS realization and service provisioning SSH the same hosts, so the
		// gate they each depend on must be the same resource — share the map.
		gates := map[string]pulumi.Resource{}
		if err := realizeTLSTermination(ctx, reg, res, computeOutputs[region], gates, vars.SSH.DeployPrivateKey, env, slug, vars.BaseDomain); err != nil {
			return err
		}
		all := types.AllOutputs{Compute: computeOutputs, Database: databaseOutputs}
		bundles, err := provisionServiceSecrets(ctx, reg, res, all, env, region)
		if err != nil {
			return err
		}
		if err := provisionServices(ctx, res, computeOutputs[region], bundles, gates, vars.SSH.DeployPrivateKey, env, slug, inforgeVersion); err != nil {
			return err
		}
	}

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

// provisionServices writes the host-side scaffolding for each service in a
// region over SSH: the systemd unit (service.Unit) and the service folder, plus
// the no-login service user when one is declared. This is the raw/systemd
// delivery path; a future container path would dispatch through a provider.
//
// It never starts the unit. ExecStart=<folder>/run does not exist until
// `inforge release` delivers code, so a start here would fail and abort the
// whole `pulumi up`. The unit is written, daemon-reloaded, and enabled (for
// boot persistence); release performs the first real start with code present.
// Connection details and the preview/up guard mirror realizeTLSTermination.
func provisionServices(ctx *pulumi.Context, res types.Resources, computeOut map[string]types.ComputeOutputs, bundles map[string]*types.ServiceSecretsBundle, gates map[string]pulumi.Resource, deployPrivateKey, env, slug, inforgeVersion string) error {
	if len(res.Service) == 0 {
		return nil
	}
	// The unit's ExecStart is inforge-bootstrap, downloaded per host pinned to
	// this inforge version. A "dev" build publishes no release asset, so fail
	// the deploy with a clear message rather than emitting a doomed download.
	// Enforced only at up time; preview never runs the command.
	if !ctx.DryRun() && inforgeVersion == "dev" {
		return fmt.Errorf("cannot provision services: inforge build is 'dev' — no inforge-bootstrap release asset to download; deploy with a released inforge binary")
	}
	canonical := naming.CanonicalComputeKeys(res.Compute)
	deployUserByCompute := deployUsersByHost(res.Compute)

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
		// Every service gets a descriptor.yaml. A secret-bearing service also gets a
		// host-key-encrypted credential.age; a secret-less one (no bundle) gets a
		// static descriptor with an empty provider and no env.
		if bundle := bundles[svc.Name]; bundle != nil {
			if err := deliverServiceSecrets(ctx, svc, host, bundle, deployUser, deployPrivateKey, env, slug, gate); err != nil {
				return err
			}
		} else {
			if err := deliverServiceDescriptor(ctx, svc, host, deployUser, deployPrivateKey, env, slug, gate); err != nil {
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
	}, pulumi.DependsOn([]pulumi.Resource{gate})); err != nil {
		return fmt.Errorf("service %q: provision unit: %w", svc.Name, err)
	}
	return nil
}

// provisionServiceSecrets provisions each service's runtime secrets bundle: it
// writes the service's infra secrets under its scoped vault path and mints a
// per-service machine identity, returning the bundles keyed by service name. A
// service whose container has no infisical secrets yields no bundle (and gets a
// unit + a static secret-less descriptor, but no credential — see
// deliverServiceDescriptor).
func provisionServiceSecrets(ctx *pulumi.Context, reg registry.ProviderRegistry, res types.Resources, all types.AllOutputs, env, region string) (map[string]*types.ServiceSecretsBundle, error) {
	bundles := map[string]*types.ServiceSecretsBundle{}
	for _, svc := range res.Service {
		provName := serviceSecretsProviderName(svc, res)
		if provName == "" {
			continue
		}
		prov, err := reg.ServiceSecretsProvisioner(provName)
		if err != nil {
			return nil, err
		}
		bundle, err := prov.ProvisionService(ctx, svc, res, env, region, all)
		if err != nil {
			return nil, fmt.Errorf("service %q: provision secrets: %w", svc.Name, err)
		}
		if bundle != nil {
			bundles[svc.Name] = bundle
		}
	}
	return bundles, nil
}

// serviceSecretsProviderName returns the secrets provider a service's secrets are
// delivered through — the provider of the first SecretsSpec in the service's
// container — or "" when the container declares no secrets. It returns the
// declared provider verbatim (not a hardcoded "infisical") so an unsupported
// provider fails loudly at the registry lookup rather than being silently
// skipped.
func serviceSecretsProviderName(svc types.ServiceSpec, res types.Resources) string {
	for _, s := range res.Secrets {
		if s.Container == svc.Container {
			return s.Provider
		}
	}
	return ""
}

// deliverServiceSecrets writes a service's bootstrapper inputs onto its host: the
// secret-free descriptor.yaml (0644) and the host-key-encrypted credential.age
// (0600). It is a two-phase output dependency: a command reads the host SSH
// public key (Stdout), the program age-encrypts the identity credentials to that
// key inside an ApplyT over the pubkey + identity outputs (program-side encrypt —
// the plaintext credential never lands on disk and the host needs no age), and a
// second command writes both files. The descriptor's provider.project is the
// workspace ID, so it too is rendered inside an ApplyT on that output. Connection
// details and the preview/up guards mirror provisionService.
func deliverServiceSecrets(ctx *pulumi.Context, svc types.ServiceSpec, host types.ComputeOutputs, bundle *types.ServiceSecretsBundle, deployUser, deployPrivateKey, env, slug string, gate pulumi.Resource) error {
	conn := iremote.Connection(host.PublicIP, deployUser, deployPrivateKey)
	name := naming.Resource(env, slug, "svc", svc.Name)

	// Read the host SSH public key. It is world-readable, so no sudo is needed;
	// its Stdout (with a trailing newline agehost.Encrypt trims) is the recipient.
	// This is the first per-host SSH command in this path, so it waits on the
	// cloud-init gate; the credential write chains off it transitively.
	const readHostKey = "cat /etc/ssh/ssh_host_ed25519_key.pub"
	hostKey, err := remote.NewCommand(ctx, name+"-hostkey", &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(readHostKey),
		Update:     pulumi.String(readHostKey),
	}, pulumi.DependsOn([]pulumi.Resource{gate}))
	if err != nil {
		return fmt.Errorf("service %q: read host key: %w", svc.Name, err)
	}

	// descriptor.yaml depends on the workspace ID (provider.project), so render it
	// inside an ApplyT on that output.
	descriptor := bundle.Project.ApplyT(func(project string) (string, error) {
		return renderDescriptor(svc, bundle, project)
	}).(pulumi.StringOutput)

	// Encrypt {client_id, client_secret} to the host key inside an ApplyT over the
	// pubkey read AND both identity outputs, so the dependency is automatic and the
	// ciphertext is never built against a stale/empty key. In preview the pubkey
	// Stdout is unknown, so Pulumi skips this ApplyT entirely; if it runs at up
	// with any input empty, that is a real failure (e.g. the host key wasn't
	// readable) and must abort the deploy rather than write an empty credential.
	credAge := pulumi.All(hostKey.Stdout, bundle.ClientID, bundle.ClientSecret).ApplyT(
		func(args []interface{}) (string, error) {
			pub, _ := args[0].(string)
			clientID, _ := args[1].(string)
			clientSecret, _ := args[2].(string)
			if pub == "" || clientID == "" || clientSecret == "" {
				return "", fmt.Errorf("service %q: empty host public key or identity credential while building credential.age", svc.Name)
			}
			plaintext, err := json.Marshal(map[string]string{
				"client_id":     clientID,
				"client_secret": clientSecret,
			})
			if err != nil {
				return "", fmt.Errorf("marshal credential: %w", err)
			}
			ct, err := agehost.Encrypt(plaintext, pub)
			if err != nil {
				return "", fmt.Errorf("encrypt credential: %w", err)
			}
			return string(ct), nil
		}).(pulumi.StringOutput)

	writeScript := pulumi.All(descriptor, credAge).ApplyT(func(args []interface{}) string {
		desc, _ := args[0].(string)
		cred, _ := args[1].(string)
		return iremote.WriteFileScript(service.DescriptorPath(svc.Name), desc) + "\n" +
			iremote.WriteFileScriptMode(service.CredentialPath(svc.Name), cred, "0600")
	}).(pulumi.StringOutput)

	deleteScript := iremote.DeleteFileScript(service.DescriptorPath(svc.Name)) + "\n" +
		iremote.DeleteFileScript(service.CredentialPath(svc.Name))

	if _, err := remote.NewCommand(ctx, name+"-secrets", &remote.CommandArgs{
		Connection: conn,
		Create:     writeScript,
		Update:     writeScript,
		Delete:     pulumi.String(deleteScript),
		Triggers:   pulumi.Array{writeScript},
	}, pulumi.DependsOn([]pulumi.Resource{hostKey})); err != nil {
		return fmt.Errorf("service %q: write descriptor/credential: %w", svc.Name, err)
	}
	return nil
}

// deliverServiceDescriptor writes a secret-less service's descriptor.yaml (0644)
// onto its host: a single static command, with no provider, no env, no host-key
// read, and no credential. The descriptor is fully known at plan time (no
// workspace ID to resolve), so it needs no ApplyT. Connection details and the
// preview/up guards mirror provisionService.
func deliverServiceDescriptor(ctx *pulumi.Context, svc types.ServiceSpec, host types.ComputeOutputs, deployUser, deployPrivateKey, env, slug string, gate pulumi.Resource) error {
	conn := iremote.Connection(host.PublicIP, deployUser, deployPrivateKey)
	name := naming.Resource(env, slug, "svc", svc.Name)

	descriptor, err := renderDescriptor(svc, nil, "")
	if err != nil {
		return err
	}
	writeScript := iremote.WriteFileScript(service.DescriptorPath(svc.Name), descriptor)
	deleteScript := iremote.DeleteFileScript(service.DescriptorPath(svc.Name))

	if _, err := remote.NewCommand(ctx, name+"-secrets", &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(writeScript),
		Update:     pulumi.String(writeScript),
		Delete:     pulumi.String(deleteScript),
		Triggers:   pulumi.Array{pulumi.String(writeScript)},
	}, pulumi.DependsOn([]pulumi.Resource{gate})); err != nil {
		return fmt.Errorf("service %q: write descriptor: %w", svc.Name, err)
	}
	return nil
}

// renderDescriptor marshals the on-host bootstrapper descriptor for a service.
// It builds the bootstrapper's own Descriptor struct (imported, not duplicated)
// so the producer can never drift from the consumer's schema. A nil bundle is a
// secret-less service: the provider is left zero-valued and env nil, which the
// bootstrapper reads as "no secrets to fetch". For a secret-bearing service,
// project is the resolved workspace ID.
func renderDescriptor(svc types.ServiceSpec, bundle *types.ServiceSecretsBundle, project string) (string, error) {
	d := bootstrapper.Descriptor{
		Version: bootstrapper.SupportedVersion,
		Service: svc.Name,
		Exec:    service.ExecPath(svc.Name),
		User:    svc.User,
	}
	if bundle != nil {
		d.Provider = bootstrapper.Provider{
			Kind:        bundle.ProviderKind,
			URL:         bundle.URL,
			Project:     project,
			Environment: bundle.Environment,
			SecretPath:  bundle.SecretPath,
		}
		d.Env = bundle.Env
	}
	b, err := yaml.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("marshal descriptor for service %q: %w", svc.Name, err)
	}
	return string(b), nil
}

// serviceProvisionScript renders the host shell that downloads inforge-bootstrap,
// writes a service's unit + folder (+ no-login user), reloads systemd, and
// ENABLES the unit. It must never emit a start/restart: the bootstrapper's target
// binary (<folder>/run) does not exist until release delivers code, so a start
// would fail the deploy. All caller-supplied values interpolated into the shell
// are quoted.
func serviceProvisionScript(svc types.ServiceSpec, inforgeVersion string) string {
	steps := []string{
		"set -euo pipefail",
		bootstrapDownloadStep(inforgeVersion),
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
	return strings.Join(steps, "\n")
}

// bootstrapDownloadStep renders the idempotent shell that downloads the
// inforge-bootstrap raw release binary onto the host, verifies its checksum, and
// installs it at service.BootstrapBin. The host arch is detected on the host
// (uname -m → Go arch), the version is pinned to the deploying inforge build, and
// the goreleaser raw-asset name scheme is mirrored (inforge-bootstrap_<ver>_linux_<arch>,
// under the v<ver> release tag). The binary's sha256 is verified against the
// release checksums.txt before it is installed as the root ExecStart for every
// service — a tampered or truncated download must never run. curl -fsSL fails the
// deploy clearly on a missing asset; a trap removes the temp files on any exit.
// The version is single-quoted into a shell var so it is injection-safe while
// still composing with the shell-side ${arch} expansion.
func bootstrapDownloadStep(inforgeVersion string) string {
	return strings.Join([]string{
		"ver=" + iremote.Quote(inforgeVersion),
		"arch=$(uname -m)",
		"case \"$arch\" in",
		"  x86_64) arch=amd64 ;;",
		"  aarch64) arch=arm64 ;;",
		"  *) echo \"unsupported host arch: $arch\" >&2; exit 1 ;;",
		"esac",
		"asset=\"inforge-bootstrap_${ver}_linux_${arch}\"",
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
		fmt.Sprintf("sudo install -m 0755 \"$tmp\" %s", iremote.Quote(service.BootstrapBin)),
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

// realizeTLSTermination realizes each tls-termination resource in a region: it
// resolves the terminator's host, collects the per-service vhosts from every
// ingress-bearing service on that host (with FQDNs env-scoped here, so the
// provider stays a pure installer), and asks the provider to realize it. Compute
// FKs are resolved through the same canonicalization validation uses, so
// `compute: bridge` and `host: bridge-01` agree on the host.
func realizeTLSTermination(ctx *pulumi.Context, reg registry.ProviderRegistry, res types.Resources, computeOut map[string]types.ComputeOutputs, gates map[string]pulumi.Resource, deployPrivateKey, env, slug, baseDomain string) error {
	if len(res.TLSTermination) == 0 {
		return nil
	}

	canonical := naming.CanonicalComputeKeys(res.Compute)
	deployUserByCompute := deployUsersByHost(res.Compute)
	vhostsByCompute := vhostsByHost(res, canonical, env, slug, baseDomain)

	for _, spec := range res.TLSTermination {
		hostKey, ok := canonical[spec.Compute]
		if !ok {
			return fmt.Errorf("tls-termination %q: compute %q does not resolve to a host", spec.Name, spec.Compute)
		}
		host, ok := computeOut[hostKey]
		if !ok {
			return fmt.Errorf("tls-termination %q: host %q has no compute output (available: %v)", spec.Name, hostKey, sortedKeys(computeOut))
		}
		tp, err := reg.TLSTermination(spec.Provider)
		if err != nil {
			return err
		}
		// The realization SSHes the host, so it waits on the host's cloud-init gate.
		gate, err := cloudInitGate(ctx, gates, hostKey, host, deployPrivateKey, env, slug)
		if err != nil {
			return err
		}
		if err := tp.Realize(ctx, spec, host, deployUserByCompute[hostKey], vhostsByCompute[hostKey], env, []pulumi.Resource{gate}); err != nil {
			return err
		}
	}
	return nil
}

// deployUsersByHost maps each expanded compute specKey to its deploy user (empty
// when the compute declares none). inforge SSHes as this user to realize
// host-level resources.
func deployUsersByHost(computes []types.ComputeSpec) map[string]string {
	byHost := map[string]string{}
	for _, c := range computes {
		user := ""
		if c.DeployUser != nil {
			user = c.DeployUser.Name
		}
		for i := 1; i <= c.InstanceCount; i++ {
			byHost[naming.SpecKey(c.Name, i)] = user
		}
	}
	return byHost
}

// vhostsByHost derives the per-service vhosts for every ingress-bearing service,
// grouped by the canonical specKey of the host it runs on. Each service's FQDN
// is env-scoped here (<hostname>.<env>.<slug>.<baseDomain>) so the provider
// receives fully-resolved names. Within a host, vhosts are sorted by service so
// the realized resources are stable across runs. canonical resolves a service's
// host FK to the same specKey validation uses.
func vhostsByHost(res types.Resources, canonical map[string]string, env, slug, baseDomain string) map[string][]types.Vhost {
	byHost := map[string][]types.Vhost{}
	for _, svc := range res.Service {
		if svc.Ingress == nil {
			continue
		}
		hostKey, ok := canonical[svc.Host]
		if !ok {
			// Validation guarantees the host resolves; skip defensively.
			continue
		}
		byHost[hostKey] = append(byHost[hostKey], types.Vhost{
			Service: svc.Name,
			FQDN:    naming.RecordFQDN(env, slug, svc.Ingress.Hostname, baseDomain),
			Port:    svc.Ingress.Port,
		})
	}
	for _, vhosts := range byHost {
		sort.Slice(vhosts, func(i, j int) bool { return vhosts[i].Service < vhosts[j].Service })
	}
	return byHost
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
// inforge-bootstrap — so the manifest carries only the base coordinates.
func assembleManifest(spec types.ComputeSpec, env, region, slug string) (string, error) {
	base := manifest.Base{
		Version:   1,
		Region:    region,
		Namespace: tags.ContainerTag(slug, env, spec.Container),
	}
	return manifest.Generate(base, nil)
}

func subdomainFor(key, name string, dns []types.DnsSpec) string {
	for _, d := range dns {
		if d.Compute == key {
			return d.Subdomain
		}
	}
	return name
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

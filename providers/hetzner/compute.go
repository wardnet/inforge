package hetzner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"

	hcloud "github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/cloudinit"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
)

// SSHKeyCache deduplicates env-scoped SSH key creation across every
// HetznerCompute instance a single program run builds. program.Run builds one
// registry — and therefore one HetznerCompute — per realization scope (the
// region-less global slice plus one per region). SSH keys are Hetzner-account-
// global resources named only `wardnet-<env>-key-{user,deploy}` (no region
// slug), so each scope would otherwise register the same URN and fail the
// preview with "Duplicate resource URN". Sharing one cache across all instances
// makes ensureSshKeys create each key exactly once, under a single dedicated
// provider. See .agents/rules/ssh-keys-register-once-across-scopes.md.
type SSHKeyCache struct {
	mu sync.Mutex
	// keys is keyed by env since SSH keys are env-scoped (not region-scoped).
	keys map[string][]*hcloud.SshKey
	// provider is the dedicated hcloud provider the account-global SSH keys are
	// registered under, created once on first use with a fixed (scope-independent)
	// resource name. Pinning the keys to a stable provider name — rather than the
	// first realizing scope's region-scoped provider — keeps their owning provider
	// stable across runs even if the scope set or realization order changes;
	// otherwise Pulumi could see the provider reference move between runs and
	// replace an account-global resource every server depends on. It assumes one
	// Hetzner account per env (the keys' env-scoped names carry no account
	// dimension). nil in tests, where the instance has no provider.
	provider *hcloud.Provider
}

// NewSSHKeyCache returns an empty SSH key cache to thread through every
// BuildRegistry/NewCompute call of one program run.
func NewSSHKeyCache() *SSHKeyCache {
	return &SSHKeyCache{keys: map[string][]*hcloud.SshKey{}}
}

// HetznerCompute implements types.ComputeProvider for Hetzner Cloud. One
// instance is shared per registry (i.e. per region/scope). Firewalls are
// deduplicated per instance via a mutex-protected map; env-scoped SSH keys are
// deduplicated across instances via a shared SSHKeyCache.
type HetznerCompute struct {
	sshAuthorizedKeys string
	deployPublicKey   string
	apiToken          string
	provider          *hcloud.Provider
	project           string
	slug              string
	eph               tags.Ephemeral
	mu                sync.Mutex
	firewalls         map[string]*hcloud.Firewall
	// servers holds each created server keyed by its instance specKey
	// (naming.SpecKey(spec.Name, instance)) so AttachNetwork can attach its private
	// network AFTER the host's cloud-init readiness gate. The private network is
	// deliberately NOT attached inline in Create — see AttachNetwork and
	// .agents/rules/attach-private-network-after-cloud-init-gate.md.
	servers map[string]*serverHandle
	// sshKeys is shared across every scope's HetznerCompute so env-scoped SSH
	// keys register once across the whole program run (see SSHKeyCache).
	sshKeys *SSHKeyCache
	// instanceCounters tracks how many servers have been created for each
	// spec name so that Create assigns each call its unique specKey suffix.
	instanceCounters map[string]int
	// placementGroups holds the scope's Hetzner spread placement groups, keyed by
	// group index. Every server joins one (always-on reliability); servers bin-pack
	// into groups of maxServersPerSpreadGroup so none exceeds Hetzner's 10-server
	// cap. See .agents/rules/servers-join-spread-placement-group.md.
	placementGroups map[int]*hcloud.PlacementGroup
	// serverOrdinal counts servers created in this scope so far; it drives the
	// deterministic placement-group bin-packing (the Nth server joins group N/cap).
	serverOrdinal int
	regions       map[string]RegionConfig
}

// maxServersPerSpreadGroup is Hetzner's hard cap on servers in one spread placement
// group. Servers bin-pack across as many groups as needed to respect it.
const maxServersPerSpreadGroup = 10

// serverHandle records a created server and the subnet its private network will be
// attached to, so AttachNetwork can wire the attach after the cloud-init gate.
type serverHandle struct {
	server   *hcloud.Server
	name     string
	subnetID pulumi.StringOutput
}

// NewCompute creates a HetznerCompute provider. project is the inforge project
// name used to label cloud resources. slug is the region slug used for resource
// naming. eph carries the ADR-0028 ephemeral-env labels (zero value for a static
// env). regionOverrides is the output of ExtractRegionConfigs (the per-region
// realizations) and may be nil — Create then fails closed for any region that
// has no realization. sshKeys is the cross-scope SSH key cache shared by every
// HetznerCompute of one program run; pass nil for a standalone instance (e.g.
// tests) and a private cache is allocated.
func NewCompute(sshAuthorizedKeys, deployPublicKey, apiToken string, provider *hcloud.Provider, project, slug string, eph tags.Ephemeral, regionOverrides map[string]RegionConfig, sshKeys *SSHKeyCache) *HetznerCompute {
	if regionOverrides == nil {
		regionOverrides = map[string]RegionConfig{}
	}
	if sshKeys == nil {
		sshKeys = NewSSHKeyCache()
	}
	return &HetznerCompute{
		sshAuthorizedKeys: sshAuthorizedKeys,
		deployPublicKey:   deployPublicKey,
		apiToken:          apiToken,
		provider:          provider,
		project:           project,
		slug:              slug,
		eph:               eph,
		firewalls:         map[string]*hcloud.Firewall{},
		servers:           map[string]*serverHandle{},
		sshKeys:           sshKeys,
		instanceCounters:  map[string]int{},
		placementGroups:   map[int]*hcloud.PlacementGroup{},
		regions:           regionOverrides,
	}
}

// Create provisions one Hetzner Cloud server for a compute spec instance. The
// program loop in program/program.go calls Create once per instance_count
// expansion; Create tracks the per-spec instance counter internally so each
// call receives its unique specKey name.
func (h *HetznerCompute) Create(
	ctx *pulumi.Context,
	spec types.ComputeSpec,
	network types.NetworkOutputs,
	env, abstractRegion, domain, manifest string,
	fwPorts types.FirewallPorts,
) (types.ComputeOutputs, error) {
	// No provider guard here — see HetznerNetwork.Create: the registry already
	// dispatched by the resolved provider, so spec.Provider may be empty (defaulted).
	regionCfg, err := ResolveRegion(abstractRegion, h.regions)
	if err != nil {
		return types.ComputeOutputs{}, err
	}

	serverType, ok := regionCfg.ServerTypes[spec.Size]
	if !ok {
		return types.ComputeOutputs{}, fmt.Errorf("hetzner has no server type for size %q in region %q — add it to providers.hetzner.regions.%s.serverTypes", spec.Size, abstractRegion, abstractRegion)
	}

	resolvedImage, ok := regionCfg.Images[spec.Image]
	if !ok {
		return types.ComputeOutputs{}, fmt.Errorf("hetzner has no image for %q in region %q — add it to providers.hetzner.regions.%s.images", spec.Image, abstractRegion, abstractRegion)
	}

	fw, err := h.ensureFirewall(ctx, spec, env, fwPorts)
	if err != nil {
		return types.ComputeOutputs{}, fmt.Errorf("ensure firewall: %w", err)
	}

	sshKeyList, err := h.ensureSshKeys(ctx, env)
	if err != nil {
		return types.ComputeOutputs{}, fmt.Errorf("ensure ssh keys: %w", err)
	}

	// Advance this spec's instance counter and the scope-wide server ordinal under
	// the lock. The ordinal — deterministic in server-creation order — bin-packs
	// servers into spread placement groups of at most maxServersPerSpreadGroup.
	h.mu.Lock()
	h.instanceCounters[spec.Name]++
	instance := h.instanceCounters[spec.Name]
	h.serverOrdinal++
	groupIndex := (h.serverOrdinal - 1) / maxServersPerSpreadGroup
	h.mu.Unlock()

	pg, err := h.ensurePlacementGroup(ctx, env, groupIndex)
	if err != nil {
		return types.ComputeOutputs{}, fmt.Errorf("ensure placement group: %w", err)
	}

	serverName := naming.ResourceInstance(env, h.slug, "vm", spec.Name, instance)

	args := &hcloud.ServerArgs{
		Name:       pulumi.String(serverName),
		ServerType: pulumi.String(serverType),
		Image:      pulumi.String(resolvedImage),
		Location:   pulumi.String(regionCfg.Location),
		SshKeys: pulumi.StringArray{
			sshKeyList[0].Name,
			sshKeyList[1].Name,
		},
		FirewallIds: pulumi.IntArray{
			idToInt(fw.ID()),
		},
		PlacementGroupId: idToInt(pg.ID()).ToIntPtrOutput(),
		Labels:           toStringMap(tags.HetznerLabels(h.project, env, h.slug, spec.Container, h.eph)),
	}

	var deployUserName string
	if spec.DeployUser != nil {
		deployUserName = spec.DeployUser.Name
	}
	// A declared deploy_user needs the deploy public key to land in its
	// authorized_keys (provision.sh no-ops without it). Emitting user-data anyway
	// would replace the server (user_data is ForceNew) yet leave the deploy account
	// uncreated, so every deploy_user SSH command would still fail with
	// "[none publickey]". Fail loudly at up time instead. Skipped in preview, where
	// the key may be absent and no resource is actually created.
	if deployUserName != "" && h.deployPublicKey == "" && !ctx.DryRun() {
		return types.ComputeOutputs{}, fmt.Errorf("compute %q declares deploy_user %q but ssh.deployPublicKey is empty — set it (variables.yaml ssh.deployPublicKey / DEPLOY_PUBLIC_KEY) so the deploy account can be provisioned", spec.Name, deployUserName)
	}
	ciVars := cloudinit.Vars{
		Domain:          domain,
		DeployPublicKey: h.deployPublicKey,
		DeployUser:      deployUserName,
		Instance:        instance,
		Manifest:        manifest,
	}
	// The deploy material is substituted into a script run as root at first boot.
	// It is escaped there, but a name or key that is not what it claims to be must
	// never reach a booting server at all. Only a declared deploy_user consumes it
	// (provision.sh no-ops without one).
	if deployUserName != "" {
		if err := ciVars.Validate(); err != nil {
			return types.ComputeOutputs{}, fmt.Errorf("compute %q: %w", spec.Name, err)
		}
	}
	switch {
	case spec.CloudInit != "":
		// A project cloud_init template: assemble it (and append the provision
		// step, which creates the deploy_user when one is declared).
		userData, ciErr := cloudinit.Assemble(spec.CloudInit, ciVars)
		if ciErr != nil {
			return types.ComputeOutputs{}, fmt.Errorf("assemble cloud-init for %s: %w", serverName, ciErr)
		}
		args.UserData = pulumi.StringPtr(userData)
	case deployUserName != "":
		// No project cloud_init, but a deploy_user is declared: inforge must still
		// provision it (create the user + install the deploy public key) so it can
		// SSH in to realize host-level resources. Hetzner injects the SSH keys into
		// root only, so without this the deploy_user never exists and every
		// deploy_user SSH command fails with "[none publickey]".
		args.UserData = pulumi.StringPtr(cloudinit.ProvisionOnly(ciVars))
	}

	server, err := hcloud.NewServer(ctx, serverName, args, h.providerOpts()...)
	if err != nil {
		return types.ComputeOutputs{}, fmt.Errorf("create server %s: %w", serverName, err)
	}

	// Record the server so AttachNetwork can wire its private network AFTER the
	// cloud-init readiness gate. The network is deliberately not attached inline
	// (see AttachNetwork and the deferred-attach rule), so PrivateIP is empty here —
	// the program's post-gate attach pass fills it in, and it is the sole consumer.
	h.mu.Lock()
	h.servers[naming.SpecKey(spec.Name, instance)] = &serverHandle{
		server:   server,
		name:     serverName,
		subnetID: network.SubnetID,
	}
	h.mu.Unlock()

	return types.ComputeOutputs{
		PublicIP:  server.Ipv4Address,
		PrivateIP: pulumi.String("").ToStringOutput(),
		// Provider-supplied OTel resource identity (ADR-0030): all plan-time constants
		// resolved above, so they need no Pulumi apply. network_zone ⊃ location maps
		// onto cloud.region ⊃ cloud.availability_zone.
		CloudProvider:    "hetzner",
		CloudRegion:      regionCfg.NetworkZone,
		AvailabilityZone: regionCfg.Location,
		MachineType:      serverType,
	}, nil
}

// AttachNetwork attaches the private network of the server Create built for spec's
// instance, gated on dependsOn (the host's cloud-init readiness gate), and returns
// the Hetzner-assigned private IP. It MUST run after the gate: a private NIC present
// at first boot races cloud-init >= 25.3's Hetzner init-local network path and
// crashes it (null-named interface → network-config-v1 schema failure +
// sys_dev_path(None) TypeError → sticky `cloud-init status: error`). Deferring the
// attach past first boot lets the image's hotplug path configure the NIC cleanly.
// See the ComputeProvider contract and
// .agents/rules/attach-private-network-after-cloud-init-gate.md.
func (h *HetznerCompute) AttachNetwork(ctx *pulumi.Context, spec types.ComputeSpec, instance int, dependsOn []pulumi.Resource) (pulumi.StringOutput, error) {
	// No provider guard — the registry dispatched by resolved provider (see Create).
	key := naming.SpecKey(spec.Name, instance)
	h.mu.Lock()
	sh, ok := h.servers[key]
	h.mu.Unlock()
	if !ok {
		return pulumi.StringOutput{}, fmt.Errorf("attach network: server %q was not created by Create", key)
	}

	attach, err := hcloud.NewServerNetwork(ctx, sh.name+"-network", &hcloud.ServerNetworkArgs{
		ServerId: idToInt(sh.server.ID()),
		SubnetId: sh.subnetID.ToStringPtrOutput(),
	}, append(h.providerOpts(), pulumi.DependsOn(dependsOn))...)
	if err != nil {
		return pulumi.StringOutput{}, fmt.Errorf("attach network for %s: %w", sh.name, err)
	}
	return attach.Ip, nil
}

// ensurePlacementGroup returns this scope's spread placement group for the given
// index, creating it once and memoizing it. Every server joins one (always-on
// reliability): the program advances a scope-wide server ordinal that bin-packs
// servers into groups of at most maxServersPerSpreadGroup, so an index maps to one
// spread group of up to 10 servers. A spread group keeps its members on distinct
// physical hosts, reducing correlated failure. Safe to call concurrently. See
// .agents/rules/servers-join-spread-placement-group.md.
func (h *HetznerCompute) ensurePlacementGroup(ctx *pulumi.Context, env string, index int) (*hcloud.PlacementGroup, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if pg, ok := h.placementGroups[index]; ok {
		return pg, nil
	}

	name := naming.Resource(env, h.slug, "pg", fmt.Sprintf("%02d", index+1))
	pg, err := hcloud.NewPlacementGroup(ctx, name, &hcloud.PlacementGroupArgs{
		Name:   pulumi.String(name),
		Type:   pulumi.String("spread"),
		Labels: toStringMap(tags.HetznerLabels(h.project, env, h.slug, "", h.eph)),
	}, h.providerOpts()...)
	if err != nil {
		return nil, fmt.Errorf("create placement group %s: %w", name, err)
	}
	h.placementGroups[index] = pg
	return pg, nil
}

// ensureFirewall returns the hcloud.Firewall for the spec name, creating it if it
// does not yet exist. The inbound rule set is derived, not hand-maintained: SSH
// (22) is always permitted from anywhere, the host's derived public ports (an
// ingress host's route listen ports, plus :80 when it terminates TLS) are opened
// to the internet, the derived private ports (a backend's route target ports and a
// service's exposed_ports, ADR-0029) are opened only to the private network CIDR
// (reachable from a co-tenant ingress or sibling node over the private network,
// never the internet), and any explicit spec.Firewall.Inbound rules are added on
// top as public. exposed_ports are proto-aware (tcp/udp). Duplicate (proto, port,
// source) tuples are collapsed so the rendered firewall is stable. It is safe to
// call concurrently.
func (h *HetznerCompute) ensureFirewall(ctx *pulumi.Context, spec types.ComputeSpec, env string, fwPorts types.FirewallPorts) (*hcloud.Firewall, error) {
	fwName := naming.Resource(env, h.slug, "fw", spec.Name)

	h.mu.Lock()
	defer h.mu.Unlock()

	if fw, ok := h.firewalls[fwName]; ok {
		return fw, nil
	}

	// publicSources is the internet; privateSources scopes a backend's target port
	// to the host's private network CIDR (validation guarantees an ingress fronting
	// it shares that network).
	publicSources := pulumi.StringArray{pulumi.String("0.0.0.0/0"), pulumi.String("::/0")}
	var privateSources pulumi.StringArray
	if fwPorts.PrivateSourceCIDR != "" {
		privateSources = pulumi.StringArray{pulumi.String(fwPorts.PrivateSourceCIDR)}
	}

	rules := make(hcloud.FirewallRuleArray, 0, len(fwPorts.Public)+len(fwPorts.Private)+len(fwPorts.PrivateExposed)+4)
	// De-duplicate by (proto, port, scope) so a port that is both derived and
	// declared, or appears in two lists, is rendered once.
	seen := map[string]bool{}
	addRule := func(proto, port string, sources pulumi.StringArray, scope string) {
		key := proto + "/" + port + "/" + scope
		if seen[key] {
			return
		}
		seen[key] = true
		rules = append(rules, &hcloud.FirewallRuleArgs{
			Direction: pulumi.String("in"),
			Protocol:  pulumi.String(proto),
			Port:      pulumi.StringPtr(port),
			SourceIps: sources,
		})
	}
	addTCP := func(port string, sources pulumi.StringArray, scope string) {
		addRule("tcp", port, sources, scope)
	}
	// SSH first (management access is never locked out).
	addTCP("22", publicSources, "public")
	for _, p := range fwPorts.Public {
		addTCP(strconv.Itoa(p), publicSources, "public")
	}
	// Private rules only when a source CIDR is known; otherwise a backend port
	// would silently open to the internet.
	if privateSources != nil {
		for _, p := range fwPorts.Private {
			addTCP(strconv.Itoa(p), privateSources, "private")
		}
		// Service exposed_ports (ADR-0029): proto-aware private binds, opened only to
		// the host's private CIDR — never publicSources.
		for _, ep := range fwPorts.PrivateExposed {
			addRule(ep.Proto, strconv.Itoa(ep.Port), privateSources, "private")
		}
	}
	if spec.Firewall != nil {
		for _, r := range spec.Firewall.Inbound {
			key := r.Proto + "/" + string(r.Port) + "/public"
			if seen[key] {
				continue
			}
			seen[key] = true
			rule := &hcloud.FirewallRuleArgs{
				Direction: pulumi.String("in"),
				Protocol:  pulumi.String(r.Proto),
				SourceIps: publicSources,
			}
			if r.Proto != "icmp" {
				rule.Port = pulumi.StringPtr(string(r.Port))
			}
			rules = append(rules, rule)
		}
	}
	// Outbound: always allow all TCP, UDP, ICMP.
	rules = append(rules,
		&hcloud.FirewallRuleArgs{
			Direction:      pulumi.String("out"),
			Protocol:       pulumi.String("tcp"),
			DestinationIps: pulumi.StringArray{pulumi.String("0.0.0.0/0"), pulumi.String("::/0")},
		},
		&hcloud.FirewallRuleArgs{
			Direction:      pulumi.String("out"),
			Protocol:       pulumi.String("udp"),
			DestinationIps: pulumi.StringArray{pulumi.String("0.0.0.0/0"), pulumi.String("::/0")},
		},
		&hcloud.FirewallRuleArgs{
			Direction:      pulumi.String("out"),
			Protocol:       pulumi.String("icmp"),
			DestinationIps: pulumi.StringArray{pulumi.String("0.0.0.0/0"), pulumi.String("::/0")},
		},
	)

	fw, err := hcloud.NewFirewall(ctx, fwName, &hcloud.FirewallArgs{
		Name:   pulumi.String(fwName),
		Labels: toStringMap(tags.HetznerLabels(h.project, env, h.slug, spec.Container, h.eph)),
		Rules:  rules,
	}, h.providerOpts()...)
	if err != nil {
		return nil, fmt.Errorf("create firewall %s: %w", fwName, err)
	}

	h.firewalls[fwName] = fw
	return fw, nil
}

// ensureSshKeys returns the [user, deploy] SSH key pair for env, creating them
// if they do not yet exist, under the cache's dedicated provider so their owning
// provider is independent of which scope realizes first. SSH keys are env-scoped
// (not region-scoped). It is safe to call concurrently. Index 0 is the user
// authorized-keys key; index 1 is the deploy public key.
func (h *HetznerCompute) ensureSshKeys(ctx *pulumi.Context, env string) ([]*hcloud.SshKey, error) {
	h.sshKeys.mu.Lock()
	defer h.sshKeys.mu.Unlock()

	if keys, ok := h.sshKeys.keys[env]; ok {
		return keys, nil
	}

	prov, err := h.sshKeyProvider(ctx)
	if err != nil {
		return nil, fmt.Errorf("create ssh key provider: %w", err)
	}

	// SSH keys are env-scoped, not region- or container-scoped: omit both the
	// region-slug and container labels so the single shared key carries no
	// scope-specific label regardless of which scope creates it.
	keyLabels := toStringMap(tags.HetznerLabels(h.project, env, "", "", h.eph))

	userKeyName := naming.GlobalResource(env, "key", "user")
	userKey, err := h.newOrImportSshKey(ctx, prov, userKeyName, h.sshAuthorizedKeys, keyLabels)
	if err != nil {
		return nil, fmt.Errorf("create user ssh key %s: %w", userKeyName, err)
	}

	deployKeyName := naming.GlobalResource(env, "key", "deploy")
	deployKey, err := h.newOrImportSshKey(ctx, prov, deployKeyName, h.deployPublicKey, keyLabels)
	if err != nil {
		return nil, fmt.Errorf("create deploy ssh key %s: %w", deployKeyName, err)
	}

	keys := []*hcloud.SshKey{userKey, deployKey}
	h.sshKeys.keys[env] = keys
	return keys, nil
}

// sshKeyProvider returns the dedicated, stably-named hcloud provider the
// account-global SSH keys register under, creating it once (memoised on the
// shared cache). Its resource name is fixed (not region-scoped) so the keys'
// owning provider does not move when the scope set or realization order changes.
// Because the cache dedups key creation to a single caller, this provider is
// registered exactly once — it never collides on URN the way a per-scope provider
// would (see .agents/rules/registry-provider-names-are-region-scoped.md). Returns
// nil when the instance has no provider (tests), so keys register provider-less
// as before. Callers must hold h.sshKeys.mu.
func (h *HetznerCompute) sshKeyProvider(ctx *pulumi.Context) (*hcloud.Provider, error) {
	if h.provider == nil {
		return nil, nil
	}
	if h.sshKeys.provider != nil {
		return h.sshKeys.provider, nil
	}
	p, err := hcloud.NewProvider(ctx, "hcloud-ssh-keys", &hcloud.ProviderArgs{
		Token: pulumi.String(h.apiToken),
	})
	if err != nil {
		return nil, err
	}
	h.sshKeys.provider = p
	return p, nil
}

// newOrImportSshKey creates an SSH key in Hetzner under prov, importing the
// existing one if a key with the same name is already present (adopt-or-create,
// idempotent). It uses a direct hcloud API call rather than Pulumi's
// LookupSshKey invoke, which logs engine-level errors on 404 and fails the stack
// even when caught.
func (h *HetznerCompute) newOrImportSshKey(ctx *pulumi.Context, prov *hcloud.Provider, name, publicKey string, labels pulumi.StringMap) (*hcloud.SshKey, error) {
	var opts []pulumi.ResourceOption
	if prov != nil {
		opts = append(opts, pulumi.Provider(prov))
	}
	if id, err := h.lookupSshKeyID(name); err == nil && id != 0 {
		opts = append(opts, pulumi.Import(pulumi.ID(strconv.Itoa(id))))
	}
	return hcloud.NewSshKey(ctx, name, &hcloud.SshKeyArgs{
		Name:      pulumi.String(name),
		PublicKey: pulumi.String(publicKey),
		Labels:    labels,
	}, opts...)
}

// lookupSshKeyID queries the hcloud API directly for an SSH key by name and
// returns its numeric ID, or 0 if not found. Errors are treated as not-found
// so a missing or misconfigured token falls back to create rather than failing.
func (h *HetznerCompute) lookupSshKeyID(name string) (int, error) {
	if h.apiToken == "" {
		return 0, nil
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"https://api.hetzner.cloud/v1/ssh_keys?name="+name, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+h.apiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	var result struct {
		SSHKeys []struct {
			ID int `json:"id"`
		} `json:"ssh_keys"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}
	if len(result.SSHKeys) == 0 {
		return 0, nil
	}
	return result.SSHKeys[0].ID, nil
}

// providerOpts returns the Pulumi provider resource option when a provider is
// set, and nil otherwise (used in tests where provider is nil).
func (h *HetznerCompute) providerOpts() []pulumi.ResourceOption {
	if h.provider == nil {
		return nil
	}
	return []pulumi.ResourceOption{pulumi.Provider(h.provider)}
}

// idToInt converts a Pulumi resource ID output to the IntOutput the hcloud SDK
// wants for numeric ID inputs (ServerId/NetworkId/FirewallId). Hetzner resource
// IDs are numeric strings; the conversion runs inside the apply and is skipped for
// unknown IDs in preview.
func idToInt(id pulumi.IDOutput) pulumi.IntOutput {
	return id.ApplyT(func(id pulumi.ID) (int, error) {
		return strconv.Atoi(string(id))
	}).(pulumi.IntOutput)
}

package hetzner

import (
	"fmt"
	"strconv"
	"sync"

	hcloud "github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/cloudinit"
	"github.com/wardnet/inforge/internal/images"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
)

// shapes maps the canonical size names to Hetzner server type identifiers.
var shapes = map[string]string{
	"SMALL":  "cax11",
	"MEDIUM": "cax21",
	"LARGE":  "cax31",
}

// HetznerCompute implements types.ComputeProvider for Hetzner Cloud. One
// instance is shared per registry (i.e. per region). Firewalls and SSH keys
// are deduplicated across multiple Create calls via mutex-protected maps.
type HetznerCompute struct {
	sshAuthorizedKeys string
	deployPublicKey   string
	provider          *hcloud.Provider
	project           string
	slug              string
	mu                sync.Mutex
	firewalls         map[string]*hcloud.Firewall
	// sshKeys is keyed by env since SSH keys are env-scoped (not region-scoped).
	sshKeys          map[string][]*hcloud.SshKey
	// instanceCounters tracks how many servers have been created for each
	// spec name so that Create assigns each call its unique specKey suffix.
	instanceCounters map[string]int
	regions          map[string]RegionConfig
}

// NewCompute creates a HetznerCompute provider. project is the inforge project
// name used to label cloud resources. slug is the region slug used for resource
// naming. regionOverrides is the output of ExtractRegionConfigs and may be nil
// (DefaultRegionConfigs are used instead).
func NewCompute(sshAuthorizedKeys, deployPublicKey string, provider *hcloud.Provider, project, slug string, regionOverrides map[string]RegionConfig) *HetznerCompute {
	if regionOverrides == nil {
		regionOverrides = map[string]RegionConfig{}
	}
	return &HetznerCompute{
		sshAuthorizedKeys: sshAuthorizedKeys,
		deployPublicKey:   deployPublicKey,
		provider:          provider,
		project:           project,
		slug:              slug,
		firewalls:         map[string]*hcloud.Firewall{},
		sshKeys:           map[string][]*hcloud.SshKey{},
		instanceCounters:  map[string]int{},
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
	env, abstractRegion, domain, manifest, bootstrapDoc string,
) (types.ComputeOutputs, error) {
	if spec.Provider != "hetzner" {
		return types.ComputeOutputs{}, fmt.Errorf("hetzner provider received spec with provider=%q", spec.Provider)
	}

	regionCfg, err := ResolveRegion(abstractRegion, h.regions)
	if err != nil {
		return types.ComputeOutputs{}, err
	}

	serverType, ok := shapes[spec.Size]
	if !ok {
		return types.ComputeOutputs{}, fmt.Errorf("hetzner has no shape for size %q", spec.Size)
	}

	resolvedImage, err := ResolveImage(images.CanonicalImage(spec.Image))
	if err != nil {
		return types.ComputeOutputs{}, err
	}

	fw, err := h.ensureFirewall(ctx, spec, env)
	if err != nil {
		return types.ComputeOutputs{}, fmt.Errorf("ensure firewall: %w", err)
	}

	sshKeyList, err := h.ensureSshKeys(ctx, env)
	if err != nil {
		return types.ComputeOutputs{}, fmt.Errorf("ensure ssh keys: %w", err)
	}

	// Advance this spec's instance counter under the lock.
	h.mu.Lock()
	h.instanceCounters[spec.Name]++
	instance := h.instanceCounters[spec.Name]
	h.mu.Unlock()

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
			fw.ID().ApplyT(func(id pulumi.ID) (int, error) {
				return strconv.Atoi(string(id))
			}).(pulumi.IntOutput),
		},
		Networks: hcloud.ServerNetworkTypeArray{
			hcloud.ServerNetworkTypeArgs{
				SubnetId: network.SubnetID.ToStringPtrOutput(),
			},
		},
		Labels: toStringMap(tags.HetznerLabels(h.project, env, h.slug, spec.Container)),
	}

	if spec.CloudInit != "" {
		var deployUserName string
		if spec.DeployUser != nil {
			deployUserName = spec.DeployUser.Name
		}
		userData, ciErr := cloudinit.Assemble(spec.CloudInit, cloudinit.Vars{
			Domain:          domain,
			DeployPublicKey: h.deployPublicKey,
			DeployUser:      deployUserName,
			Instance:        instance,
			Manifest:        manifest,
			BootstrapDoc:    bootstrapDoc,
		})
		if ciErr != nil {
			return types.ComputeOutputs{}, fmt.Errorf("assemble cloud-init for %s: %w", serverName, ciErr)
		}
		args.UserData = pulumi.StringPtr(userData)
	}

	server, err := hcloud.NewServer(ctx, serverName, args, h.providerOpts()...)
	if err != nil {
		return types.ComputeOutputs{}, fmt.Errorf("create server %s: %w", serverName, err)
	}

	return types.ComputeOutputs{PublicIP: server.Ipv4Address}, nil
}

// defaultInboundRules are the inbound rules applied when a compute spec does
// not declare explicit firewall rules: SSH only.
var defaultInboundRules = []types.FirewallRule{
	{Proto: "tcp", Port: "22"},
}

// ensureFirewall returns the hcloud.Firewall for the spec name, creating it if
// it does not yet exist. When spec.Firewall is set its inbound rules are used;
// otherwise the built-in defaults apply. SSH (22) is always included as the
// first inbound rule regardless of what the spec declares. It is safe to call
// concurrently.
func (h *HetznerCompute) ensureFirewall(ctx *pulumi.Context, spec types.ComputeSpec, env string) (*hcloud.Firewall, error) {
	fwName := naming.Resource(env, h.slug, "fw", spec.Name)

	h.mu.Lock()
	defer h.mu.Unlock()

	if fw, ok := h.firewalls[fwName]; ok {
		return fw, nil
	}

	inbound := defaultInboundRules
	if spec.Firewall != nil {
		// Always prepend SSH so management access is never locked out.
		ssh := types.FirewallRule{Proto: "tcp", Port: "22"}
		inbound = append([]types.FirewallRule{ssh}, spec.Firewall.Inbound...)
	}

	rules := make(hcloud.FirewallRuleArray, 0, len(inbound)+3)
	for _, r := range inbound {
		rule := &hcloud.FirewallRuleArgs{
			Direction: pulumi.String("in"),
			Protocol:  pulumi.String(r.Proto),
			SourceIps: pulumi.StringArray{pulumi.String("0.0.0.0/0"), pulumi.String("::/0")},
		}
		if r.Proto != "icmp" {
			rule.Port = pulumi.StringPtr(string(r.Port))
		}
		rules = append(rules, rule)
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
		Labels: toStringMap(tags.HetznerLabels(h.project, env, h.slug, spec.Container)),
		Rules:  rules,
	}, h.providerOpts()...)
	if err != nil {
		return nil, fmt.Errorf("create firewall %s: %w", fwName, err)
	}

	h.firewalls[fwName] = fw
	return fw, nil
}

// ensureSshKeys returns the [user, deploy] SSH key pair for env, creating them
// if they do not yet exist. SSH keys are env-scoped (not region-scoped). It is
// safe to call concurrently. Index 0 is the user authorized-keys key; index 1
// is the deploy public key.
func (h *HetznerCompute) ensureSshKeys(ctx *pulumi.Context, env string) ([]*hcloud.SshKey, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if keys, ok := h.sshKeys[env]; ok {
		return keys, nil
	}

	// SSH keys are env-scoped, not container-scoped: omit container label.
	keyLabels := toStringMap(tags.HetznerLabels(h.project, env, h.slug, ""))

	userKeyName := naming.GlobalResource(env, "key", "user")
	userKey, err := h.newOrImportSshKey(ctx, userKeyName, h.sshAuthorizedKeys, keyLabels)
	if err != nil {
		return nil, fmt.Errorf("create user ssh key %s: %w", userKeyName, err)
	}

	deployKeyName := naming.GlobalResource(env, "key", "deploy")
	deployKey, err := h.newOrImportSshKey(ctx, deployKeyName, h.deployPublicKey, keyLabels)
	if err != nil {
		return nil, fmt.Errorf("create deploy ssh key %s: %w", deployKeyName, err)
	}

	keys := []*hcloud.SshKey{userKey, deployKey}
	h.sshKeys[env] = keys
	return keys, nil
}

// newOrImportSshKey creates an SSH key in Hetzner, importing the existing one
// if a key with the same name is already present (adopt-or-create, idempotent).
func (h *HetznerCompute) newOrImportSshKey(ctx *pulumi.Context, name, publicKey string, labels pulumi.StringMap) (*hcloud.SshKey, error) {
	opts := h.providerOpts()
	if h.provider != nil {
		if existing, err := hcloud.LookupSshKey(ctx, &hcloud.LookupSshKeyArgs{Name: &name}, pulumi.Provider(h.provider)); err == nil && existing != nil && existing.Id != nil {
			opts = append(opts, pulumi.Import(pulumi.ID(strconv.Itoa(*existing.Id))))
		}
	}
	return hcloud.NewSshKey(ctx, name, &hcloud.SshKeyArgs{
		Name:      pulumi.String(name),
		PublicKey: pulumi.String(publicKey),
		Labels:    labels,
	}, opts...)
}

// providerOpts returns the Pulumi provider resource option when a provider is
// set, and nil otherwise (used in tests where provider is nil).
func (h *HetznerCompute) providerOpts() []pulumi.ResourceOption {
	if h.provider == nil {
		return nil
	}
	return []pulumi.ResourceOption{pulumi.Provider(h.provider)}
}

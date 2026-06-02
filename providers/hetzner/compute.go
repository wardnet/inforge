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
	mu                sync.Mutex
	firewalls         map[string]*hcloud.Firewall
	sshKeys           map[string][]*hcloud.SshKey
	// instanceCounters tracks how many servers have been created for each
	// spec name so that Create assigns each call its unique specKey suffix.
	instanceCounters map[string]int
	regions          map[string]RegionConfig
}

// NewCompute creates a HetznerCompute provider. regionOverrides is the output
// of ExtractRegionConfigs and may be nil (DefaultRegionConfigs are used
// instead).
func NewCompute(sshAuthorizedKeys, deployPublicKey string, provider *hcloud.Provider, regionOverrides map[string]RegionConfig) *HetznerCompute {
	if regionOverrides == nil {
		regionOverrides = map[string]RegionConfig{}
	}
	return &HetznerCompute{
		sshAuthorizedKeys: sshAuthorizedKeys,
		deployPublicKey:   deployPublicKey,
		provider:          provider,
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

	fw, err := h.ensureFirewall(ctx, spec, abstractRegion, env)
	if err != nil {
		return types.ComputeOutputs{}, fmt.Errorf("ensure firewall: %w", err)
	}

	sshKeyList, err := h.ensureSshKeys(ctx, abstractRegion)
	if err != nil {
		return types.ComputeOutputs{}, fmt.Errorf("ensure ssh keys: %w", err)
	}

	// Advance this spec's instance counter under the lock.
	h.mu.Lock()
	h.instanceCounters[spec.Name]++
	instance := h.instanceCounters[spec.Name]
	h.mu.Unlock()

	key := naming.SpecKey(spec.Name, instance)

	args := &hcloud.ServerArgs{
		Name:       pulumi.String(key),
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
		Labels: pulumi.StringMap{
			"urn": pulumi.String(tags.ContainerTag(abstractRegion, env, spec.Container)),
		},
	}

	if spec.CloudInit != "" {
		userData, ciErr := cloudinit.Assemble(spec.CloudInit, cloudinit.Vars{
			Domain:          domain,
			DeployPublicKey: h.deployPublicKey,
			Instance:        instance,
			Manifest:        manifest,
			BootstrapDoc:    bootstrapDoc,
		})
		if ciErr != nil {
			return types.ComputeOutputs{}, fmt.Errorf("assemble cloud-init for %s: %w", key, ciErr)
		}
		args.UserData = pulumi.StringPtr(userData)
	}

	server, err := hcloud.NewServer(ctx, key, args, h.providerOpts()...)
	if err != nil {
		return types.ComputeOutputs{}, fmt.Errorf("create server %s: %w", key, err)
	}

	return types.ComputeOutputs{PublicIP: server.Ipv4Address}, nil
}

// defaultInboundRules are the inbound rules applied when a compute spec does
// not declare explicit firewall rules: SSH, HTTP, HTTPS, DNS-over-TLS.
var defaultInboundRules = []types.FirewallRule{
	{Proto: "tcp", Port: "22"},
	{Proto: "tcp", Port: "80"},
	{Proto: "tcp", Port: "443"},
	{Proto: "tcp", Port: "853"},
}

// ensureFirewall returns the hcloud.Firewall for {specName}-{abstractRegion},
// creating it if it does not yet exist. When spec.Firewall is set its inbound
// rules are used; otherwise the built-in defaults apply. SSH (22) is always
// included as the first inbound rule regardless of what the spec declares. It
// is safe to call concurrently.
func (h *HetznerCompute) ensureFirewall(ctx *pulumi.Context, spec types.ComputeSpec, abstractRegion, env string) (*hcloud.Firewall, error) {
	key := fmt.Sprintf("%s-%s", spec.Name, abstractRegion)

	h.mu.Lock()
	defer h.mu.Unlock()

	if fw, ok := h.firewalls[key]; ok {
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

	urn := tags.ContainerTag(abstractRegion, env, spec.Container)
	fw, err := hcloud.NewFirewall(ctx, key, &hcloud.FirewallArgs{
		Name:   pulumi.String(key),
		Labels: pulumi.StringMap{"urn": pulumi.String(urn)},
		Rules:  rules,
	}, h.providerOpts()...)
	if err != nil {
		return nil, fmt.Errorf("create firewall %s: %w", key, err)
	}

	h.firewalls[key] = fw
	return fw, nil
}

// ensureSshKeys returns the [user, deploy] SSH key pair for abstractRegion,
// creating them if they do not yet exist. It is safe to call concurrently.
// Index 0 is the user authorized-keys key; index 1 is the deploy public key.
func (h *HetznerCompute) ensureSshKeys(ctx *pulumi.Context, abstractRegion string) ([]*hcloud.SshKey, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if keys, ok := h.sshKeys[abstractRegion]; ok {
		return keys, nil
	}

	userKeyName := fmt.Sprintf("user-%s", abstractRegion)
	userKey, err := hcloud.NewSshKey(ctx, userKeyName, &hcloud.SshKeyArgs{
		Name:      pulumi.String(userKeyName),
		PublicKey: pulumi.String(h.sshAuthorizedKeys),
	}, h.providerOpts()...)
	if err != nil {
		return nil, fmt.Errorf("create user ssh key %s: %w", userKeyName, err)
	}

	deployKeyName := fmt.Sprintf("deploy-%s", abstractRegion)
	deployKey, err := hcloud.NewSshKey(ctx, deployKeyName, &hcloud.SshKeyArgs{
		Name:      pulumi.String(deployKeyName),
		PublicKey: pulumi.String(h.deployPublicKey),
	}, h.providerOpts()...)
	if err != nil {
		return nil, fmt.Errorf("create deploy ssh key %s: %w", deployKeyName, err)
	}

	keys := []*hcloud.SshKey{userKey, deployKey}
	h.sshKeys[abstractRegion] = keys
	return keys, nil
}

// providerOpts returns the Pulumi provider resource option when a provider is
// set, and nil otherwise (used in tests where provider is nil).
func (h *HetznerCompute) providerOpts() []pulumi.ResourceOption {
	if h.provider == nil {
		return nil
	}
	return []pulumi.ResourceOption{pulumi.Provider(h.provider)}
}

package hetzner

import (
	"fmt"
	"sync"

	hcloud "github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
)

// HetznerNetwork implements types.NetworkProvider for Hetzner Cloud. One
// instance is shared per registry (i.e. per region), with ensureContainer
// deduplicating Hetzner Network objects across multiple NetworkSpecs that share
// the same container + region.
type HetznerNetwork struct {
	provider *hcloud.Provider
	project  string
	slug     string
	eph      tags.Ephemeral
	mu       sync.Mutex
	// containers caches created hcloud.Network objects keyed by container name
	// to avoid creating duplicates.
	containers map[string]*containerNet
	// subnetOwners maps an already-realized subnet name to the network spec that
	// realized it. A subnet's Pulumi resource name derives from the subnet name
	// alone, so two networks of one scope declaring the same subnet name would
	// collide on one URN; Create fails closed on the second.
	subnetOwners map[string]string
	// regions holds the per-region realizations (from providers.hetzner.regions)
	// and is used by ResolveRegion to look up a region's network zone.
	regions map[string]RegionConfig
}

// New creates a HetznerNetwork provider. project is the inforge project name
// used to label cloud resources. slug is the region slug used for resource
// naming. eph carries the ADR-0028 ephemeral-env labels (zero value for a static
// env). regionOverrides is the output of ExtractRegionConfigs (the per-region
// realizations) and may be nil — Create then fails closed for any region that
// has no realization.
func New(provider *hcloud.Provider, project, slug string, eph tags.Ephemeral, regionOverrides map[string]RegionConfig) *HetznerNetwork {
	if regionOverrides == nil {
		regionOverrides = map[string]RegionConfig{}
	}
	return &HetznerNetwork{
		provider:     provider,
		project:      project,
		slug:         slug,
		eph:          eph,
		containers:   map[string]*containerNet{},
		subnetOwners: map[string]string{},
		regions:      regionOverrides,
	}
}

// containerNet is one realized hcloud Network plus the CIDR it was realized
// with, so a later NetworkSpec sharing the container can be checked against it.
type containerNet struct {
	net  *hcloud.Network
	cidr string
}

// Create provisions a Hetzner Network + Subnets for the given spec. It is safe
// to call concurrently: containers for the same container name are deduplicated
// via a mutex-protected map. Returns a map from subnet name to NetworkOutputs.
func (h *HetznerNetwork) Create(ctx *pulumi.Context, spec types.NetworkSpec, env, abstractRegion string) (map[string]types.NetworkOutputs, error) {
	// No provider guard: the registry already dispatched to this provider by the
	// caller's resolved provider name (types.ResolveProvider over providerDefaults),
	// so spec.Provider — which may legitimately be empty when defaulted — is not the
	// contract and must not be re-checked here.
	regionCfg, err := ResolveRegion(abstractRegion, h.regions)
	if err != nil {
		return nil, err
	}

	net, err := h.ensureContainer(ctx, spec.Container, env, spec.CIDR)
	if err != nil {
		return nil, fmt.Errorf("ensure container network: %w", err)
	}

	// Convert the string Pulumi resource ID to int for NetworkSubnetArgs.
	networkIntID := idToInt(net.ID())

	result := make(map[string]types.NetworkOutputs, len(spec.Subnets))
	for _, sub := range spec.Subnets {
		if err := h.claimSubnet(sub.Name, spec.Name); err != nil {
			return nil, err
		}
		subnetName := naming.Resource(env, h.slug, "subnet", sub.Name)
		subnet, err := hcloud.NewNetworkSubnet(ctx, subnetName, &hcloud.NetworkSubnetArgs{
			NetworkId:   networkIntID,
			Type:        pulumi.String("cloud"),
			NetworkZone: pulumi.String(regionCfg.NetworkZone),
			IpRange:     pulumi.String(sub.CIDR),
		}, h.providerOpts()...)
		if err != nil {
			return nil, fmt.Errorf("create subnet %s: %w", subnetName, err)
		}
		result[sub.Name] = types.NetworkOutputs{
			NetworkID: net.ID().ToStringOutput(),
			SubnetID:  subnet.ID().ToStringOutput(),
		}
	}

	return result, nil
}

// claimSubnet records that network spec owner realizes the subnet name, failing
// closed when another network already claimed it: both would derive the same
// Pulumi resource name (and thus URN) and abort the deploy with an opaque
// duplicate-resource error. Validation rejects this ahead of deploy; this is the
// provider-side backstop.
func (h *HetznerNetwork) claimSubnet(subnet, owner string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if prev, ok := h.subnetOwners[subnet]; ok {
		return fmt.Errorf("subnet %q of network %q is already declared by network %q: subnet names must be unique across the networks of a scope", subnet, owner, prev)
	}
	h.subnetOwners[subnet] = owner
	return nil
}

// ensureContainer returns the hcloud.Network for the container, creating it if
// it does not yet exist. A container's network is realized once, with the CIDR
// of the first NetworkSpec that reached it, so a later spec sharing the
// container but declaring a different CIDR is an error: its subnets would land
// in a network whose IP range does not cover them. It is safe to call
// concurrently.
func (h *HetznerNetwork) ensureContainer(ctx *pulumi.Context, container, env, cidr string) (*hcloud.Network, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if existing, ok := h.containers[container]; ok {
		if existing.cidr != cidr {
			return nil, fmt.Errorf("container %q network already realized with cidr %q, but another network spec declares cidr %q: every network sharing a container must declare the same cidr", container, existing.cidr, cidr)
		}
		return existing.net, nil
	}

	netName := naming.Resource(env, h.slug, "net", container)
	opts := h.providerOpts()
	net, err := hcloud.NewNetwork(ctx, netName, &hcloud.NetworkArgs{
		Name:    pulumi.String(netName),
		IpRange: pulumi.String(cidr),
		Labels:  toStringMap(tags.HetznerLabels(h.project, env, h.slug, container, h.eph)),
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("create hcloud network %s: %w", netName, err)
	}

	h.containers[container] = &containerNet{net: net, cidr: cidr}
	return net, nil
}

// providerOpts returns the Pulumi provider resource option when a provider is
// set, and an empty slice otherwise (used in tests where provider is nil).
func (h *HetznerNetwork) providerOpts() []pulumi.ResourceOption {
	if h.provider == nil {
		return nil
	}
	return []pulumi.ResourceOption{pulumi.Provider(h.provider)}
}

// toStringMap converts a map[string]string to pulumi.StringMap.
func toStringMap(m map[string]string) pulumi.StringMap {
	out := make(pulumi.StringMap, len(m))
	for k, v := range m {
		out[k] = pulumi.String(v)
	}
	return out
}

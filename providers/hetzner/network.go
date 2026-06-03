package hetzner

import (
	"fmt"
	"strconv"
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
	mu       sync.Mutex
	// containers caches created hcloud.Network objects keyed by
	// "{container}-{abstractRegion}" to avoid creating duplicates.
	containers map[string]*hcloud.Network
	// regions is built from project overrides (regions.yaml) and used by
	// ResolveRegion as the first lookup tier before DefaultRegionConfigs.
	regions map[string]RegionConfig
}

// New creates a HetznerNetwork provider. project is the inforge project name
// used to label cloud resources. regionOverrides is the output of
// ExtractRegionConfigs and may be nil (DefaultRegionConfigs are used instead).
func New(provider *hcloud.Provider, project string, regionOverrides map[string]RegionConfig) *HetznerNetwork {
	if regionOverrides == nil {
		regionOverrides = map[string]RegionConfig{}
	}
	return &HetznerNetwork{
		provider:   provider,
		project:    project,
		containers: map[string]*hcloud.Network{},
		regions:    regionOverrides,
	}
}

// Create provisions a Hetzner Network + Subnet for the given spec. It is safe
// to call concurrently: containers for the same {container, abstractRegion}
// pair are deduplicated via a mutex-protected map.
func (h *HetznerNetwork) Create(ctx *pulumi.Context, spec types.NetworkSpec, env, abstractRegion string) (types.NetworkOutputs, error) {
	if spec.Provider != "hetzner" {
		return types.NetworkOutputs{}, fmt.Errorf("hetzner provider received spec with provider=%q", spec.Provider)
	}

	regionCfg, err := ResolveRegion(abstractRegion, h.regions)
	if err != nil {
		return types.NetworkOutputs{}, err
	}

	net, err := h.ensureContainer(ctx, spec.Container, abstractRegion, env, spec.CIDR)
	if err != nil {
		return types.NetworkOutputs{}, fmt.Errorf("ensure container network: %w", err)
	}

	subnetCIDR := spec.SubnetCIDR
	if subnetCIDR == "" {
		subnetCIDR = spec.CIDR
	}

	// Convert the string Pulumi resource ID to int for NetworkSubnetArgs.
	networkIntID := net.ID().ApplyT(func(id pulumi.ID) (int, error) {
		return strconv.Atoi(string(id))
	}).(pulumi.IntOutput)

	subnetName := fmt.Sprintf("%s-%s", naming.SpecKey(spec.Name, spec.Instance), abstractRegion)
	subnet, err := hcloud.NewNetworkSubnet(ctx, subnetName, &hcloud.NetworkSubnetArgs{
		NetworkId:   networkIntID,
		Type:        pulumi.String("cloud"),
		NetworkZone: pulumi.String(regionCfg.NetworkZone),
		IpRange:     pulumi.String(subnetCIDR),
	}, h.providerOpts()...)
	if err != nil {
		return types.NetworkOutputs{}, fmt.Errorf("create subnet %s: %w", subnetName, err)
	}

	return types.NetworkOutputs{
		NetworkID: net.ID().ToStringOutput(),
		SubnetID:  subnet.ID().ToStringOutput(),
	}, nil
}

// ensureContainer returns the hcloud.Network for {container}-{abstractRegion},
// creating it if it does not yet exist. It is safe to call concurrently.
func (h *HetznerNetwork) ensureContainer(ctx *pulumi.Context, container, abstractRegion, env, cidr string) (*hcloud.Network, error) {
	key := fmt.Sprintf("%s-%s", container, abstractRegion)

	h.mu.Lock()
	defer h.mu.Unlock()

	if net, ok := h.containers[key]; ok {
		return net, nil
	}

	opts := h.providerOpts()
	net, err := hcloud.NewNetwork(ctx, key, &hcloud.NetworkArgs{
		Name:    pulumi.String(key),
		IpRange: pulumi.String(cidr),
		Labels:  toStringMap(tags.HetznerLabels(h.project, env, abstractRegion, container)),
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("create hcloud network %s: %w", key, err)
	}

	h.containers[key] = net
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

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
	slug     string
	mu       sync.Mutex
	// containers caches created hcloud.Network objects keyed by container name
	// to avoid creating duplicates.
	containers map[string]*hcloud.Network
	// regions is built from project overrides (regions.yaml) and used by
	// ResolveRegion as the first lookup tier before DefaultRegionConfigs.
	regions map[string]RegionConfig
}

// New creates a HetznerNetwork provider. project is the inforge project name
// used to label cloud resources. slug is the region slug used for resource
// naming. regionOverrides is the output of ExtractRegionConfigs and may be nil
// (DefaultRegionConfigs are used instead).
func New(provider *hcloud.Provider, project, slug string, regionOverrides map[string]RegionConfig) *HetznerNetwork {
	if regionOverrides == nil {
		regionOverrides = map[string]RegionConfig{}
	}
	return &HetznerNetwork{
		provider:   provider,
		project:    project,
		slug:       slug,
		containers: map[string]*hcloud.Network{},
		regions:    regionOverrides,
	}
}

// Create provisions a Hetzner Network + Subnets for the given spec. It is safe
// to call concurrently: containers for the same container name are deduplicated
// via a mutex-protected map. Returns a map from subnet name to NetworkOutputs.
func (h *HetznerNetwork) Create(ctx *pulumi.Context, spec types.NetworkSpec, env, abstractRegion string) (map[string]types.NetworkOutputs, error) {
	if spec.Provider != "hetzner" {
		return nil, fmt.Errorf("hetzner provider received spec with provider=%q", spec.Provider)
	}

	regionCfg, err := ResolveRegion(abstractRegion, h.regions)
	if err != nil {
		return nil, err
	}

	net, err := h.ensureContainer(ctx, spec.Container, env, spec.CIDR)
	if err != nil {
		return nil, fmt.Errorf("ensure container network: %w", err)
	}

	// Convert the string Pulumi resource ID to int for NetworkSubnetArgs.
	networkIntID := net.ID().ApplyT(func(id pulumi.ID) (int, error) {
		return strconv.Atoi(string(id))
	}).(pulumi.IntOutput)

	result := make(map[string]types.NetworkOutputs, len(spec.Subnets))
	for _, sub := range spec.Subnets {
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

// ensureContainer returns the hcloud.Network for the container, creating it if
// it does not yet exist. It is safe to call concurrently.
func (h *HetznerNetwork) ensureContainer(ctx *pulumi.Context, container, env, cidr string) (*hcloud.Network, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if net, ok := h.containers[container]; ok {
		return net, nil
	}

	netName := naming.Resource(env, h.slug, "net", container)
	opts := h.providerOpts()
	net, err := hcloud.NewNetwork(ctx, netName, &hcloud.NetworkArgs{
		Name:    pulumi.String(netName),
		IpRange: pulumi.String(cidr),
		Labels:  toStringMap(tags.HetznerLabels(h.project, env, h.slug, container)),
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("create hcloud network %s: %w", netName, err)
	}

	h.containers[container] = net
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

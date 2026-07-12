package hetzner

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
	"gopkg.in/yaml.v3"
)

// ---- ResolveRegion tests (pure, no Pulumi) ----------------------------------

func TestResolveRegionFromRealization(t *testing.T) {
	realizations := map[string]RegionConfig{
		"us-east-1": {Location: "ash", NetworkZone: "us-east"},
	}
	cfg, err := ResolveRegion("us-east-1", realizations)
	require.NoError(t, err)
	assert.Equal(t, "ash", cfg.Location)
	assert.Equal(t, "us-east", cfg.NetworkZone)
}

func TestResolveRegionUnknownReturnsError(t *testing.T) {
	_, err := ResolveRegion("mars-1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no region realization")
	assert.Contains(t, err.Error(), "mars-1")
}

// TestResolveRegionIncompleteRealization confirms a realization missing
// location or network_zone fails closed at the provider boundary, rather than
// passing an empty value through to hcloud as an opaque apply-time error.
func TestResolveRegionIncompleteRealization(t *testing.T) {
	_, err := ResolveRegion("us-east-1", map[string]RegionConfig{
		"us-east-1": {NetworkZone: "us-east"}, // location omitted
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing location")

	_, err = ResolveRegion("us-east-1", map[string]RegionConfig{
		"us-east-1": {Location: "ash"}, // network_zone omitted
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing network_zone")
}

// TestExtractRegionConfigsFromYAML decodes a regions.yaml region's providers
// block exactly as the loader does (yaml.Unmarshal into the AbstractRegion's
// Providers shape) and confirms the hetzner realization — including serverTypes
// and images — round-trips into the RegionConfig keyed by the region name,
// verifying the decode shape empirically.
func TestExtractRegionConfigsFromYAML(t *testing.T) {
	const doc = `
slug: euc1
providers:
  hetzner:
    apiToken: tok
    location: nbg1
    network_zone: eu-central
    serverTypes: {SMALL: cx23, MEDIUM: cx33, LARGE: cx43}
    images: {ubuntu-24.04: ubuntu-24.04}
`
	var ar struct {
		Providers map[string]map[string]any `yaml:"providers"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(doc), &ar))

	got := ExtractRegionConfigs("eu-central-1", ar.Providers)
	assert.Equal(t, RegionConfig{
		Location:    "nbg1",
		NetworkZone: "eu-central",
		ServerTypes: map[string]string{"SMALL": "cx23", "MEDIUM": "cx33", "LARGE": "cx43"},
		Images:      map[string]string{"ubuntu-24.04": "ubuntu-24.04"},
	}, got["eu-central-1"])
}

func TestExtractRegionConfigs(t *testing.T) {
	tests := []struct {
		name      string
		region    string
		providers map[string]map[string]any
		want      map[string]RegionConfig
	}{
		{
			name:      "absent hetzner provider",
			region:    "us-east-1",
			providers: map[string]map[string]any{"neon": {"apiKey": "k"}},
			want:      map[string]RegionConfig{},
		},
		{
			name:      "hetzner without realization fields",
			region:    "us-east-1",
			providers: map[string]map[string]any{"hetzner": {"apiToken": "tok"}},
			want: map[string]RegionConfig{
				"us-east-1": {ServerTypes: map[string]string{}, Images: map[string]string{}},
			},
		},
		{
			name:   "non-string and empty entries skipped within maps",
			region: "us-east-1",
			providers: map[string]map[string]any{"hetzner": {
				"location":     "ash",
				"network_zone": "us-east",
				"serverTypes":  map[string]any{"SMALL": "cx23", "MEDIUM": 33, "LARGE": ""},
				"images":       map[string]any{"ubuntu-24.04": "ubuntu-24.04"},
			}},
			want: map[string]RegionConfig{
				"us-east-1": {
					Location:    "ash",
					NetworkZone: "us-east",
					ServerTypes: map[string]string{"SMALL": "cx23"},
					Images:      map[string]string{"ubuntu-24.04": "ubuntu-24.04"},
				},
			},
		},
		{
			name:   "missing maps default to empty",
			region: "us-east-1",
			providers: map[string]map[string]any{"hetzner": {
				"location": "ash", "network_zone": "us-east",
			}},
			want: map[string]RegionConfig{
				"us-east-1": {
					Location:    "ash",
					NetworkZone: "us-east",
					ServerTypes: map[string]string{},
					Images:      map[string]string{},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractRegionConfigs(tt.region, tt.providers))
		})
	}
}

// ---- ensureContainer idempotency test (requires Pulumi mock runtime) --------

type testMocks struct{}

func (m *testMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *testMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	return args.Name + "-id", resource.PropertyMap{
		"name":    resource.NewStringProperty(args.Name),
		"ipRange": resource.NewStringProperty("10.0.0.0/8"),
		"labels":  resource.NewObjectProperty(resource.PropertyMap{}),
	}, nil
}

// numericIDMocks hands back a numeric resource ID, which a subnet's networkId
// (an int arg, via idToInt) requires.
type numericIDMocks struct{}

func (m *numericIDMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *numericIDMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	return "42", resource.PropertyMap{"name": resource.NewStringProperty(args.Name)}, nil
}

// TestEnsureContainerCIDRMismatch: a container is realized as one hcloud Network
// with the first spec's CIDR. A later spec sharing the container but declaring a
// different CIDR must fail closed rather than silently placing its subnets in a
// network whose IP range does not cover them.
func TestEnsureContainerCIDRMismatch(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := New(nil, "test-project", "use1", tags.Ephemeral{}, nil)

		_, err := h.ensureContainer(ctx, "edge", "prod", "10.0.0.0/16")
		require.NoError(t, err)

		_, err = h.ensureContainer(ctx, "edge", "prod", "10.1.0.0/16")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must declare the same cidr")
		return nil
	}, pulumi.WithMocks("inforge", "test", &testMocks{}))

	require.NoError(t, err)
}

// TestCreateRejectsDuplicateSubnetName: a subnet's Pulumi resource name derives
// from the subnet name alone, so two networks of one scope reusing a subnet name
// would collide on one URN. Create fails closed on the second.
func TestCreateRejectsDuplicateSubnetName(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		regions := map[string]RegionConfig{"eu-central": {Location: "nbg1", NetworkZone: "eu-central"}}
		h := New(nil, "test-project", "euc", tags.Ephemeral{}, regions)

		_, err := h.Create(ctx, types.NetworkSpec{
			Name: "corenet", Container: "core", CIDR: "10.0.0.0/16",
			Subnets: []types.SubnetSpec{{Name: "app", CIDR: "10.0.1.0/24"}},
		}, "prod", "eu-central")
		require.NoError(t, err)

		_, err = h.Create(ctx, types.NetworkSpec{
			Name: "edgenet", Container: "edge", CIDR: "10.1.0.0/16",
			Subnets: []types.SubnetSpec{{Name: "app", CIDR: "10.1.1.0/24"}},
		}, "prod", "eu-central")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `already declared by network "corenet"`)
		return nil
	}, pulumi.WithMocks("inforge", "test", &numericIDMocks{}))

	require.NoError(t, err)
}

func TestEnsureContainerIdempotency(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := New(nil, "test-project", "use1", tags.Ephemeral{}, nil)

		net1, err := h.ensureContainer(ctx, "vpc", "prod", "10.0.0.0/8")
		if err != nil {
			return err
		}
		net2, err := h.ensureContainer(ctx, "vpc", "prod", "10.0.0.0/8")
		if err != nil {
			return err
		}
		// Pointer identity: same key must return the exact same *hcloud.Network.
		if net1 != net2 {
			t.Error("ensureContainer returned different objects for the same key")
		}
		// Different container must produce a distinct object.
		net3, err := h.ensureContainer(ctx, "db", "prod", "10.1.0.0/16")
		if err != nil {
			return err
		}
		if net1 == net3 {
			t.Error("ensureContainer returned the same object for different keys")
		}
		return nil
	}, pulumi.WithMocks("inforge", "test", &testMocks{}))

	require.NoError(t, err)
}

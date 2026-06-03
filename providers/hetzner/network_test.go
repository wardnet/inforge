package hetzner

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/regions"
)

// ---- ResolveRegion tests (pure, no Pulumi) ----------------------------------

func TestResolveRegionBuiltIn(t *testing.T) {
	cfg, err := ResolveRegion("us-east-1", nil)
	require.NoError(t, err)
	assert.Equal(t, "ash", cfg.Location)
	assert.Equal(t, "us-east", cfg.NetworkZone)
}

func TestResolveRegionProjectOverrideWins(t *testing.T) {
	overrides := map[string]RegionConfig{
		"us-east-1": {Location: "nbg1", NetworkZone: "eu-central"},
	}
	cfg, err := ResolveRegion("us-east-1", overrides)
	require.NoError(t, err)
	assert.Equal(t, "nbg1", cfg.Location, "project override must win over default")
}

func TestResolveRegionUnknownReturnsError(t *testing.T) {
	_, err := ResolveRegion("mars-1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hetzner has no datacenter for abstract region")
	assert.Contains(t, err.Error(), "mars-1")
}

func TestExtractRegionConfigs(t *testing.T) {
	tbl := regions.Table{
		"us-east-1": {
			Slug: "use1",
			Providers: map[string]map[string]any{
				"hetzner": {"location": "ash", "network_zone": "us-east"},
			},
		},
		"eu-central-1": {Slug: "euc1"}, // no hetzner entry
		"custom-1": {
			Slug: "cus1",
			Providers: map[string]map[string]any{
				// incomplete entry — missing network_zone
				"hetzner": {"location": "nbg1"},
			},
		},
	}

	got := ExtractRegionConfigs(tbl)

	assert.Equal(t, RegionConfig{Location: "ash", NetworkZone: "us-east"}, got["us-east-1"])
	_, hasEUCentral := got["eu-central-1"]
	assert.False(t, hasEUCentral, "entry without hetzner providers block must be skipped")
	_, hasCustom := got["custom-1"]
	assert.False(t, hasCustom, "incomplete entry (missing network_zone) must be skipped")
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

func TestEnsureContainerIdempotency(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := New(nil, "test-project", nil)

		net1, err := h.ensureContainer(ctx, "vpc", "us-east-1", "prod", "10.0.0.0/8")
		if err != nil {
			return err
		}
		net2, err := h.ensureContainer(ctx, "vpc", "us-east-1", "prod", "10.0.0.0/8")
		if err != nil {
			return err
		}
		// Pointer identity: same key must return the exact same *hcloud.Network.
		if net1 != net2 {
			t.Error("ensureContainer returned different objects for the same key")
		}
		// Different key must produce a distinct object.
		net3, err := h.ensureContainer(ctx, "db", "us-east-1", "prod", "10.1.0.0/16")
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

package program

import (
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/registry"
	"github.com/wardnet/inforge/internal/types"
)

// infraMocks records every registered resource's logical name and returns a
// superset of outputs so a network + compute + database all resolve under one
// mock. Surplus outputs are harmless: each provider reads only the fields it
// declares.
type infraMocks struct {
	mu    sync.Mutex
	names []string
}

func (m *infraMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *infraMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	m.names = append(m.names, args.Name)
	m.mu.Unlock()
	// hcloud resource IDs are numeric (a subnet references its network by numeric
	// networkId); return a numeric ID for hcloud resources, a name-derived one
	// otherwise.
	id := args.Name + "-id"
	if strings.HasPrefix(args.TypeToken, "hcloud:") {
		id = "12345"
	}
	return id, resource.PropertyMap{
		"name":          resource.NewStringProperty(args.Name),
		"ipRange":       resource.NewStringProperty("10.0.0.0/8"),
		"ipv4Address":   resource.NewStringProperty("1.2.3.4"),
		"projectId":     resource.NewStringProperty("proj-id"),
		"branchId":      resource.NewStringProperty("br-id"),
		"connectionUrl": resource.NewStringProperty("postgresql://u:p@h/db"),
	}, nil
}

func (m *infraMocks) has(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range m.names {
		if n == name {
			return true
		}
	}
	return false
}

// TestCreateInfraGlobalScope exercises the global-first instantiation path end to
// end: createInfra with an EMPTY slug must realize network, compute, and database
// resources under the reserved "global" output slot with REGION-LESS names. This
// is the headline path of the slice — naming.Resource / naming.ResourceInstance
// with an empty slug, flowing through real provider Create calls and through
// assembleManifest's tags.ContainerTag — exercised here, not just in unit tests.
func TestCreateInfraGlobalScope(t *testing.T) {
	mocks := &infraMocks{}
	networkOutputs := map[string]map[string]types.NetworkOutputs{}
	computeOutputs := map[string]map[string]types.ComputeOutputs{}
	databaseOutputs := map[string]map[string]types.DatabaseOutputs{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		// A global registry: built from the global block's providers with an empty
		// slug (region "global" is absent from the empty table → slug "").
		config := map[string]map[string]any{
			"hetzner": {
				"apiToken":     "t",
				"location":     "ash",
				"network_zone": "us-east",
				"serverTypes":  map[string]any{"SMALL": "cx23"},
				"images":       map[string]any{"ubuntu-24.04": "ubuntu-24.04"},
			},
			"neon": {"apiKey": "k", "region": "aws-us-east-2"},
		}
		reg := registry.BuildRegistry(ctx, config, nil, types.SSHConfig{}, regions.Table{}, "proj", "prd", globalScope)

		res := types.Resources{
			Network: []types.NetworkSpec{{
				Name: "corenet", Container: "core", Provider: "hetzner",
				CIDR:    "10.0.0.0/16",
				Subnets: []types.SubnetSpec{{Name: "main", CIDR: "10.0.0.0/24"}},
			}},
			Compute: []types.ComputeSpec{{
				Name: "edge", Container: "core", Provider: "hetzner",
				Network: "corenet", Size: "SMALL", Image: "ubuntu-24.04",
				Kind: "vm", InstanceCount: 1,
			}},
			Database: []types.DatabaseSpec{{
				Name: "shared", Container: "shared", Provider: "neon",
				Engine: "postgresql", Branch: "main", Database: "shared", Owner: "app",
			}},
		}

		return createInfra(ctx, reg, res, "prd", globalScope, "", "example.com",
			types.ProviderDefaults{}, networkOutputs, computeOutputs, databaseOutputs)
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	// Outputs land in the reserved "global" slot.
	require.Contains(t, computeOutputs, globalScope)
	assert.Contains(t, computeOutputs[globalScope], "edge-01", "global compute output under the global slot")
	assert.Contains(t, databaseOutputs[globalScope], "shared", "global database output under the global slot")
	assert.Contains(t, networkOutputs[globalScope], "corenet/main", "global subnet output under the global slot")

	// Names are region-less (no slug segment): the empty slug propagated through
	// naming.ResourceInstance and naming.Resource.
	assert.True(t, mocks.has(naming.ResourceInstance("prd", "", "vm", "edge", 1)),
		"global server should have a region-less name (wardnet-prd-vm-edge-01)")
	assert.True(t, mocks.has(naming.Resource("prd", "", "db", "shared")),
		"global database should have a region-less name (wardnet-prd-db-shared)")
}

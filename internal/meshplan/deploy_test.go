package meshplan

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/types"
)

func TestBuildDeployDescriptor(t *testing.T) {
	regional := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Service: []types.ServiceSpec{
			{Name: "tenants", Host: "bridge", Pki: "mesh"},
			{Name: "plain", Host: "bridge"}, // not a mesh member
		},
	}
	global := types.Resources{
		Compute: []types.ComputeSpec{{Name: "hub", InstanceCount: 1}},
		Service: []types.ServiceSpec{{Name: "core", Host: "hub", Pki: "mesh"}},
	}
	table := regions.Table{"us-east-1": {Slug: "use1"}}

	desc, err := BuildDeployDescriptor("prd", "wardnet.network", regional, global, table)
	require.NoError(t, err)
	require.Len(t, desc.Targets, 2, "one regional mesh host + one global mesh host")

	assert.Equal(t, DeployTarget{
		Host:    "bridge-01",
		HostDNS: "bridge.vm.prd.use1.wardnet.network",
		SSHUser: "deploy",
		Scope:   "us-east-1",
	}, desc.Targets[0])
	// The global slice is region-less: no region segment in the host FQDN.
	assert.Equal(t, DeployTarget{
		Host:    "hub-01",
		HostDNS: "hub.vm.prd.wardnet.network",
		SSHUser: "deploy",
		Scope:   pki.ScopeGlobal,
	}, desc.Targets[1])
}

func TestBuildDeployDescriptorNoMeshHosts(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "web", InstanceCount: 1}},
		Service: []types.ServiceSpec{{Name: "plain", Host: "web"}},
	}
	desc, err := BuildDeployDescriptor("prd", "wardnet.network", res, types.Resources{}, regions.Table{"us-east-1": {Slug: "use1"}})
	require.NoError(t, err)
	assert.Empty(t, desc.Targets)
}

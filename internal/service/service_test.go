package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/types"
)

func TestUnit(t *testing.T) {
	unit := Unit(types.ServiceSpec{Name: "api"})
	assert.Contains(t, unit, "Description=wardnet api")
	assert.Contains(t, unit, "WorkingDirectory=/srv/wardnet/api")
	// ExecStart is the bootstrapper pointed at the service's descriptor dir, not
	// the service binary directly — the bootstrapper execs that after dropping
	// privilege.
	assert.Contains(t, unit, "ExecStart=/usr/local/bin/inforge-bootstrap /etc/wardnet/services/api")
	assert.Contains(t, unit, "StartLimitIntervalSec=0", "unlimited restarts so a service recovers when the vault returns")
	assert.Contains(t, unit, "Restart=on-failure")
	assert.Contains(t, unit, "WantedBy=multi-user.target")
}

// TestUnitRunsAsRoot guards that the unit never sets User=: the unit runs as
// root and the bootstrapper drops privilege to the service user itself.
func TestUnitRunsAsRoot(t *testing.T) {
	unit := Unit(types.ServiceSpec{Name: "api", User: "wardnet"})
	assert.NotContains(t, unit, "User=", "the unit runs as root; the bootstrapper drops privilege itself")
}

func TestFolderAndUnitName(t *testing.T) {
	assert.Equal(t, "/srv/wardnet/api", Folder("api"))
	assert.Equal(t, "wardnet-api.service", UnitName("api"))
}

func TestDescriptorDirAndExecPath(t *testing.T) {
	assert.Equal(t, "/etc/wardnet/services/api", DescriptorDir("api"))
	assert.Equal(t, "/srv/wardnet/api/run", ExecPath("api"))
}

// singleRegionTable is a one-region table used by the descriptor tests that
// assert single-region output (one target per service).
func singleRegionTable() regions.Table {
	return regions.Table{"us-east-1": {Slug: "use1"}}
}

func TestBuildDeployDescriptor(t *testing.T) {
	res := types.Resources{
		Service: []types.ServiceSpec{
			{Name: "api", Host: "bridge-01", Type: "raw"},
			{Name: "worker", Host: "edge-01", Type: "raw"},
		},
	}

	desc, err := BuildDeployDescriptor("prd", "example.com", res, singleRegionTable())
	require.NoError(t, err)
	require.Len(t, desc.Targets, 2)

	byName := map[string]DeployTarget{}
	for _, tgt := range desc.Targets {
		byName[tgt.Service] = tgt
	}

	api := byName["api"]
	assert.Equal(t, "bridge.vm.prd.use1.example.com", api.HostDNS, "host DNS is the bare compute name with the vm segment, env+slug scoped")
	assert.Equal(t, "/srv/wardnet/api", api.Folder)
	assert.Equal(t, "wardnet-api.service", api.Unit)

	worker := byName["worker"]
	assert.Equal(t, "edge.vm.prd.use1.example.com", worker.HostDNS, "host DNS is derived from each service's own host compute")
}

// TestBuildDeployDescriptorMultiRegion asserts the shared resource set fans each
// service out into one target per region, with region-specific host DNS, in
// sorted region order.
func TestBuildDeployDescriptorMultiRegion(t *testing.T) {
	res := types.Resources{
		Service: []types.ServiceSpec{
			{Name: "api", Host: "bridge-01", Type: "raw"},
		},
	}
	table := regions.Table{
		"us-east-1":    {Slug: "use1"},
		"eu-central-1": {Slug: "euc1"},
	}

	desc, err := BuildDeployDescriptor("prd", "example.com", res, table)
	require.NoError(t, err)
	// One service × two regions -> two targets, sorted by region name.
	require.Len(t, desc.Targets, 2)

	hostDNSByService := map[string][]string{}
	for _, tgt := range desc.Targets {
		assert.Equal(t, "api", tgt.Service)
		hostDNSByService[tgt.Service] = append(hostDNSByService[tgt.Service], tgt.HostDNS)
	}
	// eu-central-1 sorts before us-east-1; both carry the same service, distinct slugs.
	assert.Equal(t, []string{
		"bridge.vm.prd.euc1.example.com",
		"bridge.vm.prd.use1.example.com",
	}, hostDNSByService["api"])
}

func TestBuildDeployDescriptorPropagatesUser(t *testing.T) {
	res := types.Resources{
		Service: []types.ServiceSpec{
			{Name: "api", Host: "bridge-01", Type: "raw", User: "wardnet"},
			{Name: "worker", Host: "bridge-01", Type: "raw"},
		},
	}
	desc, err := BuildDeployDescriptor("prd", "example.com", res, singleRegionTable())
	require.NoError(t, err)
	byName := map[string]DeployTarget{}
	for _, tgt := range desc.Targets {
		byName[tgt.Service] = tgt
	}
	assert.Equal(t, "wardnet", byName["api"].User)
	assert.Empty(t, byName["worker"].User)
}

func TestBuildDeployDescriptorSSHUser(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{
			{Name: "bridge", InstanceCount: 1, DeployUser: &types.DeployUserSpec{Name: "deployer"}},
			{Name: "edge", InstanceCount: 1}, // no deploy_user
		},
		Service: []types.ServiceSpec{
			{Name: "api", Host: "bridge-01", Type: "raw"},  // host declares deploy_user "deployer"
			{Name: "worker", Host: "edge-01", Type: "raw"}, // host declares none -> fallback
		},
	}
	desc, err := BuildDeployDescriptor("prd", "example.com", res, singleRegionTable())
	require.NoError(t, err)
	byName := map[string]DeployTarget{}
	for _, tgt := range desc.Targets {
		byName[tgt.Service] = tgt
	}
	// SSHUser (connect-as) is distinct from User (run-as).
	assert.Equal(t, "deployer", byName["api"].SSHUser, "uses the host's deploy_user")
	assert.Equal(t, "deploy", byName["worker"].SSHUser, "falls back to deploy when the host declares none")
}

func TestDeployDescriptorMarshal(t *testing.T) {
	desc := DeployDescriptor{
		Environment: "prd",
		Targets:     []DeployTarget{{Service: "api", HostDNS: "bridge.use1.example.com", Folder: "/srv/wardnet/api", Unit: "wardnet-api.service"}},
	}
	b, err := desc.Marshal()
	require.NoError(t, err)
	out := string(b)
	assert.Contains(t, out, "environment: prd")
	assert.Contains(t, out, "service: api")
	assert.Contains(t, out, "host_dns: bridge.use1.example.com")
	assert.Contains(t, out, "unit: wardnet-api.service")
}

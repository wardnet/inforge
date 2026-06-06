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

func TestBuildDeployDescriptor(t *testing.T) {
	byRegion := map[string]types.Resources{
		"us-east-1": {
			DNS: []types.DnsSpec{
				{Provider: "cloudflare", Compute: "bridge-01", Subdomain: "bridge"},
			},
			Service: []types.ServiceSpec{
				{Name: "api", Host: "bridge-01", Type: "raw"},
				{Name: "worker", Host: "bridge-02", Type: "raw"}, // no DNS record -> falls back to compute name
			},
		},
	}

	desc, err := BuildDeployDescriptor("prd", "example.com", byRegion, regions.DefaultTable())
	require.NoError(t, err)
	require.Len(t, desc.Targets, 2)

	byName := map[string]DeployTarget{}
	for _, tgt := range desc.Targets {
		byName[tgt.Service] = tgt
	}

	api := byName["api"]
	assert.Equal(t, "bridge.prd.use1.example.com", api.HostDNS, "uses the DNS record subdomain, with env")
	assert.Equal(t, "/srv/wardnet/api", api.Folder)
	assert.Equal(t, "wardnet-api.service", api.Unit)

	worker := byName["worker"]
	assert.Equal(t, "bridge.prd.use1.example.com", worker.HostDNS, "falls back to the compute name as subdomain, with env")
}

func TestBuildDeployDescriptorPropagatesUser(t *testing.T) {
	byRegion := map[string]types.Resources{
		"us-east-1": {
			Service: []types.ServiceSpec{
				{Name: "api", Host: "bridge-01", Type: "raw", User: "wardnet"},
				{Name: "worker", Host: "bridge-01", Type: "raw"},
			},
		},
	}
	desc, err := BuildDeployDescriptor("prd", "example.com", byRegion, regions.DefaultTable())
	require.NoError(t, err)
	byName := map[string]DeployTarget{}
	for _, tgt := range desc.Targets {
		byName[tgt.Service] = tgt
	}
	assert.Equal(t, "wardnet", byName["api"].User)
	assert.Empty(t, byName["worker"].User)
}

func TestBuildDeployDescriptorSSHUser(t *testing.T) {
	byRegion := map[string]types.Resources{
		"us-east-1": {
			Compute: []types.ComputeSpec{
				{Name: "bridge", InstanceCount: 1, DeployUser: &types.DeployUserSpec{Name: "deployer"}},
				{Name: "edge", InstanceCount: 1}, // no deploy_user
			},
			Service: []types.ServiceSpec{
				{Name: "api", Host: "bridge-01", Type: "raw"},  // host declares deploy_user "deployer"
				{Name: "worker", Host: "edge-01", Type: "raw"}, // host declares none -> fallback
			},
		},
	}
	desc, err := BuildDeployDescriptor("prd", "example.com", byRegion, regions.DefaultTable())
	require.NoError(t, err)
	byName := map[string]DeployTarget{}
	for _, tgt := range desc.Targets {
		byName[tgt.Service] = tgt
	}
	// SSHUser (connect-as) is distinct from User (run-as).
	assert.Equal(t, "deployer", byName["api"].SSHUser, "uses the host's deploy_user")
	assert.Equal(t, "deploy", byName["worker"].SSHUser, "falls back to deploy when the host declares none")
}

func TestBuildDeployDescriptorUnknownRegion(t *testing.T) {
	byRegion := map[string]types.Resources{
		"mars-1": {Service: []types.ServiceSpec{{Name: "api", Host: "bridge-01"}}},
	}
	_, err := BuildDeployDescriptor("prd", "example.com", byRegion, regions.DefaultTable())
	assert.Error(t, err)
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

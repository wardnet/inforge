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
	assert.Contains(t, unit, "ExecStart=/srv/wardnet/api/run")
	assert.Contains(t, unit, "WantedBy=multi-user.target")
}

func TestFolderAndUnitName(t *testing.T) {
	assert.Equal(t, "/srv/wardnet/api", Folder("api"))
	assert.Equal(t, "wardnet-api.service", UnitName("api"))
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
	assert.Equal(t, "bridge.use1.example.com", api.HostDNS, "uses the DNS record subdomain")
	assert.Equal(t, "/srv/wardnet/api", api.Folder)
	assert.Equal(t, "wardnet-api.service", api.Unit)

	worker := byName["worker"]
	assert.Equal(t, "bridge.use1.example.com", worker.HostDNS, "falls back to the compute name as subdomain")
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

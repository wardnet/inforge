package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/types"
)

// TestBuildDeployDescriptor: each app expands into one target per region, with the
// ingress host DNS, deploy path, regional app FQDN, SPA flag, and the ingress
// host's deploy user resolved.
func TestBuildDeployDescriptor(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{
			{Name: "edge", InstanceCount: 1, DeployUser: &types.DeployUserSpec{Name: "ops"}},
		},
		Ingress: []types.IngressSpec{{Name: "web", Host: "edge"}},
		App: []types.AppSpec{
			{Name: "dashboard", Ingress: "web", Subdomain: "my", Spa: true},
		},
	}
	table := regions.Table{"us-east-1": {Slug: "use1"}}

	desc, err := BuildDeployDescriptor("prd", "wardnet.network", res, table)
	require.NoError(t, err)
	require.Len(t, desc.Targets, 1)
	tg := desc.Targets[0]
	assert.Equal(t, "dashboard", tg.App)
	assert.Equal(t, "edge.vm.prd.use1.wardnet.network", tg.IngressHostDNS)
	assert.Equal(t, "/srv/wardnet/app/dashboard", tg.DeployPath)
	assert.Equal(t, "my.use1.wardnet.network", tg.FQDN)
	assert.True(t, tg.Spa)
	assert.Equal(t, "ops", tg.SSHUser)
}

// TestBuildDeployDescriptorDefaultSSHUser: an ingress host with no deploy_user
// falls back to the historical "deploy" account.
func TestBuildDeployDescriptorDefaultSSHUser(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "edge", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "web", Host: "edge"}},
		App:     []types.AppSpec{{Name: "my", Ingress: "web", Subdomain: "my"}},
	}
	desc, err := BuildDeployDescriptor("prd", "wardnet.network", res, regions.Table{"us-east-1": {Slug: "use1"}})
	require.NoError(t, err)
	require.Len(t, desc.Targets, 1)
	assert.Equal(t, "deploy", desc.Targets[0].SSHUser)
}

// TestBuildDeployDescriptorSkipsUnresolvedIngress: an app whose ingress does not
// resolve is skipped rather than emitted with a blank host.
func TestBuildDeployDescriptorSkipsUnresolvedIngress(t *testing.T) {
	res := types.Resources{
		App: []types.AppSpec{{Name: "orphan", Ingress: "missing", Subdomain: "x"}},
	}
	desc, err := BuildDeployDescriptor("prd", "wardnet.network", res, regions.Table{"us-east-1": {Slug: "use1"}})
	require.NoError(t, err)
	assert.Empty(t, desc.Targets)
}

// TestPaths pins the on-host path scheme apps and the release path agree on.
func TestPaths(t *testing.T) {
	assert.Equal(t, "/srv/wardnet/app/my", Folder("my"))
	assert.Equal(t, "/srv/wardnet/app/my/current", CurrentPath("my"))
	assert.Equal(t, "/srv/wardnet/app/my/placeholder/index.html", PlaceholderIndexPath("my"))
}

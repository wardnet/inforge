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
	// ExecStart is the agent pointed at the service's descriptor dir, not
	// the service binary directly — the agent execs that after dropping
	// privilege.
	assert.Contains(t, unit, "ExecStart=/usr/local/bin/inforge-agent /etc/wardnet/services/api")
	assert.Contains(t, unit, "StartLimitIntervalSec=0", "unlimited restarts so a service recovers when the vault returns")
	assert.Contains(t, unit, "Restart=on-failure")
	assert.Contains(t, unit, "WantedBy=multi-user.target")
}

// TestUnitConditionPathExists guards the gap between provisioning (which
// enables the unit for boot-persistence) and a service's first-ever release:
// without this condition, a freshly provisioned host's first boot
// auto-starts every enabled unit regardless of whether any payload was ever
// delivered, crash-looping forever (StartLimitIntervalSec=0 disables the
// rate limit) until someone runs `inforge releases deploy`. A failed
// Condition is not a failure exit, so Restart= never engages — systemd just
// skips the start silently until ExecPath exists.
func TestUnitConditionPathExists(t *testing.T) {
	unit := Unit(types.ServiceSpec{Name: "api"})
	assert.Contains(t, unit, "ConditionPathExists=/srv/wardnet/api/run", "must match ExecPath so systemd skips starting until the payload is delivered")
}

// TestUnitRunsAsRoot guards that the unit never sets User=: the unit runs as
// root and the agent drops privilege to the service user itself.
func TestUnitRunsAsRoot(t *testing.T) {
	unit := Unit(types.ServiceSpec{Name: "api", User: "wardnet"})
	assert.NotContains(t, unit, "User=", "the unit runs as root; the agent drops privilege itself")
}

func TestFolderAndUnitName(t *testing.T) {
	assert.Equal(t, "/srv/wardnet/api", Folder("api"))
	assert.Equal(t, "wardnet-api.service", UnitName("api"))
}

func TestUnitRuntimeDirectoryAndReload(t *testing.T) {
	// RuntimeDirectory is always present (the agent projects mesh PEMs into
	// it); ExecReload only when the service declares reload:.
	withReload := Unit(types.ServiceSpec{Name: "api", User: "api", Reload: "/bin/kill -HUP $MAINPID"})
	assert.Contains(t, withReload, "RuntimeDirectory=wardnet/api")
	// 0700 so the dir holding the leaf key is owner-only (the leaf key is 0400).
	assert.Contains(t, withReload, "RuntimeDirectoryMode=0700")
	assert.Contains(t, withReload, "ExecReload=/bin/kill -HUP $MAINPID")

	noReload := Unit(types.ServiceSpec{Name: "api", User: "api"})
	assert.Contains(t, noReload, "RuntimeDirectory=wardnet/api")
	assert.NotContains(t, noReload, "ExecReload")
}

func TestRuntimeDir(t *testing.T) {
	assert.Equal(t, "/run/wardnet/api", RuntimeDir("api"))
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

	desc, err := BuildDeployDescriptor("prd", "example.com", res, types.Resources{}, singleRegionTable())
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

// TestBuildDeployDescriptorGlobal guards the bug this function was fixed for:
// a container: global service (e.g. tenants) must land in the descriptor
// exactly once, with a region-less host DNS — `inforge releases push/deploy`
// resolves a service purely from this descriptor, so a global service missing
// here means it's undeployable via that path even though `inforge deploy`
// fully realizes it.
func TestBuildDeployDescriptorGlobal(t *testing.T) {
	global := types.Resources{
		Service: []types.ServiceSpec{
			{Name: "tenants", Host: "tenants-01", Type: "raw"},
		},
	}
	desc, err := BuildDeployDescriptor("prd", "example.com", types.Resources{}, global, singleRegionTable())
	require.NoError(t, err)
	require.Len(t, desc.Targets, 1)
	assert.Equal(t, "tenants", desc.Targets[0].Service)
	assert.Equal(t, "tenants.vm.prd.example.com", desc.Targets[0].HostDNS, "global host DNS has no region slug segment")
}

// TestBuildDeployDescriptorRegionalAndGlobal guards that regional and global
// services coexist in one descriptor without interfering with each other.
func TestBuildDeployDescriptorRegionalAndGlobal(t *testing.T) {
	res := types.Resources{
		Service: []types.ServiceSpec{{Name: "api", Host: "bridge-01", Type: "raw"}},
	}
	global := types.Resources{
		Service: []types.ServiceSpec{{Name: "tenants", Host: "tenants-01", Type: "raw"}},
	}
	desc, err := BuildDeployDescriptor("prd", "example.com", res, global, singleRegionTable())
	require.NoError(t, err)
	require.Len(t, desc.Targets, 2)

	byName := map[string]DeployTarget{}
	for _, tgt := range desc.Targets {
		byName[tgt.Service] = tgt
	}
	assert.Equal(t, "bridge.vm.prd.use1.example.com", byName["api"].HostDNS)
	assert.Equal(t, "tenants.vm.prd.example.com", byName["tenants"].HostDNS)
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

	desc, err := BuildDeployDescriptor("prd", "example.com", res, types.Resources{}, table)
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
	desc, err := BuildDeployDescriptor("prd", "example.com", res, types.Resources{}, singleRegionTable())
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
	desc, err := BuildDeployDescriptor("prd", "example.com", res, types.Resources{}, singleRegionTable())
	require.NoError(t, err)
	byName := map[string]DeployTarget{}
	for _, tgt := range desc.Targets {
		byName[tgt.Service] = tgt
	}
	// SSHUser (connect-as) is distinct from User (run-as).
	assert.Equal(t, "deployer", byName["api"].SSHUser, "uses the host's deploy_user")
	assert.Equal(t, "deploy", byName["worker"].SSHUser, "falls back to deploy when the host declares none")
}

// TestBuildDeployDescriptorGlobalService asserts a global service contributes
// exactly one region-less target (no region segment in its host DNS),
// regardless of how many regions the table lists — the regression this guards
// is a global service silently missing from deployDescriptor entirely.
func TestBuildDeployDescriptorGlobalService(t *testing.T) {
	regional := types.Resources{
		Service: []types.ServiceSpec{
			{Name: "api", Host: "bridge-01", Type: "raw"},
		},
	}
	global := types.Resources{
		Service: []types.ServiceSpec{
			{Name: "tenants", Host: "core-01", Type: "raw"},
		},
	}
	table := regions.Table{
		"us-east-1":    {Slug: "use1"},
		"eu-central-1": {Slug: "euc1"},
	}

	desc, err := BuildDeployDescriptor("prd", "example.com", regional, global, table)
	require.NoError(t, err)
	// One regional service x two regions, plus one global service once.
	require.Len(t, desc.Targets, 3)

	byName := map[string][]DeployTarget{}
	for _, tgt := range desc.Targets {
		byName[tgt.Service] = append(byName[tgt.Service], tgt)
	}
	require.Len(t, byName["api"], 2)
	require.Len(t, byName["tenants"], 1)
	assert.Equal(t, "core.vm.prd.example.com", byName["tenants"][0].HostDNS, "global service host DNS carries no region segment")
	assert.Equal(t, "tenants", byName["tenants"][0].Service)
	assert.Equal(t, "global", byName["tenants"][0].Scope, "a global target's Scope is pki.ScopeGlobal")
	for _, tgt := range byName["api"] {
		assert.Contains(t, []string{"us-east-1", "eu-central-1"}, tgt.Scope, "a regional target's Scope is its abstract region name")
	}
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

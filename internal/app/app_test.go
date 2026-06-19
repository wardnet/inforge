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

// TestBundleDir: each release SHA gets its own sibling directory under the app
// folder — the basis for rollback-by-symlink.
func TestBundleDir(t *testing.T) {
	assert.Equal(t, "/srv/wardnet/app/my/abc123", BundleDir("my", "abc123"))
}

// TestSwapCurrentScript: the swap stages a relative symlink under a temp name and
// renames it over `current` with `mv -T`, so the document root flips atomically.
// The target is the bare SHA (relative), not an absolute path.
func TestSwapCurrentScript(t *testing.T) {
	got := SwapCurrentScript("my", "abc123")
	want := "sudo ln -sfn 'abc123' '/srv/wardnet/app/my/.current.tmp' && " +
		"sudo mv -T '/srv/wardnet/app/my/.current.tmp' '/srv/wardnet/app/my/current'"
	assert.Equal(t, want, got)
}

// TestGCReleasesScript: GC excludes the placeholder, the literal `current` symlink,
// and whatever `current` resolves to, keeping the newest KeepReleases bundles.
func TestGCReleasesScript(t *testing.T) {
	got := GCReleasesScript("my")
	assert.Contains(t, got, "cd '/srv/wardnet/app/my'")
	assert.Contains(t, got, "current=$(readlink current")
	assert.Contains(t, got, "grep -vx 'placeholder'")
	assert.Contains(t, got, "grep -vx current")
	assert.Contains(t, got, `grep -vx "$current"`)
	// KeepReleases newest are retained: tail starts one past the kept window.
	assert.Contains(t, got, "tail -n +6")
	assert.Contains(t, got, "xargs -r sudo rm -rf")
}

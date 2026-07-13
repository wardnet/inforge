package program

import (
	"testing"

	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/types"
)

// TestServiceRestartWaitsForEveryMeshReload is the regression guard for #226.
//
// A service's delivery command ends in reloadOrRestartScript — the restart that brings it up
// on its new configuration. If that lands before a CALLEE's mesh proxy has reloaded, the
// caller presents a valid leaf whose identity the callee's allow-map does not yet list, and
// `meshnginx`'s guard correctly 403s every call until the reload catches up.
//
// Declaration order does NOT prevent this. `program.Run` declares realizeMesh before
// provisionServices, and realizes the global scope first — but **Pulumi is a DAG, not a
// script**. The loop only declares; the engine runs anything unordered CONCURRENTLY. A
// side-effecting remote.Command produces no output a later resource consumes, so absent an
// explicit edge the restart and the reload race. Observed in prd: ddns restarted at 15:25:10,
// tenants' mesh proxy reloaded at 15:25:11, and every call in between was 403'd.
//
// So the edge must be explicit, and this test is what keeps it.
func TestServiceRestartWaitsForEveryMeshReload(t *testing.T) {
	mocks := newCommandMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		// Stand-ins for two callees' mesh nginx reloads — in production these are what
		// MeshProvider.Realize returns, accumulated across scopes. Two of them because a
		// regional caller depends on its OWN scope's reload *and* the global scope's: the
		// cross-scope hop (regional caller -> global callee) is exactly what 403'd.
		reloads := make([]pulumi.Resource, 0, 2)
		for _, name := range []string{"global-mesh-reload", "regional-mesh-reload"} {
			r, err := remote.NewCommand(ctx, name, &remote.CommandArgs{
				Connection: remote.ConnectionArgs{
					Host:       pulumi.String("1.2.3.4"),
					User:       pulumi.String("deploy"),
					PrivateKey: pulumi.String("priv"),
				},
				Create: pulumi.String("systemctl reload wardnet-mesh"),
			})
			if err != nil {
				return err
			}
			reloads = append(reloads, r)
		}

		res := types.Resources{
			Compute: []types.ComputeSpec{
				{Name: "bridge", InstanceCount: 1, DeployUser: &types.DeployUserSpec{Name: "deploy"}},
			},
			Service: []types.ServiceSpec{
				{Name: "ghost", Container: "ghost", Host: "bridge-01", Type: "raw", User: "ghost", Pki: "wardnet-mesh"},
			},
		}
		computeOut := map[string]types.ComputeOutputs{
			"bridge-01": {PublicIP: pulumi.String("1.2.3.4").ToStringOutput()},
		}
		// A secret-bearing service takes the deliverServiceSecrets path — the one that
		// restarts.
		material := map[string]serviceMaterial{
			"ghost": {Env: map[string]pulumi.StringOutput{"TOKEN": pulumi.String("t").ToStringOutput()}},
		}
		return provisionServices(ctx, res, computeOut, material, map[string]pulumi.Resource{},
			"priv", "prd", "us-east-1", "use1", "example.com", "1.2.3", reloads)
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	svc := naming.Resource("prd", "use1", "svc", "ghost")
	assert.True(t, mocks.dependsOn(svc+"-secrets", "global-mesh-reload"),
		"the restart must wait on the GLOBAL mesh reload — the cross-scope hop (regional caller -> global callee) is what 403'd")
	assert.True(t, mocks.dependsOn(svc+"-secrets", "regional-mesh-reload"),
		"the restart must wait on its own scope's mesh reload too")
	// The pre-existing unit ordering must survive (deploy-must-never-leave-a-service-stopped).
	assert.True(t, mocks.dependsOn(svc+"-secrets", svc+"-provision"),
		"the restart must still wait on the unit provision")
}

// A service with no secrets takes deliverServiceDescriptor, which writes a file and does NOT
// restart — so it needs no mesh ordering. Depending on the mesh there would add a pointless
// edge and, worse, imply a restart ordering that path does not have.
func TestSecretlessDeliveryNeedsNoMeshOrdering(t *testing.T) {
	mocks := newCommandMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		reload, err := remote.NewCommand(ctx, "mesh-reload", &remote.CommandArgs{
			Connection: remote.ConnectionArgs{
				Host:       pulumi.String("1.2.3.4"),
				User:       pulumi.String("deploy"),
				PrivateKey: pulumi.String("priv"),
			},
			Create: pulumi.String("systemctl reload wardnet-mesh"),
		})
		if err != nil {
			return err
		}
		res := types.Resources{
			Compute: []types.ComputeSpec{
				{Name: "bridge", InstanceCount: 1, DeployUser: &types.DeployUserSpec{Name: "deploy"}},
			},
			Service: []types.ServiceSpec{
				{Name: "ghost", Container: "ghost", Host: "bridge-01", Type: "raw", User: "ghost"},
			},
		}
		computeOut := map[string]types.ComputeOutputs{
			"bridge-01": {PublicIP: pulumi.String("1.2.3.4").ToStringOutput()},
		}
		return provisionServices(ctx, res, computeOut, map[string]serviceMaterial{}, map[string]pulumi.Resource{},
			"priv", "prd", "us-east-1", "use1", "example.com", "1.2.3", []pulumi.Resource{reload})
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	svc := naming.Resource("prd", "use1", "svc", "ghost")
	assert.False(t, mocks.dependsOn(svc+"-secrets", "mesh-reload"),
		"the descriptor write does not restart, so it must not be ordered against the mesh")
}

// The #226 fix rests on the global scope realizing FIRST: meshReloads accumulates across
// scopes, so a regional service's restart depends on the GLOBAL mesh's reload only because
// the global scope came first. That is exactly the cross-scope hop the 403s came from.
//
// Nothing else enforces it — reversing two `append` calls twenty lines apart would silently
// re-open the race with no test failure and no error. So the invariant is asserted, and a
// loud failure at deploy beats a quiet 403 storm in production.
func TestGlobalScopeMustRealizeFirst(t *testing.T) {
	assert.NoError(t, requireGlobalScopeFirst([]string{globalScope, "eu-central", "us-east-1"}))
	assert.NoError(t, requireGlobalScopeFirst([]string{"eu-central"}), "a regional-only env has no global mesh to order against")
	assert.NoError(t, requireGlobalScopeFirst(nil))

	err := requireGlobalScopeFirst([]string{"eu-central", globalScope})
	require.Error(t, err, "a global scope realized AFTER a region silently re-opens #226")
	assert.Contains(t, err.Error(), "must realize BEFORE")
}

// A service with no `pki:` is not a mesh member: it never dials the mesh and no callee's
// allow-map has an entry for it, so it CANNOT be 403'd. Ordering it behind every mesh host's
// nginx install would serialize the deploy behind apt-get runs on machines it has nothing to
// do with.
func TestANonMeshServiceIsNotOrderedAgainstTheMesh(t *testing.T) {
	mocks := newCommandMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		reload, err := remote.NewCommand(ctx, "mesh-reload", &remote.CommandArgs{
			Connection: remote.ConnectionArgs{
				Host:       pulumi.String("1.2.3.4"),
				User:       pulumi.String("deploy"),
				PrivateKey: pulumi.String("priv"),
			},
			Create: pulumi.String("systemctl reload wardnet-mesh"),
		})
		if err != nil {
			return err
		}
		res := types.Resources{
			Compute: []types.ComputeSpec{
				{Name: "bridge", InstanceCount: 1, DeployUser: &types.DeployUserSpec{Name: "deploy"}},
			},
			Service: []types.ServiceSpec{
				// No Pki -> not a mesh member.
				{Name: "ghost", Container: "ghost", Host: "bridge-01", Type: "raw", User: "ghost"},
			},
		}
		computeOut := map[string]types.ComputeOutputs{
			"bridge-01": {PublicIP: pulumi.String("1.2.3.4").ToStringOutput()},
		}
		material := map[string]serviceMaterial{
			"ghost": {Env: map[string]pulumi.StringOutput{"TOKEN": pulumi.String("t").ToStringOutput()}},
		}
		return provisionServices(ctx, res, computeOut, material, map[string]pulumi.Resource{},
			"priv", "prd", "us-east-1", "use1", "example.com", "1.2.3", []pulumi.Resource{reload})
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	svc := naming.Resource("prd", "use1", "svc", "ghost")
	assert.False(t, mocks.dependsOn(svc+"-secrets", "mesh-reload"),
		"a non-mesh service cannot be 403'd, so it must not wait on the mesh")
	// It must still keep the unit ordering it always had.
	assert.True(t, mocks.dependsOn(svc+"-secrets", svc+"-provision"))
}

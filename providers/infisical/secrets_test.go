package infisical

import (
	"sync"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/types"
)

// --- naming-convention tests --------------------------------------------------

// capturedCall records the Pulumi logical name and inputs of a registered resource.
type capturedCall struct {
	logicalName string
	inputs      resource.PropertyMap
}

// namingMocks captures every RegisterResource call keyed by Pulumi type token.
type namingMocks struct {
	mu       sync.Mutex
	captured map[string]capturedCall
}

func newNamingMocks() *namingMocks {
	return &namingMocks{captured: map[string]capturedCall{}}
}

func (m *namingMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *namingMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	m.captured[args.TypeToken] = capturedCall{logicalName: args.Name, inputs: args.Inputs}
	m.mu.Unlock()

	outputs := resource.PropertyMap{}
	if args.TypeToken == infisicalWorkspaceType {
		outputs["workspaceId"] = resource.NewStringProperty("ws-test-id")
	}
	if args.TypeToken == infisicalIdentityType {
		outputs["authClientId"] = resource.NewStringProperty("client-id")
		outputs["authClientSecret"] = resource.NewStringProperty("client-secret")
	}
	return args.Name + "-id", outputs, nil
}

// TestProvisionServiceScopesPaths verifies a service's infra secrets are written
// under /<svc>/infra while its identity is scoped read to /<svc>, and that the
// bundle carries the env-var -> infra/<key> mapping the descriptor needs.
func TestProvisionServiceScopesPaths(t *testing.T) {
	mocks := newNamingMocks()
	var bundle *types.ServiceSecretsBundle
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "", "", "use1")
		res := types.Resources{
			Secrets: []types.SecretsSpec{{
				Name:      "ghost-secrets",
				Container: "ghost",
				Provider:  "infisical",
				Secrets:   map[string]types.SecretsEntry{"DATABASE_URL": {Source: "gha:DATABASE_URL"}},
			}},
		}
		svc := types.ServiceSpec{Name: "ghost", Container: "ghost", Provider: "raw", User: "ghost"}
		var err error
		bundle, err = adapter.ProvisionService(ctx, svc, res, "prd", "us-east-1", types.AllOutputs{})
		return err
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	assert.Equal(t, "/ghost/infra", mocks.captured[infisicalSecretsBatchType].inputs["secretPath"].StringValue())
	assert.Equal(t, "/ghost", mocks.captured[infisicalIdentityType].inputs["secretPath"].StringValue())
	require.NotNil(t, bundle)
	assert.Equal(t, "/ghost", bundle.SecretPath)
	assert.Equal(t, map[string]string{"DATABASE_URL": "infra/DATABASE_URL"}, bundle.Env)
}

// TestProvisionServicePassesOrganizationId verifies the adapter's configured
// organizationId is threaded onto the identity resource input, so a deployment
// whose token carries no organizationId claim can still scope the identity.
func TestProvisionServicePassesOrganizationId(t *testing.T) {
	mocks := newNamingMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "", "org-explicit", "use1")
		res := types.Resources{
			Secrets: []types.SecretsSpec{{
				Name:      "ghost-secrets",
				Container: "ghost",
				Provider:  "infisical",
				Secrets:   map[string]types.SecretsEntry{"DATABASE_URL": {Source: "gha:DATABASE_URL"}},
			}},
		}
		svc := types.ServiceSpec{Name: "ghost", Container: "ghost", Provider: "raw", User: "ghost"}
		_, err := adapter.ProvisionService(ctx, svc, res, "prd", "us-east-1", types.AllOutputs{})
		return err
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	assert.Equal(t, "org-explicit", mocks.captured[infisicalIdentityType].inputs["organizationId"].StringValue())
}

// TestProvisionServiceNoSecretsReturnsNil verifies a service whose container has
// no infisical secrets yields no bundle and provisions nothing.
func TestProvisionServiceNoSecretsReturnsNil(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "", "", "use1")
		svc := types.ServiceSpec{Name: "ghost", Container: "ghost"}
		bundle, err := adapter.ProvisionService(ctx, svc, types.Resources{}, "prd", "us-east-1", types.AllOutputs{})
		require.NoError(t, err)
		assert.Nil(t, bundle)
		return nil
	}, pulumi.WithMocks("project", "stack", &infisicalMocks{}))
	require.NoError(t, err)
}

// TestInfraSecretEntries verifies the derivation merges only infisical
// SecretsSpecs in the service's own container.
func TestInfraSecretEntries(t *testing.T) {
	res := types.Resources{
		Secrets: []types.SecretsSpec{
			{Container: "ghost", Provider: "infisical", Secrets: map[string]types.SecretsEntry{"A": {Source: "gha:A"}}},
			{Container: "ghost", Provider: "infisical", Secrets: map[string]types.SecretsEntry{"B": {Source: "gha:B"}}},
			{Container: "ghost", Provider: "other", Secrets: map[string]types.SecretsEntry{"SKIP": {Source: "gha:SKIP"}}},
			{Container: "other", Provider: "infisical", Secrets: map[string]types.SecretsEntry{"NOPE": {Source: "gha:NOPE"}}},
		},
	}
	got, err := infraSecretEntries(types.ServiceSpec{Name: "ghost", Container: "ghost"}, res)
	require.NoError(t, err)
	assert.Equal(t, map[string]types.SecretsEntry{
		"A": {Source: "gha:A"},
		"B": {Source: "gha:B"},
	}, got)
}

// TestInfraSecretEntriesRejectsDuplicateKey: the same key declared by two specs
// in the same container is ambiguous and must error, not silently last-win.
func TestInfraSecretEntriesRejectsDuplicateKey(t *testing.T) {
	res := types.Resources{
		Secrets: []types.SecretsSpec{
			{Container: "ghost", Provider: "infisical", Secrets: map[string]types.SecretsEntry{"DUP": {Source: "gha:ONE"}}},
			{Container: "ghost", Provider: "infisical", Secrets: map[string]types.SecretsEntry{"DUP": {Source: "gha:TWO"}}},
		},
	}
	_, err := infraSecretEntries(types.ServiceSpec{Name: "ghost", Container: "ghost"}, res)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `secret key "DUP"`)
}

// bridgeServiceWithSecret is a single-service / single-secret fixture for the
// naming tests: a service named "bridge" in container "bridge" with one infra
// secret, so ProvisionService creates the workspace + batch + identity.
func bridgeServiceWithSecret() (types.ServiceSpec, types.Resources) {
	svc := types.ServiceSpec{Name: "bridge", Container: "bridge", Provider: "raw", User: "bridge"}
	res := types.Resources{
		Secrets: []types.SecretsSpec{{
			Name:      "bridge",
			Container: "bridge",
			Provider:  "infisical",
			Secrets:   map[string]types.SecretsEntry{"MY_SECRET": {Source: "gha:MY_SECRET"}},
		}},
	}
	return svc, res
}

// TestWorkspaceNamePassedToAPIMatchesNamingConvention verifies that the name
// field sent to the Infisical API matches the full naming convention
// (wardnet-<env>-<regionSlug>-container-<container>), using the environment
// ("prd") not the abstract region ("us-east-1") as the env segment.
func TestWorkspaceNamePassedToAPIMatchesNamingConvention(t *testing.T) {
	mocks := newNamingMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "", "", "use1")
		svc, res := bridgeServiceWithSecret()
		// env="prd", region="us-east-1" — workspace name must use env, not region.
		_, err := adapter.ProvisionService(ctx, svc, res, "prd", "us-east-1", types.AllOutputs{})
		return err
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	want := naming.Resource("prd", "use1", "container", "bridge")
	got := mocks.captured[infisicalWorkspaceType].inputs["name"].StringValue()
	assert.Equal(t, want, got,
		"workspace name must use env (prd) not region (us-east-1) as the env segment")
}

// TestSecretsBatchNameMatchesNamingConvention verifies that the Pulumi logical
// name of an Infisical secrets batch follows
// wardnet-<env>-<regionSlug>-secrets-<specName>.
func TestSecretsBatchNameMatchesNamingConvention(t *testing.T) {
	mocks := newNamingMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "", "", "use1")
		svc, res := bridgeServiceWithSecret()
		_, err := adapter.ProvisionService(ctx, svc, res, "prd", "us-east-1", types.AllOutputs{})
		return err
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	want := naming.Resource("prd", "use1", "secrets", "bridge")
	got := mocks.captured[infisicalSecretsBatchType].logicalName
	assert.Equal(t, want, got,
		"Infisical secrets batch Pulumi logical name must follow naming convention")
}

// Compile-time assertion: InfisicalSecretsAdapter satisfies the provisioner.
var _ types.ServiceSecretsProvisioner = (*InfisicalSecretsAdapter)(nil)

// infisicalMocks is a minimal Pulumi mock monitor for secrets adapter tests.
type infisicalMocks struct{}

func (m *infisicalMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *infisicalMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	outputs := resource.PropertyMap{}
	if args.TypeToken == infisicalWorkspaceType {
		outputs["workspaceId"] = resource.NewStringProperty("ws-test-id")
	}
	return args.Name + "-id", outputs, nil
}

// awaitString resolves a StringOutput to its concrete value (or fails on timeout).
func awaitString(t *testing.T, o pulumi.StringOutput) string {
	t.Helper()
	ch := make(chan string, 1)
	o.ApplyT(func(s string) string { ch <- s; return s })
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timeout awaiting output")
		return ""
	}
}

// TestResolveRefGlobalDatabase verifies a global/ prefix redirects a database ref
// to the region-less global slot, regardless of the consuming service's region.
func TestResolveRefGlobalDatabase(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		all := types.AllOutputs{
			Database: map[string]map[string]types.DatabaseOutputs{
				"us-east-1": {"shared": {ConnectionURL: pulumi.String("regional-url").ToStringOutput()}},
				"global":    {"shared": {ConnectionURL: pulumi.String("global-url").ToStringOutput()}},
			},
		}
		out, err := resolveRef("ref:database/global/shared.connectionUrl", "us-east-1", all)
		require.NoError(t, err)
		assert.Equal(t, "global-url", awaitString(t, out), "global/ must resolve against the global slot, not the service's region")
		return nil
	}, pulumi.WithMocks("project", "stack", &infisicalMocks{}))
	require.NoError(t, err)
}

// TestResolveRefStatic verifies a static/value source resolves to its literal,
// verbatim — no resource lookup, no placeholder.
func TestResolveRefStatic(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		for _, src := range []string{"static:info", "value:info"} {
			out, err := resolveRef(src, "us-east-1", types.AllOutputs{})
			require.NoError(t, err)
			assert.Equal(t, "info", awaitString(t, out), src)
		}
		// A value with special characters (URL) is preserved verbatim.
		out, err := resolveRef("value:https://api.example.com:443/v1", "us-east-1", types.AllOutputs{})
		require.NoError(t, err)
		assert.Equal(t, "https://api.example.com:443/v1", awaitString(t, out))
		return nil
	}, pulumi.WithMocks("project", "stack", &infisicalMocks{}))
	require.NoError(t, err)
}

// TestResolveRefGlobalCompute verifies the same redirect for a compute ref.
func TestResolveRefGlobalCompute(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		all := types.AllOutputs{
			Compute: map[string]map[string]types.ComputeOutputs{
				"global": {"edge-01": {PublicIP: pulumi.String("203.0.113.7").ToStringOutput()}},
			},
		}
		out, err := resolveRef("ref:compute/global/edge-01.publicIp", "us-east-1", all)
		require.NoError(t, err)
		assert.Equal(t, "203.0.113.7", awaitString(t, out))
		return nil
	}, pulumi.WithMocks("project", "stack", &infisicalMocks{}))
	require.NoError(t, err)
}

// TestResolveRefGlobalMissing verifies a global ref to an absent name fails
// against the global slot (not the service's region).
func TestResolveRefGlobalMissing(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		all := types.AllOutputs{
			Database: map[string]map[string]types.DatabaseOutputs{
				"global": {},
			},
		}
		_, err := resolveRef("ref:database/global/missing.connectionUrl", "us-east-1", all)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `no database "missing" in region "global"`)
		return nil
	}, pulumi.WithMocks("project", "stack", &infisicalMocks{}))
	require.NoError(t, err)
}

// TestEnsureWorkspaceIdempotent verifies that the same (container, env) pair
// returns the same workspace resource on repeated calls.
func TestEnsureWorkspaceIdempotent(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "", "", "use1")

		first, err := adapter.ensureWorkspace(ctx, "mycontainer", "prd")
		if err != nil {
			return err
		}
		second, err := adapter.ensureWorkspace(ctx, "mycontainer", "prd")
		if err != nil {
			return err
		}

		// Both calls must return the same StringOutput (same underlying promise).
		if first != second {
			t.Error("ensureWorkspace returned different outputs for the same key")
		}
		return nil
	}, pulumi.WithMocks("project", "stack", &infisicalMocks{}))
	require.NoError(t, err)
}

// TestEnsureWorkspaceDifferentEnvsAreIndependent verifies that distinct
// (container, env) pairs produce separate workspace resources.
func TestEnsureWorkspaceDifferentEnvsAreIndependent(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "", "", "use1")

		prd, err := adapter.ensureWorkspace(ctx, "mycontainer", "prd")
		if err != nil {
			return err
		}
		stg, err := adapter.ensureWorkspace(ctx, "mycontainer", "stg")
		if err != nil {
			return err
		}

		if prd == stg {
			t.Error("ensureWorkspace returned the same output for different envs")
		}
		return nil
	}, pulumi.WithMocks("project", "stack", &infisicalMocks{}))
	require.NoError(t, err)
}

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
	t.Setenv("DATABASE_URL", "postgres://example/db")
	mocks := newNamingMocks()
	var bundle *types.ServiceSecretsBundle
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "", "", "use1")
		svc := types.ServiceSpec{
			Name: "ghost", Container: "ghost", User: "ghost",
			Environment: map[string]string{"DATABASE_URL": "env:DATABASE_URL"},
		}
		var err error
		bundle, err = adapter.ProvisionService(ctx, svc, "prd", "us-east-1", types.AllOutputs{}, nil)
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
	t.Setenv("DATABASE_URL", "postgres://example/db")
	mocks := newNamingMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "", "org-explicit", "use1")
		svc := types.ServiceSpec{
			Name: "ghost", Container: "ghost", User: "ghost",
			Environment: map[string]string{"DATABASE_URL": "env:DATABASE_URL"},
		}
		_, err := adapter.ProvisionService(ctx, svc, "prd", "us-east-1", types.AllOutputs{}, nil)
		return err
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	assert.Equal(t, "org-explicit", mocks.captured[infisicalIdentityType].inputs["organizationId"].StringValue())
}

// TestProvisionServiceNoSecretsReturnsNil verifies a service with no secrets
// yields no bundle and provisions nothing.
func TestProvisionServiceNoSecretsReturnsNil(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "", "", "use1")
		svc := types.ServiceSpec{Name: "ghost", Container: "ghost"}
		bundle, err := adapter.ProvisionService(ctx, svc, "prd", "us-east-1", types.AllOutputs{}, nil)
		require.NoError(t, err)
		assert.Nil(t, bundle)
		return nil
	}, pulumi.WithMocks("project", "stack", &infisicalMocks{}))
	require.NoError(t, err)
}

// TestProvisionServicePkiOnly verifies a plain mesh member (pki: set, no infra
// secrets, no mtls_files) is NOT provisioned: its leaf lives with the mesh
// proxy, so the service needs no workspace, identity, or credential (ADR-0033).
func TestProvisionServicePkiOnly(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "", "", "use1")
		svc := types.ServiceSpec{Name: "ghost", Container: "ghost", User: "ghost", Pki: "wardnet-mesh"}
		bundle, err := adapter.ProvisionService(ctx, svc, "prd", "us-east-1", types.AllOutputs{}, nil)
		require.NoError(t, err)
		assert.Nil(t, bundle, "a plain mesh member holds no cert material and needs no bundle (ADR-0033)")
		return nil
	}, pulumi.WithMocks("project", "stack", &infisicalMocks{}))
	require.NoError(t, err)
}

// TestProvisionServiceMtlsFiles verifies an mtls_files: opted-in service (the
// raw-mTLS-plane exception, e.g. tunneller) still gets a workspace + identity
// (scoped read on /<svc>, which covers /<svc>/mtls) and a bundle with an empty
// Env, but writes no infra secret batch.
func TestProvisionServiceMtlsFiles(t *testing.T) {
	mocks := newNamingMocks()
	var bundle *types.ServiceSecretsBundle
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "", "", "use1")
		svc := types.ServiceSpec{Name: "ghost", Container: "ghost", User: "ghost", Pki: "wardnet-mesh", MtlsFiles: true}
		var err error
		bundle, err = adapter.ProvisionService(ctx, svc, "prd", "us-east-1", types.AllOutputs{}, nil)
		return err
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	require.NotNil(t, bundle, "an mtls_files service must get a bundle so the host can fetch its leaf")
	assert.Equal(t, "/ghost", bundle.SecretPath)
	assert.Empty(t, bundle.Env, "no infra secrets → empty env map")
	assert.Equal(t, "/ghost", mocks.captured[infisicalIdentityType].inputs["secretPath"].StringValue())
	_, wroteBatch := mocks.captured[infisicalSecretsBatchType]
	assert.False(t, wroteBatch, "no infra secret batch is written for an mtls_files-only service")
}

// bridgeServiceWithSecret is a single-service fixture for the naming tests: a
// service named "bridge" in container "bridge" with one infra secret declared
// inline, so ProvisionService creates the workspace + batch + identity.
func bridgeServiceWithSecret() types.ServiceSpec {
	return types.ServiceSpec{
		Name: "bridge", Container: "bridge", User: "bridge",
		Environment: map[string]string{"MY_SECRET": "env:MY_SECRET"},
	}
}

// TestWorkspaceNamePassedToAPIMatchesNamingConvention verifies that the name
// field sent to the Infisical API matches the full naming convention
// (wardnet-<env>-<regionSlug>-container-<container>), using the environment
// ("prd") not the abstract region ("us-east-1") as the env segment.
func TestWorkspaceNamePassedToAPIMatchesNamingConvention(t *testing.T) {
	t.Setenv("MY_SECRET", "my-secret-value")
	mocks := newNamingMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "", "", "use1")
		svc := bridgeServiceWithSecret()
		// env="prd", region="us-east-1" — workspace name must use env, not region.
		_, err := adapter.ProvisionService(ctx, svc, "prd", "us-east-1", types.AllOutputs{}, nil)
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
	t.Setenv("MY_SECRET", "my-secret-value")
	mocks := newNamingMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "", "", "use1")
		svc := bridgeServiceWithSecret()
		_, err := adapter.ProvisionService(ctx, svc, "prd", "us-east-1", types.AllOutputs{}, nil)
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

// TestResolveRefDatabaseRejected verifies a database ref is rejected: a database
// exposes no referenceable outputs (ADR-0025); DB credentials flow only through a
// grant. The global/ redirect for grant targets is exercised in the program tests.
func TestResolveRefDatabaseRejected(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := resolveRef("ref:database/global/shared.connectionUrl", "container", "us-east-1", types.AllOutputs{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "use a grants: entry")
		return nil
	}, pulumi.WithMocks("project", "stack", &infisicalMocks{}))
	require.NoError(t, err)
}

// TestResolveRefLiteral verifies a bare string (no recognised prefix) resolves
// to its verbatim value — no resource lookup, no placeholder.
func TestResolveRefLiteral(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		out, err := resolveRef("info", "container", "us-east-1", types.AllOutputs{})
		require.NoError(t, err)
		assert.Equal(t, "info", awaitString(t, out))
		// A value with special characters (URL) is preserved verbatim.
		out, err = resolveRef("https://api.example.com:443/v1", "container", "us-east-1", types.AllOutputs{})
		require.NoError(t, err)
		assert.Equal(t, "https://api.example.com:443/v1", awaitString(t, out))
		return nil
	}, pulumi.WithMocks("project", "stack", &infisicalMocks{}))
	require.NoError(t, err)
}

// TestResolveRefEnv verifies an env: source resolves to the matching environment
// variable's value (injected into the deploy process), not a literal.
func TestResolveRefEnv(t *testing.T) {
	t.Setenv("MY_ENV_SECRET", "s3cr3t-value")
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		out, err := resolveRef("env:MY_ENV_SECRET", "container", "us-east-1", types.AllOutputs{})
		require.NoError(t, err)
		assert.Equal(t, "s3cr3t-value", awaitString(t, out))
		return nil
	}, pulumi.WithMocks("project", "stack", &infisicalMocks{}))
	require.NoError(t, err)
}

// TestResolveRefEnvUnsetErrors verifies an unset/empty env var fails loudly
// rather than silently materialising an empty secret.
func TestResolveRefEnvUnsetErrors(t *testing.T) {
	t.Setenv("INFORGE_TEST_ENV_UNSET", "")
	_, err := resolveRef("env:INFORGE_TEST_ENV_UNSET", "container", "us-east-1", types.AllOutputs{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty or unset")
}

// TestResolveRefGlobalCompute verifies the same redirect for a compute ref.
func TestResolveRefGlobalCompute(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		all := types.AllOutputs{
			Compute: map[string]map[string]types.ComputeOutputs{
				"global": {"edge-01": {PublicIP: pulumi.String("203.0.113.7").ToStringOutput()}},
			},
		}
		out, err := resolveRef("ref:compute/global/edge-01.publicIp", "container", "us-east-1", all)
		require.NoError(t, err)
		assert.Equal(t, "203.0.113.7", awaitString(t, out))
		return nil
	}, pulumi.WithMocks("project", "stack", &infisicalMocks{}))
	require.NoError(t, err)
}

// TestResolveRefVault verifies a vault: source serves the value the program
// pre-decrypted into all.Encrypted, addressed by (container, vaultKey).
func TestResolveRefVault(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		all := types.AllOutputs{
			Encrypted: map[string]map[string]string{"ghost": {"API_KEY": "plain-value"}},
		}
		out, err := resolveRef("vault:API_KEY", "ghost", "us-east-1", all)
		require.NoError(t, err)
		assert.Equal(t, "plain-value", awaitString(t, out))
		return nil
	}, pulumi.WithMocks("project", "stack", &infisicalMocks{}))
	require.NoError(t, err)
}

// TestResolveRefVaultMissing verifies a vault: source with no pre-decrypted
// value fails loudly — the adapter never decrypts on its own.
func TestResolveRefVaultMissing(t *testing.T) {
	_, err := resolveRef("vault:API_KEY", "ghost", "us-east-1", types.AllOutputs{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no decrypted value")
}

// TestProvisionServiceVaultSource verifies a vault: source flows through
// ProvisionService into the env-var -> infra/<key> bundle mapping. The vault
// key may differ from the env var name (decoupled naming).
func TestProvisionServiceVaultSource(t *testing.T) {
	mocks := newNamingMocks()
	var bundle *types.ServiceSecretsBundle
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "", "", "use1")
		svc := types.ServiceSpec{
			Name: "ghost", Container: "ghost", User: "ghost",
			Environment: map[string]string{"API_KEY": "vault:API_KEY"},
		}
		all := types.AllOutputs{
			Encrypted: map[string]map[string]string{"ghost": {"API_KEY": "plain-value"}},
		}
		var err error
		bundle, err = adapter.ProvisionService(ctx, svc, "prd", "us-east-1", all, nil)
		return err
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	assert.Equal(t, "/ghost/infra", mocks.captured[infisicalSecretsBatchType].inputs["secretPath"].StringValue())
	require.NotNil(t, bundle)
	assert.Equal(t, map[string]string{"API_KEY": "infra/API_KEY"}, bundle.Env)
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

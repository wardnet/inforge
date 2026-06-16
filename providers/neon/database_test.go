package neon

import (
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/types"
)

// Compile-time assertion: NeonDatabaseAdapter satisfies types.DatabaseProvider.
var _ types.DatabaseProvider = (*NeonDatabaseAdapter)(nil)

// TestResolveRegionBuiltIn verifies the built-in region mappings.
func TestResolveRegionBuiltIn(t *testing.T) {
	cases := []struct {
		abstract string
		neon     string
	}{
		{"us-east-1", "aws-us-east-2"},
		{"eu-west-1", "aws-eu-west-2"},
		{"ap-east-1", "aws-ap-southeast-1"},
	}
	for _, tc := range cases {
		got, err := ResolveRegion(tc.abstract)
		require.NoError(t, err)
		assert.Equal(t, tc.neon, got)
	}
}

// TestResolveRegionUnknownReturnsError verifies an unknown region is rejected.
func TestResolveRegionUnknownReturnsError(t *testing.T) {
	_, err := ResolveRegion("mars-north-1")
	assert.Error(t, err)
}

// dbMocks is a minimal Pulumi mock monitor for database adapter tests.
type dbMocks struct{}

func (m *dbMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *dbMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	outputs := resource.PropertyMap{}
	switch args.TypeToken {
	case neonProjectType:
		outputs["projectId"] = resource.NewStringProperty("proj-test-id")
	case neonDatabaseType:
		outputs["branchId"] = resource.NewStringProperty("br-test-id")
		outputs["connectionUrl"] = resource.NewStringProperty("postgresql://role:pass@host/db")
	case neonRoleType:
		outputs["user"] = resource.NewStringProperty("svc-role")
		outputs["password"] = resource.NewStringProperty("svc-pass")
		outputs["host"] = resource.NewStringProperty("host")
		outputs["port"] = resource.NewStringProperty("5432")
		outputs["dbName"] = resource.NewStringProperty("appdb")
		outputs["url"] = resource.NewStringProperty("postgresql://svc-role:svc-pass@host:5432/appdb")
	}
	return args.Name + "-id", outputs, nil
}

// TestEnsureContainerIdempotent verifies that calling ensureContainer twice with
// the same key returns the exact same *neonProjectResource (pointer identity),
// meaning no duplicate NeonProject resources are registered.
func TestEnsureContainerIdempotent(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("test-api-key", "test-project", "use1", "")

		first, err := adapter.ensureContainer(ctx, "mycontainer", "prod", "aws-us-east-2")
		if err != nil {
			return err
		}
		second, err := adapter.ensureContainer(ctx, "mycontainer", "prod", "aws-us-east-2")
		if err != nil {
			return err
		}

		// Pointer identity: same key must return the exact same resource.
		if first != second {
			t.Error("ensureContainer returned different objects for the same key")
		}
		return nil
	}, pulumi.WithMocks("project", "stack", &dbMocks{}))
	require.NoError(t, err)
}

// TestEnsureContainerDifferentRegionsAreIndependent verifies that different
// (container, region) pairs produce distinct NeonProject resources.
func TestEnsureContainerDifferentRegionsAreIndependent(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("test-api-key", "test-project", "use1", "")

		east, err := adapter.ensureContainer(ctx, "mycontainer", "prod", "aws-us-east-2")
		if err != nil {
			return err
		}
		west, err := adapter.ensureContainer(ctx, "mycontainer", "prod", "aws-eu-west-2")
		if err != nil {
			return err
		}

		// Pointer identity: different neon regions must produce distinct resources.
		if east == west {
			t.Error("ensureContainer returned the same object for different neon regions")
		}
		return nil
	}, pulumi.WithMocks("project", "stack", &dbMocks{}))
	require.NoError(t, err)
}

// --- naming-convention tests --------------------------------------------------

// namingCapture captures every RegisterResource call keyed by Pulumi type token.
type namingCapture struct {
	mu       sync.Mutex
	captured map[string]struct {
		logicalName string
		inputs      resource.PropertyMap
	}
}

func newNamingCapture() *namingCapture {
	return &namingCapture{captured: map[string]struct {
		logicalName string
		inputs      resource.PropertyMap
	}{}}
}

func (m *namingCapture) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *namingCapture) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	m.captured[args.TypeToken] = struct {
		logicalName string
		inputs      resource.PropertyMap
	}{logicalName: args.Name, inputs: args.Inputs}
	m.mu.Unlock()

	outputs := resource.PropertyMap{}
	switch args.TypeToken {
	case neonProjectType:
		outputs["projectId"] = resource.NewStringProperty("proj-test-id")
	case neonDatabaseType:
		outputs["branchId"] = resource.NewStringProperty("br-test-id")
		outputs["connectionUrl"] = resource.NewStringProperty("postgresql://role:pass@host/db")
	case neonRoleType:
		outputs["user"] = resource.NewStringProperty("svc-role")
		outputs["password"] = resource.NewStringProperty("svc-pass")
		outputs["host"] = resource.NewStringProperty("host")
		outputs["port"] = resource.NewStringProperty("5432")
		outputs["dbName"] = resource.NewStringProperty("appdb")
		outputs["url"] = resource.NewStringProperty("postgresql://svc-role:svc-pass@host:5432/appdb")
	}
	return args.Name + "-id", outputs, nil
}

// TestNeonProjectNamePassedToAPIMatchesNamingConvention verifies that the name
// field sent to the Neon API follows the full naming convention
// (wardnet-<env>-<regionSlug>-container-<container>), not just the raw
// container name.
func TestNeonProjectNamePassedToAPIMatchesNamingConvention(t *testing.T) {
	mocks := newNamingCapture()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("api-key", "inforge", "use1", "")
		spec := types.DatabaseSpec{
			Name:      "main",
			Container: "bridge",
			Provider:  "neon",
			Database:  "appdb",
			Owner:     "app",
		}
		_, err := adapter.Create(ctx, spec, "prd", "us-east-1")
		return err
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	want := naming.Resource("prd", "use1", "container", "bridge")
	got := mocks.captured[neonProjectType].inputs["name"].StringValue()
	assert.Equal(t, want, got,
		"name sent to Neon API must follow naming convention, not be the raw container name")
}

// TestNeonDatabasePulumiNameMatchesNamingConvention verifies that the Pulumi
// logical name of a NeonDatabase follows wardnet-<env>-<regionSlug>-db-<specName>.
func TestNeonDatabasePulumiNameMatchesNamingConvention(t *testing.T) {
	mocks := newNamingCapture()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("api-key", "inforge", "use1", "")
		spec := types.DatabaseSpec{
			Name:      "main",
			Container: "bridge",
			Provider:  "neon",
			Database:  "appdb",
			Owner:     "app",
		}
		_, err := adapter.Create(ctx, spec, "prd", "us-east-1")
		return err
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	want := naming.Resource("prd", "use1", "db", "main")
	got := mocks.captured[neonDatabaseType].logicalName
	assert.Equal(t, want, got,
		"Neon database Pulumi logical name must follow naming convention")
}

// TestResolveRegionConfigOverridesMap verifies the physical Neon region comes
// from provider config when set (required for the region-less global slice, which
// has no abstract region to map), and falls back to the abstract-region map
// otherwise (the regional default, unchanged).
func TestResolveRegionConfigOverridesMap(t *testing.T) {
	// Explicit config region wins, even for an abstract region absent from the map
	// (e.g. the global slice's "global").
	withRegion := New("k", "p", "", "aws-eu-central-1")
	got, err := withRegion.resolveRegion("global")
	require.NoError(t, err)
	assert.Equal(t, "aws-eu-central-1", got)

	// No config region: fall back to the abstract-region map.
	noRegion := New("k", "p", "use1", "")
	got, err = noRegion.resolveRegion("us-east-1")
	require.NoError(t, err)
	assert.Equal(t, "aws-us-east-2", got)

	// No config region and an unmapped abstract region: error.
	_, err = noRegion.resolveRegion("global")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no region mapping")
}

// TestCreateSmoke verifies that Create returns a non-empty DatabaseOutputs.
func TestCreateSmoke(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("test-api-key", "test-project", "use1", "")
		spec := types.DatabaseSpec{
			Name:      "bridge",
			Container: "mycontainer",
			Provider:  "neon",
			Engine:    "postgresql",
			Branch:    "main",
			Database:  "appdb",
			Owner:     "approle",
		}
		out, err := adapter.Create(ctx, spec, "prod", "us-east-1")
		if err != nil {
			return err
		}
		assert.NotNil(t, out.RoleProvisioner, "Create should return a role provisioner (no credential output — ADR-0025)")

		// The provisioner mints a NeonRole and returns its connection value fields.
		fields, err := out.RoleProvisioner.ProvisionRole(ctx, "wardnet-prod-use1-dbrole-api-bridge", "rw")
		if err != nil {
			return err
		}
		assert.NotZero(t, fields.User, "ProvisionRole returns the role's USER field")
		assert.NotZero(t, fields.Password, "ProvisionRole returns the role's PASSWORD field")
		return nil
	}, pulumi.WithMocks("project", "stack", &dbMocks{}))
	require.NoError(t, err)
}

package neon

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	}
	return args.Name + "-id", outputs, nil
}

// TestEnsureContainerIdempotent verifies that calling ensureContainer twice with
// the same key returns the exact same *neonProjectResource (pointer identity),
// meaning no duplicate NeonProject resources are registered.
func TestEnsureContainerIdempotent(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("test-api-key")

		first, err := adapter.ensureContainer(ctx, "mycontainer", "aws-us-east-2")
		if err != nil {
			return err
		}
		second, err := adapter.ensureContainer(ctx, "mycontainer", "aws-us-east-2")
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
		adapter := New("test-api-key")

		east, err := adapter.ensureContainer(ctx, "mycontainer", "aws-us-east-2")
		if err != nil {
			return err
		}
		west, err := adapter.ensureContainer(ctx, "mycontainer", "aws-eu-west-2")
		if err != nil {
			return err
		}

		// Pointer identity: different regions must produce distinct resources.
		if east == west {
			t.Error("ensureContainer returned the same object for different regions")
		}
		return nil
	}, pulumi.WithMocks("project", "stack", &dbMocks{}))
	require.NoError(t, err)
}

// TestCreateSmoke verifies that Create returns a non-empty DatabaseOutputs.
func TestCreateSmoke(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("test-api-key")
		spec := types.DatabaseSpec{
			Name:      "bridge",
			Container: "mycontainer",
			Provider:  "neon",
			Engine:    "postgresql",
			Branch:    "main",
			Database:  "appdb",
			Role:      "approle",
		}
		out, err := adapter.Create(ctx, spec, "prod", "us-east-1")
		if err != nil {
			return err
		}
		assert.NotZero(t, out.ConnectionURL, "ConnectionURL should be non-zero output")
		return nil
	}, pulumi.WithMocks("project", "stack", &dbMocks{}))
	require.NoError(t, err)
}

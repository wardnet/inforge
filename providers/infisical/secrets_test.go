package infisical

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/types"
)

// Compile-time assertions: InfisicalSecretsAdapter satisfies both interfaces.
var _ types.SecretsBackendProvider = (*InfisicalSecretsAdapter)(nil)
var _ types.ComputeInstanceManifestContributor = (*InfisicalSecretsAdapter)(nil)

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

// TestEnsureWorkspaceIdempotent verifies that the same (container, region) pair
// returns the same workspace resource on repeated calls.
func TestEnsureWorkspaceIdempotent(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "enc", "")

		first, err := adapter.ensureWorkspace(ctx, "mycontainer", "us-east-1")
		if err != nil {
			return err
		}
		second, err := adapter.ensureWorkspace(ctx, "mycontainer", "us-east-1")
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

// TestEnsureWorkspaceDifferentRegionsAreIndependent verifies that distinct
// (container, region) pairs produce separate workspace resources.
func TestEnsureWorkspaceDifferentRegionsAreIndependent(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		adapter := New("cid", "csec", "enc", "")

		east, err := adapter.ensureWorkspace(ctx, "mycontainer", "us-east-1")
		if err != nil {
			return err
		}
		west, err := adapter.ensureWorkspace(ctx, "mycontainer", "eu-west-1")
		if err != nil {
			return err
		}

		if east == west {
			t.Error("ensureWorkspace returned the same output for different regions")
		}
		return nil
	}, pulumi.WithMocks("project", "stack", &infisicalMocks{}))
	require.NoError(t, err)
}

// TestContributeToManifestMatchingSpec verifies that ContributeToManifest
// returns a non-empty contribution when the compute spec's container has an
// Infisical SecretsSpec.
func TestContributeToManifestMatchingSpec(t *testing.T) {
	adapter := New("client-id", "client-secret", "enc-secret", "https://app.infisical.com")

	computeSpec := types.ComputeSpec{Container: "myapp"}
	resources := types.Resources{
		Secrets: []types.SecretsSpec{
			{Container: "myapp", Provider: "infisical"},
		},
	}

	contrib, err := adapter.ContributeToManifest(computeSpec, resources, "prd", "us-east-1")
	require.NoError(t, err)
	assert.NotEmpty(t, contrib, "expected a non-empty contribution for a matching spec")

	secrets, ok := contrib["secrets"].(map[string]any)
	require.True(t, ok, "expected 'secrets' key in contribution")
	assert.Equal(t, "infisical", secrets["provider"])
	assert.Equal(t, "prod", secrets["environment"], "prd should be mapped to prod")
}

// TestContributeToManifestNoMatch verifies that an empty contribution is
// returned when no SecretsSpec matches the compute container.
func TestContributeToManifestNoMatch(t *testing.T) {
	adapter := New("cid", "csec", "enc", "")

	computeSpec := types.ComputeSpec{Container: "other"}
	resources := types.Resources{
		Secrets: []types.SecretsSpec{
			{Container: "myapp", Provider: "infisical"},
		},
	}

	contrib, err := adapter.ContributeToManifest(computeSpec, resources, "dev", "us-east-1")
	require.NoError(t, err)
	assert.Empty(t, contrib)
}

// TestContributeToManifestEmptyBootstrapSecretEncErrors verifies that an empty
// bootstrapSecretEnc is rejected rather than written into the manifest.
func TestContributeToManifestEmptyBootstrapSecretEncErrors(t *testing.T) {
	adapter := New("cid", "csec", "", "") // empty bootstrapSecretEnc

	computeSpec := types.ComputeSpec{Container: "myapp"}
	resources := types.Resources{
		Secrets: []types.SecretsSpec{
			{Container: "myapp", Provider: "infisical"},
		},
	}

	_, err := adapter.ContributeToManifest(computeSpec, resources, "prd", "us-east-1")
	assert.Error(t, err, "expected error when bootstrapSecretEnc is empty")
}

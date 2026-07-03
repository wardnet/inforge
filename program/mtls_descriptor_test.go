package program

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/agent"
	"github.com/wardnet/inforge/internal/meshcert"
	"github.com/wardnet/inforge/internal/types"
)

func TestRenderDescriptorMeshFiles(t *testing.T) {
	svc := types.ServiceSpec{Name: "bridge", Container: "bridge", Host: "bridge", Type: "raw", User: "bridge", Pki: "wardnet-mesh"}
	// A mesh service with a provider (a bundle) advertises its leaf/key/bundle.
	bundle := &types.ServiceSecretsBundle{ProviderKind: "infisical", URL: "https://x", Environment: "prod", SecretPath: "/bridge"}
	out, err := renderDescriptor(svc, types.ComputeOutputs{}, bundle, "ws-1", "prd", "us-east-1", "use1", "wardnet.network", "bridge-01", 0)
	require.NoError(t, err)

	d, err := agent.ParseDescriptor([]byte(out))
	require.NoError(t, err)
	assert.Equal(t, meshcert.DescriptorFiles(), d.Files,
		"a mesh service with a provider advertises its leaf/key/bundle in files:")
}

func TestRenderDescriptorMeshNoProviderNoFiles(t *testing.T) {
	// A mesh service with no provider yet (secret-less; provider/identity lands
	// in #109) emits no files: — never an unsatisfiable descriptor.
	svc := types.ServiceSpec{Name: "bridge", Container: "bridge", Host: "bridge", Type: "raw", User: "bridge", Pki: "wardnet-mesh"}
	out, err := renderDescriptor(svc, types.ComputeOutputs{}, nil, "", "prd", "us-east-1", "use1", "wardnet.network", svc.Name+"-01", 0)
	require.NoError(t, err)

	d, err := agent.ParseDescriptor([]byte(out))
	require.NoError(t, err)
	assert.Empty(t, d.Files, "no provider → no files:")
}

func TestRenderDescriptorNoMeshNoFiles(t *testing.T) {
	svc := types.ServiceSpec{Name: "plain", Container: "plain", Host: "plain", Type: "raw", User: "plain"}
	out, err := renderDescriptor(svc, types.ComputeOutputs{}, nil, "", "prd", "us-east-1", "use1", "wardnet.network", svc.Name+"-01", 0)
	require.NoError(t, err)

	d, err := agent.ParseDescriptor([]byte(out))
	require.NoError(t, err)
	assert.Empty(t, d.Files, "a service with no pki: has no files: entries")
}

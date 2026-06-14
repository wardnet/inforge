package program

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/bootstrapper"
	"github.com/wardnet/inforge/internal/meshcert"
	"github.com/wardnet/inforge/internal/types"
)

func TestRenderDescriptorMeshFiles(t *testing.T) {
	svc := types.ServiceSpec{Name: "bridge", Container: "bridge", Host: "bridge", Type: "raw", User: "bridge", Pki: "wardnet-mesh"}
	out, err := renderDescriptor(svc, nil, "", "prd", "us-east-1", "use1", "wardnet.network")
	require.NoError(t, err)

	d, err := bootstrapper.ParseDescriptor([]byte(out))
	require.NoError(t, err)
	assert.Equal(t, meshcert.DescriptorFiles(), d.Files,
		"a mesh service advertises its leaf/key/bundle in files:")
}

func TestRenderDescriptorNoMeshNoFiles(t *testing.T) {
	svc := types.ServiceSpec{Name: "plain", Container: "plain", Host: "plain", Type: "raw", User: "plain"}
	out, err := renderDescriptor(svc, nil, "", "prd", "us-east-1", "use1", "wardnet.network")
	require.NoError(t, err)

	d, err := bootstrapper.ParseDescriptor([]byte(out))
	require.NoError(t, err)
	assert.Empty(t, d.Files, "a service with no pki: has no files: entries")
}

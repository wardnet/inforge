package manifest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/types"
)

// TestGeneratePlain asserts the manifest is plain YAML with base + contribution
// fields legible and no SOPS/age artifacts — secrets are runtime-fetched, never
// baked.
func TestGeneratePlain(t *testing.T) {
	base := Base{Version: 1, Region: "us-east-1", Namespace: "urn:use1:wardnet:prd:bridge"}
	contrib := types.ManifestContribution{"log_level": "info"}

	out, err := Generate(base, []types.ManifestContribution{contrib})
	require.NoError(t, err)
	assert.NotContains(t, out, "ENC[", "no encrypted fields — baking is retired")
	assert.NotContains(t, out, "sops:", "no SOPS metadata — baking is retired")
	assert.Contains(t, out, "region: us-east-1")
	assert.Contains(t, out, "namespace: urn:use1:wardnet:prd:bridge")
	assert.Contains(t, out, "version: 1")
	assert.Contains(t, out, "log_level: info")
}

// TestGenerateNoContributions asserts a base-only manifest renders cleanly.
func TestGenerateNoContributions(t *testing.T) {
	out, err := Generate(Base{Version: 1, Region: "us-east-1", Namespace: "ns"}, nil)
	require.NoError(t, err)
	assert.NotContains(t, out, "ENC[")
	assert.Contains(t, out, "region: us-east-1")
}

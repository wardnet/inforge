package manifest

import (
	"testing"

	"github.com/getsops/sops/v3/decrypt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/bootstrap"
	"github.com/wardnet/inforge/internal/types"
	"gopkg.in/yaml.v3"
)

func TestGenerateNoSecrets(t *testing.T) {
	base := Base{Version: 1, Region: "us-east-1", Namespace: "urn:use1:wardnet:prd:bridge"}
	contrib := types.ManifestContribution{"log_level": "info"}

	res, err := Generate(base, []types.ManifestContribution{contrib}, "")
	require.NoError(t, err)
	assert.False(t, res.BootstrapNeeded, "no secrets => no bootstrap")
	assert.NotContains(t, res.Manifest, "ENC[")
	assert.NotContains(t, res.Manifest, "sops:")
	assert.Contains(t, res.Manifest, "region: us-east-1")
	assert.Contains(t, res.Manifest, "log_level: info")
}

func TestGenerateProbeWithSecrets(t *testing.T) {
	base := Base{Version: 1, Region: "us-east-1", Namespace: "urn:use1:wardnet:prd:bridge"}
	contrib := types.ManifestContribution{
		"log_level":   "info",
		"db_password": Secret("supersecret"),
	}

	res, err := Generate(base, []types.ManifestContribution{contrib}, "")
	require.NoError(t, err)
	assert.True(t, res.BootstrapNeeded, "secrets present => bootstrap needed even on probe")
	assert.Empty(t, res.Manifest, "probe must not attempt encryption with empty recipient")
}

func TestGenerateWithSecretsEncryptsAndRoundTrips(t *testing.T) {
	mat, err := bootstrap.Mint()
	require.NoError(t, err)

	base := Base{Version: 1, Region: "us-east-1", Namespace: "urn:use1:wardnet:prd:bridge"}
	contrib := types.ManifestContribution{
		"log_level":   "info",
		"db_password": Secret("supersecret"),
	}

	res, err := Generate(base, []types.ManifestContribution{contrib}, mat.Recipient)
	require.NoError(t, err)

	// Bootstrap is triggered by the secret's presence, and the document is SOPS-encrypted.
	assert.True(t, res.BootstrapNeeded)
	assert.Contains(t, res.Manifest, "ENC[", "secret field should be encrypted")
	assert.Contains(t, res.Manifest, "sops:", "should carry SOPS metadata")
	// Non-secret fields stay legible.
	assert.Contains(t, res.Manifest, "log_level: info")

	// Round-trip: with K, the secret decrypts back to its plaintext.
	t.Setenv("SOPS_AGE_KEY", mat.Identity.String())
	plain, err := decrypt.Data([]byte(res.Manifest), "yaml")
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, yaml.Unmarshal(plain, &out))
	assert.Equal(t, "supersecret", out["db_password"])
	assert.Equal(t, "info", out["log_level"])
}

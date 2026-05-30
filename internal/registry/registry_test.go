package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/types"
)

func TestStubRegistryUnknownProvider(t *testing.T) {
	r := BuildRegistry(map[string]map[string]any{"hetzner": {"apiToken": "x"}}, types.SSHConfig{})

	_, err := r.Network("hetzner")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown provider: "hetzner"`)

	_, err = r.Compute("hetzner")
	assert.Error(t, err)
	_, err = r.DNS("cloudflare")
	assert.Error(t, err)
	_, err = r.Database("neon")
	assert.Error(t, err)
	_, err = r.Secrets("infisical")
	assert.Error(t, err)

	assert.Nil(t, r.ManifestContributors())
}

func TestMergeProviders(t *testing.T) {
	global := map[string]map[string]any{
		"hetzner":    {"apiToken": "global-token", "project": "main"},
		"cloudflare": {"apiToken": "cf-token"},
	}
	overrides := map[string]map[string]any{
		"hetzner": {"location": "ash", "apiToken": "region-token"},
		"neon":    {"apiKey": "neon-key"},
	}

	merged := MergeProviders(global, overrides)

	// Override key wins; untouched global key is preserved.
	assert.Equal(t, "region-token", merged["hetzner"]["apiToken"])
	assert.Equal(t, "main", merged["hetzner"]["project"])
	assert.Equal(t, "ash", merged["hetzner"]["location"])
	// Provider only in global is carried through unchanged.
	assert.Equal(t, "cf-token", merged["cloudflare"]["apiToken"])
	// Provider only in overrides is added.
	assert.Equal(t, "neon-key", merged["neon"]["apiKey"])

	// Inputs are not mutated.
	assert.Equal(t, "global-token", global["hetzner"]["apiToken"])
	_, ok := global["neon"]
	assert.False(t, ok)
}

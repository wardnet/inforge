package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/types"
	cfprovider "github.com/wardnet/inforge/providers/cloudflare"
	"github.com/wardnet/inforge/providers/hetzner"
	"github.com/wardnet/inforge/providers/neon"
)

func TestRegistryUnknownProvider(t *testing.T) {
	// nil ctx + nil regionTable: providers are built lazily, so construction is
	// not triggered during this test. Only the "unknown provider" paths are hit.
	r := BuildRegistry(nil, map[string]map[string]any{"hetzner": {"apiToken": "x"}}, types.SSHConfig{}, nil)

	// "hetzner" is now a known network provider — must succeed.
	np, err := r.Network("hetzner")
	require.NoError(t, err)
	assert.IsType(t, (*hetzner.HetznerNetwork)(nil), np)

	// "hetzner" is now a known compute provider — must succeed.
	cp, err := r.Compute("hetzner")
	require.NoError(t, err)
	assert.IsType(t, (*hetzner.HetznerCompute)(nil), cp)

	// "cloudflare" is now a known DNS provider — must succeed.
	dp, err := r.DNS("cloudflare")
	require.NoError(t, err)
	assert.IsType(t, (*cfprovider.CloudflareDns)(nil), dp)

	// "neon" is now a known database provider — must succeed.
	dbp, err := r.Database("neon")
	require.NoError(t, err)
	assert.IsType(t, (*neon.NeonDatabaseAdapter)(nil), dbp)

	_, err = r.Database("unknown-db")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown provider: "unknown-db"`)

	_, err = r.Secrets("infisical")
	assert.Error(t, err)

	// Unknown network provider still errors.
	_, err = r.Network("unknown-cloud")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown provider: "unknown-cloud"`)

	assert.Empty(t, r.ManifestContributors())
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

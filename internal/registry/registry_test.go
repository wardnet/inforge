package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/types"
	cfprovider "github.com/wardnet/inforge/providers/cloudflare"
	"github.com/wardnet/inforge/providers/hetzner"
	"github.com/wardnet/inforge/providers/infisical"
	"github.com/wardnet/inforge/providers/neon"
)

func TestRegistryUnknownProvider(t *testing.T) {
	// nil ctx + nil regionTable: providers are built lazily, so construction is
	// not triggered during this test. Only the "unknown provider" paths are hit.
	r := BuildRegistry(nil, map[string]map[string]any{"hetzner": {"apiToken": "x"}}, types.SSHConfig{}, nil, "test-project", "test", "us-east-1")

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

	// "infisical" is a known secrets provider — must succeed.
	sp, err := r.Secrets("infisical")
	require.NoError(t, err)
	assert.IsType(t, (*infisical.InfisicalSecretsAdapter)(nil), sp)

	// Unknown secrets provider errors.
	_, err = r.Secrets("unknown-secrets")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown provider: "unknown-secrets"`)

	// "infisical" is also a known service-secrets provisioner.
	ssp, err := r.ServiceSecretsProvisioner("infisical")
	require.NoError(t, err)
	assert.IsType(t, (*infisical.InfisicalSecretsAdapter)(nil), ssp)

	_, err = r.ServiceSecretsProvisioner("unknown-secrets")
	require.Error(t, err)

	// Unknown network provider still errors.
	_, err = r.Network("unknown-cloud")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown provider: "unknown-cloud"`)
}

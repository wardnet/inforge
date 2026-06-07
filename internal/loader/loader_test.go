package loader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testdataDir reuses the validate package's fixture environments.
var testdataDir = filepath.Join("..", "validate", "testdata")

func TestSubstituteEnvVarsPresent(t *testing.T) {
	t.Setenv("INFORGE_TEST_TOKEN", "sekret")
	out, err := substituteEnvVars(map[string]any{
		"token": "${INFORGE_TEST_TOKEN}",
		"nested": []any{
			"plain",
			"prefix-${INFORGE_TEST_TOKEN}",
		},
	}, false)
	require.NoError(t, err)
	m := out.(map[string]any)
	assert.Equal(t, "sekret", m["token"])
	assert.Equal(t, []any{"plain", "prefix-sekret"}, m["nested"])
}

func TestSubstituteEnvVarsMissing(t *testing.T) {
	_, err := substituteEnvVars("${INFORGE_DEFINITELY_UNSET_VAR}", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required env var")
}

func TestSubstituteEnvVarsLenient(t *testing.T) {
	out, err := substituteEnvVars("${INFORGE_DEFINITELY_UNSET_VAR}", true)
	require.NoError(t, err)
	assert.Equal(t, "", out)
}

func TestLoadVariables(t *testing.T) {
	vars, err := LoadVariables("ok", testdataDir)
	require.NoError(t, err)
	assert.Equal(t, "example.com", vars.BaseDomain)
	assert.Equal(t, "ssh-ed25519 AAAA...authorized", vars.SSH.AuthorizedKeys)
}

func TestLoadResourcesDefaultsAndCloudInit(t *testing.T) {
	res, err := LoadResources("ok", testdataDir)
	require.NoError(t, err)

	require.Len(t, res.Compute, 1)
	c := res.Compute[0]
	assert.Equal(t, "vm", c.Kind, "kind should default to vm")
	assert.Equal(t, 1, c.InstanceCount, "instance_count should default to 1")
	assert.True(t, filepath.IsAbs(c.CloudInit), "cloud_init should resolve to an absolute path")

	require.Len(t, res.Service, 1)
	assert.Equal(t, "raw", res.Service[0].Type, "service type should default to raw")

	// the api service declares ingress fed by the host's tls-termination.
	require.NotNil(t, res.Service[0].Ingress, "api service should declare ingress")
	assert.Equal(t, "api", res.Service[0].Ingress.Hostname)
	assert.Equal(t, 8080, res.Service[0].Ingress.Port)

	require.Len(t, res.TLSTermination, 1)
	assert.Equal(t, "edge", res.TLSTermination[0].Name)
	assert.Equal(t, "bridge", res.TLSTermination[0].Compute)

	// public network keeps its declared type; private keeps its parent.
	require.Len(t, res.Network, 2)
}

// TestLoadRegionTableFromFile exercises the nested regions.yaml: the per-region
// slug + provider config under `regions:`, plus the optional `global:` block.
func TestLoadRegionTableFromFile(t *testing.T) {
	rt, global, err := LoadRegionTable("ok", testdataDir)
	require.NoError(t, err)
	slug, err := rt.Slug("us-east-1")
	require.NoError(t, err)
	assert.Equal(t, "use1", slug)
	// Provider config now lives per region in regions.yaml.
	assert.Contains(t, rt["us-east-1"].Providers, "hetzner")
	// The ok fixture declares no global slot.
	assert.Nil(t, global)

	st, err := LoadSizeTable("ok", testdataDir)
	require.NoError(t, err)
	require.NoError(t, st.Resolve("SMALL"))
}

// TestLoadRegionTableMissing confirms an absent regions.yaml yields an empty
// table (the new model makes regions.yaml the deploy authority — there is no
// built-in fallback region set), with no error.
func TestLoadRegionTableMissing(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "prd"), 0o755))
	rt, global, err := LoadRegionTable("prd", dir)
	require.NoError(t, err)
	assert.Empty(t, rt)
	assert.Nil(t, global)
}

// TestLoadSizeTableFromFile exercises the on-disk size table: a YAML list of
// names that replaces the defaults wholesale.
func TestLoadSizeTableFromFile(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "prd")
	require.NoError(t, os.MkdirAll(envPath, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(envPath, "sizes.yaml"),
		[]byte("- tiny\n- huge\n"), 0o644))

	st, err := LoadSizeTable("prd", dir)
	require.NoError(t, err)
	require.NoError(t, st.Resolve("tiny"))
	require.NoError(t, st.Resolve("huge"))
	// Replace, not merge: a default size is absent from the loaded table.
	assert.Error(t, st.Resolve("SMALL"))
}

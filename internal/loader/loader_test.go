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
	require.Len(t, vars.Regions, 1)
	assert.Equal(t, "us-east-1", vars.Regions[0].Name)
	assert.Contains(t, vars.Providers, "hetzner")
}

func TestLoadResourcesDefaultsAndCloudInit(t *testing.T) {
	byRegion, err := LoadResources("ok", testdataDir)
	require.NoError(t, err)

	res, ok := byRegion["us-east-1"]
	require.True(t, ok)

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

func TestLoadTablesDefault(t *testing.T) {
	rt, err := LoadRegionTable("ok", testdataDir)
	require.NoError(t, err)
	slug, err := rt.Slug("us-east-1")
	require.NoError(t, err)
	assert.Equal(t, "use1", slug)

	st, err := LoadSizeTable("ok", testdataDir)
	require.NoError(t, err)
	require.NoError(t, st.Resolve("SMALL"))
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

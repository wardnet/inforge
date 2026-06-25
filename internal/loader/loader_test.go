package loader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/types"
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

	require.Len(t, res.Service, 3)
	// Services load in filename order: api, tunneller, web.
	api := res.Service[0]
	assert.Equal(t, "api", api.Name)
	assert.Equal(t, "raw", api.Type, "service type should default to raw")

	// the tunneller service is private-only: no ingress/routes, just exposed_ports.
	tunneller := res.Service[1]
	assert.Equal(t, "tunneller", tunneller.Name)
	assert.Empty(t, tunneller.Ingress, "a private-only service declares no ingress")
	require.Len(t, tunneller.ExposedPorts, 2, "tunneller declares two exposed ports")
	assert.Equal(t, types.ExposedPort{Proto: "tcp", Port: 9444}, tunneller.ExposedPorts[0])
	assert.Equal(t, types.ExposedPort{Proto: "udp", Port: 51820}, tunneller.ExposedPorts[1])

	// the api service declares a tls-termination and a forward route.
	require.Len(t, api.Routes, 2, "api service should declare two routes")
	assert.Equal(t, "tls-termination", api.Routes[0].Type)
	assert.Equal(t, 443, api.Routes[0].Listen)
	assert.Equal(t, 8080, api.Routes[0].Target)
	assert.Equal(t, "forward", api.Routes[1].Type)
	assert.Equal(t, 853, api.Routes[1].Listen)
	assert.Equal(t, 5353, api.Routes[1].Target)

	// public network keeps its declared type; private keeps its parent.
	require.Len(t, res.Network, 2)
}

// TestLoadGlobalResources reads the global slice under resources/<env>/global/
// into a separate resource set, distinct from the regional set.
func TestLoadGlobalResources(t *testing.T) {
	global, err := LoadGlobalResources("global-ok", testdataDir)
	require.NoError(t, err)
	require.Len(t, global.Database, 1, "the global slice declares one database")
	assert.Equal(t, "shared", global.Database[0].Name)
	assert.Equal(t, "main", global.Database[0].Branch, "database branch should default to main")

	// The regional set of the same environment does NOT include the global
	// database: global/ is loaded separately, not as a regional resource type.
	regional, err := LoadResources("global-ok", testdataDir)
	require.NoError(t, err)
	assert.Empty(t, regional.Database, "global/ must not leak into the regional set")
}

// TestLoadIngressAndApp reads the ingress and app resource folders at both
// scopes: a regional ingress fronting a same-scope host plus its app, and the
// global equivalents. It confirms the new ingress/ folder is loaded and the app
// carries the ingress foreign key (not the retired cdn field).
func TestLoadIngressAndApp(t *testing.T) {
	regional, err := LoadResources("ingress-app-ok", testdataDir)
	require.NoError(t, err)
	require.Len(t, regional.Ingress, 1, "the regional slice declares one ingress")
	assert.Equal(t, "web", regional.Ingress[0].Name)
	assert.Equal(t, "bridge", regional.Ingress[0].Host, "ingress carries its compute host FK")
	require.Len(t, regional.App, 1, "the regional slice declares one app")
	assert.Equal(t, "dashboard", regional.App[0].Name)
	assert.Equal(t, "web", regional.App[0].Ingress, "app carries its ingress FK")
	assert.Equal(t, "my", regional.App[0].Subdomain)
	assert.True(t, regional.App[0].Spa)

	global, err := LoadGlobalResources("ingress-app-ok", testdataDir)
	require.NoError(t, err)
	require.Len(t, global.Ingress, 1, "the global slice declares one ingress")
	assert.Equal(t, "edge", global.Ingress[0].Host, "global ingress fronts the global host")
	require.Len(t, global.App, 1, "the global slice declares one app")
	assert.Equal(t, "marketing", global.App[0].Name)
	assert.Equal(t, "web", global.App[0].Ingress)
	assert.Equal(t, "www", global.App[0].Subdomain)
}

// TestLoadGlobalResourcesMissing confirms an environment with no global/ slice
// yields an empty set (the global slice is optional), with no error.
func TestLoadGlobalResourcesMissing(t *testing.T) {
	global, err := LoadGlobalResources("ok", testdataDir)
	require.NoError(t, err)
	assert.Empty(t, global.Database)
	assert.Empty(t, global.Compute)
	assert.Empty(t, global.Network)
}

// TestLoadRegionTableFromFile exercises the nested regions.yaml: the per-region
// slug + provider config under `regions:`, plus the optional `global:` block.
func TestLoadRegionTableFromFile(t *testing.T) {
	// The ok fixture's dns.zone is an ${ENV_VAR} ref; the strict (deploy) load
	// resolves it, so the var must be set here. Validation loads the same fixture
	// raw and deliberately leaves it unset (see TestValidateResourcesOK).
	t.Setenv("INFORGE_TEST_OK_ZONE", "test-zone")
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

// writeRegionsYAML writes a regions.yaml with a ${ENV_VAR} provider credential
// into a temp env dir and returns the dir, for the substitution tests below.
func writeRegionsYAML(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "prd"), 0o755))
	const doc = `regions:
  us-east-1:
    slug: use1
    providers:
      hetzner:
        apiToken: ${INFORGE_TEST_HCLOUD_TOKEN}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prd", "regions.yaml"), []byte(doc), 0o644))
	return dir
}

// TestLoadRegionTableSubstitutesEnvVars is the regression guard for the bug: a
// ${ENV_VAR} in regions.yaml (e.g. a provider credential) must be substituted, not
// passed to the provider as the literal string "${...}".
func TestLoadRegionTableSubstitutesEnvVars(t *testing.T) {
	t.Setenv("INFORGE_TEST_HCLOUD_TOKEN", "real-token-value")
	dir := writeRegionsYAML(t)

	rt, _, err := LoadRegionTable("prd", dir)
	require.NoError(t, err)
	assert.Equal(t, "real-token-value", rt["us-east-1"].Providers["hetzner"]["apiToken"],
		"the credential ${ENV_VAR} must be substituted, not passed through literally")
}

// TestLoadRegionTableMissingEnvVarErrors: strict load fails clearly when a
// referenced credential env var is unset (rather than handing the provider a
// literal "${...}").
func TestLoadRegionTableMissingEnvVarErrors(t *testing.T) {
	t.Setenv("INFORGE_TEST_HCLOUD_TOKEN", "")
	dir := writeRegionsYAML(t)

	_, _, err := LoadRegionTable("prd", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required env var")
}

// TestLoadRegionTableRaw: the raw load (used by validation) leaves a ${ENV_VAR}
// reference as a literal — even when the env var is unset — so structural
// validation runs without credentials and an unresolved credential never reads
// as an (empty) missing value.
func TestLoadRegionTableRaw(t *testing.T) {
	t.Setenv("INFORGE_TEST_HCLOUD_TOKEN", "")
	dir := writeRegionsYAML(t)

	rt, _, err := LoadRegionTableRaw("prd", dir)
	require.NoError(t, err)
	assert.Equal(t, "${INFORGE_TEST_HCLOUD_TOKEN}", rt["us-east-1"].Providers["hetzner"]["apiToken"])
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

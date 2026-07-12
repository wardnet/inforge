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

func TestResolverStringExpandsPlaceholders(t *testing.T) {
	r := NewResolverFrom(func(k string) (string, bool) {
		if k == "INFORGE_TEST_TOKEN" {
			return "sekret", true
		}
		return "", false
	})
	got, err := r.String("providers.hetzner.apiToken", "prefix-${INFORGE_TEST_TOKEN}")
	require.NoError(t, err)
	assert.Equal(t, "prefix-sekret", got)
}

// A missing variable is an error, and the error names BOTH the config field that
// wanted it and the env var that was absent — the old message named only the var.
func TestResolverStringMissingNamesFieldAndVar(t *testing.T) {
	r := NewResolverFrom(func(string) (string, bool) { return "", false })
	_, err := r.String("ssh.authorizedKeys", "${INFORGE_DEFINITELY_UNSET_VAR}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required env var")
	assert.Contains(t, err.Error(), "INFORGE_DEFINITELY_UNSET_VAR")
	assert.Contains(t, err.Error(), "ssh.authorizedKeys")
}

// An env var set to the empty string is treated as absent: a blank credential is
// never a legitimate value.
func TestResolverStringEmptyIsMissing(t *testing.T) {
	r := NewResolverFrom(func(string) (string, bool) { return "", true })
	_, err := r.String("base_domain", "${INFORGE_TEST_BLANK}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required env var")
}

// A field with no placeholder never consults the environment at all.
func TestResolverStringLiteralNeedsNoEnv(t *testing.T) {
	r := NewResolverFrom(func(string) (string, bool) {
		t.Fatal("a literal must not be looked up in the environment")
		return "", false
	})
	got, err := r.String("base_domain", "example.com")
	require.NoError(t, err)
	assert.Equal(t, "example.com", got)
}

// Loading never expands: the raw document keeps its placeholders and requires no
// environment. This is what lets a command resolve only the fields it reads.
func TestLoadVariablesRawKeepsPlaceholders(t *testing.T) {
	raw, err := LoadVariablesRaw("ok", testdataDir)
	require.NoError(t, err)
	assert.Equal(t, "example.com", raw.BaseDomain)
	assert.Equal(t, "ssh-ed25519 AAAA...authorized", raw.SSH.AuthorizedKeys)
}

func TestResolverVariablesResolvesWholeDocument(t *testing.T) {
	raw := RawVariables{BaseDomain: "${INFORGE_TEST_DOMAIN}"}
	raw.SSH.AuthorizedKeys = "${INFORGE_TEST_KEYS}"
	r := NewResolverFrom(func(k string) (string, bool) {
		return map[string]string{
			"INFORGE_TEST_DOMAIN": "example.com",
			"INFORGE_TEST_KEYS":   "ssh-ed25519 AAAA",
		}[k], true
	})
	vars, err := r.Variables(raw)
	require.NoError(t, err)
	assert.Equal(t, "example.com", vars.BaseDomain)
	assert.Equal(t, "ssh-ed25519 AAAA", vars.SSH.AuthorizedKeys)
}

// THE REGRESSION TEST. Minting a mesh leaf reads base_domain and nothing else, so
// an unset SSH_AUTHORIZED_KEYS must not fail it. Under the old eager loader the
// whole document was expanded on load, and `inforge pki renew` / `inforge releases
// deploy` died with "missing required env var: SSH_AUTHORIZED_KEYS" on a code path
// that never reads an SSH key — which is exactly how a tunneller release broke in
// production while ddns (no mtls_files, so no leaf mint) sailed through.
func TestResolvingOneFieldIgnoresUnsetVarsInOtherFields(t *testing.T) {
	raw := RawVariables{BaseDomain: "wardnet.network"}
	raw.SSH.AuthorizedKeys = "${SSH_AUTHORIZED_KEYS}" // unset in the environment
	raw.SSH.DeployPublicKey = "${DEPLOY_PUBLIC_KEY}"  // unset in the environment
	r := NewResolverFrom(func(string) (string, bool) { return "", false })

	baseDomain, err := r.String("base_domain", raw.BaseDomain)
	require.NoError(t, err, "resolving base_domain must not require the ssh block")
	assert.Equal(t, "wardnet.network", baseDomain)

	// ...and the same document still fails loudly when a caller DOES need the
	// field whose variable is unset.
	_, err = r.Variables(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ssh.authorizedKeys")
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
	require.Len(t, global.Database, 1, "the global slice declares one logical database")
	assert.Equal(t, "shared", global.Database[0].Name)
	assert.Equal(t, "pg", global.Database[0].Cluster, "the logical database names its cluster")
	require.Len(t, global.DatabaseCluster, 1, "the global slice declares one database-cluster")
	assert.Equal(t, "pg", global.DatabaseCluster[0].Name)
	assert.Equal(t, "17", global.DatabaseCluster[0].Version, "the cluster version defaults to 17")

	// The regional set of the same environment does NOT include the global
	// database/cluster: global/ is loaded separately, not as a regional resource type.
	regional, err := LoadResources("global-ok", testdataDir)
	require.NoError(t, err)
	assert.Empty(t, regional.Database, "global/ must not leak into the regional set")
	assert.Empty(t, regional.DatabaseCluster, "global/ must not leak into the regional set")
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

// TestLoadRegionsRawFromFile exercises the nested regions.yaml: the per-region
// slug + provider config under `regions:`, plus the optional `global:` block.
// The ok fixture's dns.zone is an ${ENV_VAR} ref, and the raw load needs no env
// var set for it — the placeholder simply survives.
func TestLoadRegionsRawFromFile(t *testing.T) {
	raw, err := LoadRegionsRaw("ok", testdataDir)
	require.NoError(t, err)
	slug, err := raw.Table.Slug("us-east-1")
	require.NoError(t, err)
	assert.Equal(t, "use1", slug)
	assert.Contains(t, raw.Table["us-east-1"].Providers, "hetzner")
	assert.Nil(t, raw.Global, "the ok fixture declares no global slot")

	st, err := LoadSizeTable("ok", testdataDir)
	require.NoError(t, err)
	require.NoError(t, st.Resolve("SMALL"))
}

// An absent regions.yaml yields an empty table (regions.yaml is the deploy
// authority — there is no built-in fallback region set), with no error.
func TestLoadRegionsRawMissing(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "prd"), 0o755))
	raw, err := LoadRegionsRaw("prd", dir)
	require.NoError(t, err)
	assert.Empty(t, raw.Table)
	assert.Nil(t, raw.Global)
}

// writeRegionsYAML writes a regions.yaml with a ${ENV_VAR} provider credential
// into a temp env dir and returns the dir, for the resolution tests below.
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

// The regression guard for the credential bug: a ${ENV_VAR} in regions.yaml must
// be expanded before it reaches a provider, never passed through as the literal
// string "${...}".
func TestResolverRegionsSubstitutesCredentials(t *testing.T) {
	raw, err := LoadRegionsRaw("prd", writeRegionsYAML(t))
	require.NoError(t, err)
	r := NewResolverFrom(func(string) (string, bool) { return "real-token-value", true })

	table, _, err := r.Regions(raw)
	require.NoError(t, err)
	assert.Equal(t, "real-token-value", table["us-east-1"].Providers["hetzner"]["apiToken"],
		"the credential ${ENV_VAR} must be resolved, not passed through literally")
}

// Resolving fails clearly when a referenced credential is unset, rather than
// handing the provider a literal "${...}" — and the error names the field.
func TestResolverRegionsMissingCredentialErrors(t *testing.T) {
	raw, err := LoadRegionsRaw("prd", writeRegionsYAML(t))
	require.NoError(t, err)
	r := NewResolverFrom(func(string) (string, bool) { return "", false })

	_, _, err = r.Regions(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required env var")
	assert.Contains(t, err.Error(), "regions.us-east-1.providers.hetzner.apiToken")
}

// The raw load (what validation uses) leaves a ${ENV_VAR} as a literal even when
// the var is unset, so structural validation runs without credentials and an
// unresolved credential never reads as an (empty) missing value.
func TestLoadRegionsRawKeepsCredentialLiteral(t *testing.T) {
	raw, err := LoadRegionsRaw("prd", writeRegionsYAML(t))
	require.NoError(t, err)
	assert.Equal(t, "${INFORGE_TEST_HCLOUD_TOKEN}", raw.Table["us-east-1"].Providers["hetzner"]["apiToken"])
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

func TestNormalizeServiceTrimsMeshAllowList(t *testing.T) {
	s := types.ServiceSpec{Mesh: &types.MeshSpec{Port: 8080, AllowedServices: []string{" ddns ", "gateway"}, PublicPaths: []string{" /v1/** "}, InternalPaths: []string{" /internal/** "}}, HealthProbePaths: []string{" /healthz "}}
	NormalizeService(&s)
	if s.Mesh.AllowedServices[0] != "ddns" {
		t.Errorf("mesh allow list not trimmed: %q", s.Mesh.AllowedServices[0])
	}
	if s.Mesh.PublicPaths[0] != "/v1/**" {
		t.Errorf("mesh public paths not trimmed: %q", s.Mesh.PublicPaths[0])
	}
	if s.Mesh.InternalPaths[0] != "/internal/**" {
		t.Errorf("mesh internal paths not trimmed: %q", s.Mesh.InternalPaths[0])
	}
	if s.HealthProbePaths[0] != "/healthz" {
		t.Errorf("health probe paths not trimmed: %q", s.HealthProbePaths[0])
	}
}

func TestNormalizeGatewayNormalizesServices(t *testing.T) {
	g := types.GatewaySpec{Name: "api", Services: []string{" ddns "}, HealthProbePaths: []string{" /healthz "}}
	NormalizeGateway(&g)
	if g.Services[0] != "ddns" {
		t.Errorf("gateway services not trimmed: %q", g.Services[0])
	}
	if g.HealthProbePaths[0] != "/healthz" {
		t.Errorf("gateway health probe paths not trimmed: %q", g.HealthProbePaths[0])
	}
	if g.HealthProbesPort != types.DefaultHealthProbesPort {
		t.Errorf("gateway health port not defaulted: %d", g.HealthProbesPort)
	}
}

func TestNormalizeGatewayTrims(t *testing.T) {
	g := types.GatewaySpec{Name: " mesh ", Container: " platform ", Host: " bridge ", Subdomain: " gateway "}
	NormalizeGateway(&g)
	if g.Name != "mesh" || g.Container != "platform" || g.Host != "bridge" || g.Subdomain != "gateway" {
		t.Errorf("NormalizeGateway did not trim: %+v", g)
	}
}

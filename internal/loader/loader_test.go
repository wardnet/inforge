package loader

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/types"
	"github.com/wardnet/inforge/internal/yamldoc"
)

// testdataDir reuses the validate package's fixture environments.
var testdataDir = filepath.Join("..", "validate", "testdata")

// Reading never resolves: the document keeps its patterns and needs no
// environment. Resolution is a separate act, performed by a consumer that wants
// a real value.
func TestVariablesLiteralKeepsPatterns(t *testing.T) {
	vars, err := VariablesLiteral("ok", testdataDir)
	require.NoError(t, err)
	assert.Equal(t, "example.com", vars.BaseDomain)
	assert.Equal(t, "ssh-ed25519 AAAA...authorized", vars.SSH.AuthorizedKeys)
}

// The whole-file resolved decode: the deploy program's path. Every leaf goes
// through the chain, so a missing value is reported before anything is created.
func TestVariablesResolvesEveryLeaf(t *testing.T) {
	dir := writeVariablesYAML(t, `base_domain: env:INFORGE_TEST_DOMAIN
ssh:
  authorizedKeys: env:INFORGE_TEST_KEYS
`)
	chain := yamldoc.Chain[string]{yamldoc.EnvFrom(func(k string) (string, bool) {
		return map[string]string{
			"INFORGE_TEST_DOMAIN": "example.com",
			"INFORGE_TEST_KEYS":   "ssh-ed25519 AAAA",
		}[k], true
	})}

	vars, err := Variables(context.Background(), chain, "prd", dir)
	require.NoError(t, err)
	assert.Equal(t, "example.com", vars.BaseDomain)
	assert.Equal(t, "ssh-ed25519 AAAA", vars.SSH.AuthorizedKeys)
}

// THE REGRESSION TEST. A consumer that reads ONE leaf cannot be failed by a
// pattern in a leaf it never reads. Under the old eager loader the whole document
// was expanded on load, so `inforge pki renew` and `inforge releases deploy` died
// with "missing required env var: SSH_AUTHORIZED_KEYS" while minting a leaf — a
// code path that reads base_domain and nothing else. Only tunneller broke, because
// mtls_files: is what makes a release mint a leaf at all.
func TestReadingOneLeafIgnoresPatternsInOtherLeaves(t *testing.T) {
	dir := writeVariablesYAML(t, `base_domain: wardnet.network
ssh:
  authorizedKeys: env:SSH_AUTHORIZED_KEYS
  deployPublicKey: env:DEPLOY_PUBLIC_KEY
`)
	// Nothing is set in the environment.
	chain := yamldoc.Chain[string]{yamldoc.EnvFrom(func(string) (string, bool) { return "", false })}

	doc, err := VariablesDoc("prd", dir)
	require.NoError(t, err)

	leaf, ok := doc.At("base_domain")
	require.True(t, ok)
	got, err := leaf.Resolve(context.Background(), chain)
	require.NoError(t, err, "reading base_domain must not require the ssh block")
	assert.Equal(t, "wardnet.network", got)

	// ...and the same document still fails loudly for a consumer that DOES want the
	// leaf whose variable is unset.
	_, err = Variables(context.Background(), chain, "prd", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ssh.authorizedKeys")
	assert.Contains(t, err.Error(), "SSH_AUTHORIZED_KEYS")
}

func writeVariablesYAML(t *testing.T, doc string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "prd"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prd", "variables.yaml"), []byte(doc), 0o644))
	return dir
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

// The literal read of regions.yaml: the ok fixture's dns.zone is a ${...} ref and
// no env var need be set — the pattern simply survives.
func TestRegionsLiteralFromFile(t *testing.T) {
	rt, global, err := RegionsLiteral("ok", testdataDir)
	require.NoError(t, err)
	slug, err := rt.Slug("us-east-1")
	require.NoError(t, err)
	assert.Equal(t, "use1", slug)
	assert.Contains(t, rt["us-east-1"].Providers, "hetzner")
	assert.Nil(t, global, "the ok fixture declares no global slot")

	st, err := LoadSizeTable("ok", testdataDir)
	require.NoError(t, err)
	require.NoError(t, st.Resolve("SMALL"))
}

// An absent regions.yaml yields an empty table (regions.yaml is the deploy
// authority — there is no built-in fallback region set), with no error.
func TestRegionsMissingFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "prd"), 0o755))
	rt, global, err := RegionsLiteral("prd", dir)
	require.NoError(t, err)
	assert.Empty(t, rt)
	assert.Nil(t, global)
}

// writeRegionsYAML writes a regions.yaml with a env:ENV_VAR provider credential
// into a temp env dir and returns the dir.
func writeRegionsYAML(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "prd"), 0o755))
	const doc = `regions:
  us-east-1:
    slug: use1
    providers:
      hetzner:
        apiToken: env:INFORGE_TEST_HCLOUD_TOKEN
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prd", "regions.yaml"), []byte(doc), 0o644))
	return dir
}

// The regression guard for the credential bug: a env:ENV_VAR must be resolved
// before it reaches a provider, never passed through as the literal "${...}".
func TestRegionsResolvesCredentials(t *testing.T) {
	chain := yamldoc.Chain[string]{yamldoc.EnvFrom(func(string) (string, bool) { return "real-token-value", true })}
	rt, _, err := Regions(context.Background(), chain, "prd", writeRegionsYAML(t))
	require.NoError(t, err)
	assert.Equal(t, "real-token-value", rt["us-east-1"].Providers["hetzner"]["apiToken"],
		"the credential must be resolved, not passed through literally")
}

// Resolving fails clearly when a referenced credential is unset — and names the
// leaf, not just the variable.
func TestRegionsMissingCredentialErrors(t *testing.T) {
	chain := yamldoc.Chain[string]{yamldoc.EnvFrom(func(string) (string, bool) { return "", false })}
	_, _, err := Regions(context.Background(), chain, "prd", writeRegionsYAML(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required env var")
	assert.Contains(t, err.Error(), "regions.us-east-1.providers.hetzner.apiToken")
}

// The literal read (what validation uses) leaves a env:ENV_VAR as written even
// with the var unset, so structural validation runs without credentials and an
// unresolved credential never reads as an (empty) missing value.
func TestRegionsLiteralKeepsCredentialAsWritten(t *testing.T) {
	rt, _, err := RegionsLiteral("prd", writeRegionsYAML(t))
	require.NoError(t, err)
	assert.Equal(t, "env:INFORGE_TEST_HCLOUD_TOKEN", rt["us-east-1"].Providers["hetzner"]["apiToken"])
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

package validate

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/nginx"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/sizes"
	"github.com/wardnet/inforge/internal/types"
)

const testdataDir = "testdata"

// captureStdout redirects os.Stdout for the duration of fn and returns what was
// written. The reporter prints per-resource OK/FAIL lines to stdout, so a test can
// assert a specific validation message fired (the returned error is only the
// summary "validation failed").
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	return buf.String()
}

func TestValidateResourcesOK(t *testing.T) {
	err := ValidateResources("ok", testdataDir, types.ProviderDefaults{})
	assert.NoError(t, err, "the ok environment should validate cleanly")
}

func TestValidateResourcesPkiMissingIntermediate(t *testing.T) {
	// The mesh PKI has only the global intermediate; the regional services need a
	// us-east-1 intermediate, so validation fails (the "silent miss" guard).
	err := ValidateResources("pki-missing-intermediate", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "a regional service whose scope has no mesh intermediate should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

func TestValidateResourcesBad(t *testing.T) {
	err := ValidateResources("bad", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "the bad environment should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesProviderDefaultsOK: specs that omit provider: pass when
// project-level defaults cover all resource classes.
func TestValidateResourcesProviderDefaultsOK(t *testing.T) {
	defaults := types.ProviderDefaults{
		Compute:  "hetzner",
		Database: map[string]string{"postgresql": "neon"},
	}
	err := ValidateResources("provider-defaults-ok", testdataDir, defaults)
	assert.NoError(t, err, "specs without provider: should pass when defaults cover all classes")
}

// TestValidateResourcesProviderDefaultsFail: specs that omit provider: fail when
// no defaults are configured.
func TestValidateResourcesProviderDefaultsFail(t *testing.T) {
	err := ValidateResources("provider-defaults-ok", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "specs without provider: should fail when no defaults are configured")
	assert.Contains(t, err.Error(), "validation failed")
}

func TestValidateResourcesNamingAlias(t *testing.T) {
	err := ValidateResources("naming-alias", testdataDir, types.ProviderDefaults{})
	assert.NoError(t, err, "the naming-alias environment should validate cleanly")
}

func TestValidateResourcesNamingAliasMulti(t *testing.T) {
	err := ValidateResources("naming-alias-multi", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "the naming-alias-multi environment should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesGlobalOK validates an environment whose regional secret
// resolves a GLOBAL database output (ref:database/global/shared.connectionUrl) —
// the one allowed cross-region reference.
func TestValidateResourcesGlobalOK(t *testing.T) {
	err := ValidateResources("global-ok", testdataDir, types.ProviderDefaults{})
	assert.NoError(t, err, "a regional secret referencing a global database should validate cleanly")
}

// TestValidateResourcesGlobalBad validates an environment whose GLOBAL secret
// references a REGIONAL database: the global slice is validated in a global-only
// context, so the regional database is not found and validation fails — enforcing
// "global → global only".
func TestValidateResourcesGlobalBad(t *testing.T) {
	err := ValidateResources("global-bad", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "a global resource referencing a regional one should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesGlobalDNSMissing: a global slice with a tls-termination
// service + ingress realizes DNS records and ACME certs against the placement
// region's authority. When that region (us-east-1) declares no dns: block, the
// records/certs would silently never deploy, so validation must reject the slice
// with a clear, actionable message rather than passing as it did before the fix.
func TestValidateResourcesGlobalDNSMissing(t *testing.T) {
	out := captureStdout(t, func() {
		err := ValidateResources("global-dns-missing", testdataDir, types.ProviderDefaults{})
		require.Error(t, err, "a global tls-termination service with no placement-region dns: must fail")
		assert.Contains(t, err.Error(), "validation failed")
	})
	assert.Contains(t, out, "placementRegion \"us-east-1\" has no dns: authority",
		"the failure must name the missing-dns guard, not some unrelated error")
}

// TestValidateResourcesGlobalPlacementUndefined: when placementRegion names an
// undefined region, the DNS guard must stay silent — indexing the region table with
// a bad key yields a zero-value (nil Dns) that would otherwise pile a misleading "no
// dns: authority" error on top of the real "not a defined region" error. The
// operator should see only the latter.
func TestValidateResourcesGlobalPlacementUndefined(t *testing.T) {
	out := captureStdout(t, func() {
		err := ValidateResources("global-placement-undefined", testdataDir, types.ProviderDefaults{})
		require.Error(t, err, "an undefined placementRegion must fail validation")
	})
	assert.Contains(t, out, "is not a defined region", "the real error must be reported")
	assert.NotContains(t, out, "has no dns: authority",
		"the DNS guard must not fire for a region that does not exist")
}

// TestValidateResourcesEncryptedOK: a `vault:KEY` secret whose (container, KEY)
// ciphertext exists in the env's secrets.enc.yaml validates
// cleanly — the check is presence-only, so the fixture ciphertext is a dummy.
func TestValidateResourcesEncryptedOK(t *testing.T) {
	err := ValidateResources("encrypted-ok", testdataDir, types.ProviderDefaults{})
	assert.NoError(t, err, "an encrypted source with a matching store entry should validate cleanly")
}

// TestValidateResourcesEncryptedMissingKey: the store exists but holds no
// ciphertext for the `vault:KEY` under the service's container.
func TestValidateResourcesEncryptedMissingKey(t *testing.T) {
	err := ValidateResources("encrypted-bad", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "an encrypted source without a store entry should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesEncryptedNoStore: a `vault:KEY` secret with no
// secrets.enc.yaml must fail and point at `inforge secret init`.
func TestValidateResourcesEncryptedNoStore(t *testing.T) {
	err := ValidateResources("encrypted-nostore", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "an encrypted source without a store file should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesIngressAppOK: an ingress + app at both regional and global
// scope, each ingress fronting a same-scope compute host and each app resolving
// its ingress within its own scope, validates cleanly.
func TestValidateResourcesIngressAppOK(t *testing.T) {
	err := ValidateResources("ingress-app-ok", testdataDir, types.ProviderDefaults{})
	assert.NoError(t, err, "regional and global ingress/app referencing same-scope hosts should validate cleanly")
}

// TestValidateResourcesAppBadIngress: an app whose ingress: foreign key names no
// ingress resource in its scope fails validation (the same-scope FK rule).
func TestValidateResourcesAppBadIngress(t *testing.T) {
	err := ValidateResources("app-bad-ingress", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "an app referencing an unknown ingress should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesIngressBadHost: an ingress whose host: foreign key names no
// compute in its scope fails validation (the same-scope host FK rule).
func TestValidateResourcesIngressBadHost(t *testing.T) {
	err := ValidateResources("ingress-bad-host", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "an ingress referencing an unknown compute host should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesAppDuplicateSubdomain: two apps in one scope sharing a
// subdomain fail — each app must map to a distinct public FQDN.
func TestValidateResourcesAppDuplicateSubdomain(t *testing.T) {
	err := ValidateResources("app-duplicate-subdomain", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "two apps sharing a subdomain in one scope should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesAppDuplicateName: two apps in one scope sharing a name fail.
// Resource-name uniqueness is enforced generically in validateType (a duplicate
// would silently overwrite the scope's FK-resolution map), so it applies to every
// resource type — see the compute/ingress duplicate-name cases below.
func TestValidateResourcesAppDuplicateName(t *testing.T) {
	err := ValidateResources("app-duplicate-name", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "two apps sharing a name in one scope should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesComputeDuplicateName: two compute folders declaring the same
// `name:` fail — name uniqueness is generic, not an app/ingress special case.
func TestValidateResourcesComputeDuplicateName(t *testing.T) {
	err := ValidateResources("compute-duplicate-name", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "two computes sharing a name in one scope should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesIngressDuplicateName: two ingress folders declaring the same
// `name:` fail (same generic uniqueness path).
func TestValidateResourcesIngressDuplicateName(t *testing.T) {
	err := ValidateResources("ingress-duplicate-name", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "two ingresses sharing a name in one scope should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestCheckComputeGlobalNetworkRejected: a compute attaching to a global network
// is recognized but rejected (cross-region networking not supported yet).
func TestCheckComputeGlobalNetworkRejected(t *testing.T) {
	ctx := baseCtx()
	ctx.sizeTable = sizes.DefaultTable()
	ctx.networks = map[string]types.NetworkSpec{}
	errs, _ := checkCompute(types.ComputeSpec{
		Provider: "hetzner", Network: "global/corenet", Size: "SMALL", Kind: "vm",
	}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0], "not supported yet")
	// The generic "network not found" message must NOT also fire for a global ref.
	for _, e := range errs {
		assert.NotContains(t, e, "not found")
	}
}

// TestCheckServiceGlobalHostRejected: a service on a global host is rejected —
// such a service is defined in the global slice itself, not referenced from a region.
func TestCheckServiceGlobalHostRejected(t *testing.T) {
	ctx := baseCtx()
	errs, _ := checkService(types.ServiceSpec{
		Host: "global/edge-01", Type: "raw", User: "svc",
	}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "defined in the global slice itself")
}

// ingressCtx returns a baseCtx seeded with one ingress "web" (fronting the bridge
// host) so app FK checks resolve, plus an empty app-subdomain count map. (Bare-name
// uniqueness is enforced generically in validateType, not in checkIngress/checkApp.)
func ingressCtx() regionContext {
	ctx := baseCtx()
	ctx.ingressNames = map[string]bool{"web": true}
	ctx.appSubdomainCounts = map[string]int{}
	return ctx
}

// TestCheckIngressValid: an ingress whose host: resolves to a same-scope
// single-instance vm compute passes.
func TestCheckIngressValid(t *testing.T) {
	errs, _ := checkIngress(types.IngressSpec{Name: "web", Host: "bridge"}, ingressCtx())
	assert.Empty(t, errs)
}

// TestCheckIngressGlobalHostRejected: an ingress referencing a global compute is
// rejected — like service.host, it is declared in the global slice itself.
func TestCheckIngressGlobalHostRejected(t *testing.T) {
	errs, _ := checkIngress(types.IngressSpec{Name: "web", Host: "global/edge"}, ingressCtx())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "global slice itself")
}

// TestCheckIngressUnknownHostRejected: an ingress whose host: does not resolve to
// a compute in the same scope fails the foreign-key check.
func TestCheckIngressUnknownHostRejected(t *testing.T) {
	errs, _ := checkIngress(types.IngressSpec{Name: "web", Host: "ghost"}, ingressCtx())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "does not resolve to a compute")
}

// TestCheckAppGlobalIngressRejected: an app referencing a global ingress is
// rejected — like service.host, an app served from a global ingress is declared
// in the global slice itself, not referenced from a region.
func TestCheckAppGlobalIngressRejected(t *testing.T) {
	errs, _ := checkApp(types.AppSpec{Name: "dash", Ingress: "global/web", Subdomain: "my"}, ingressCtx())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "global slice itself")
}

// TestCheckAppUnknownIngressRejected: an app whose ingress: does not resolve to an
// ingress in the same scope fails the foreign-key check.
func TestCheckAppUnknownIngressRejected(t *testing.T) {
	errs, _ := checkApp(types.AppSpec{Name: "dash", Ingress: "ghost", Subdomain: "my"}, ingressCtx())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "does not resolve to an ingress resource")
}

// TestCheckAppValid: an app whose ingress: resolves to a same-scope ingress passes.
func TestCheckAppValid(t *testing.T) {
	errs, _ := checkApp(types.AppSpec{Name: "dash", Ingress: "web", Subdomain: "my", Spa: true}, ingressCtx())
	assert.Empty(t, errs)
}

// TestCheckAppDuplicateSubdomain: two apps sharing a subdomain in one scope are
// rejected (each app must map to a distinct public FQDN).
func TestCheckAppDuplicateSubdomain(t *testing.T) {
	ctx := ingressCtx()
	ctx.appSubdomainCounts["my"] = 2
	errs, _ := checkApp(types.AppSpec{Name: "dash", Ingress: "web", Subdomain: "my"}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0], "distinct public FQDN")
}

// TestCheckServiceDatabaseRefRejected: a ref:database/* is rejected — a database
// exposes no referenceable outputs (ADR-0025); DB credentials flow only through a
// grant. Regional access to a global database is exercised via grants (a
// database/global/<name> grant) in grant_test.go.
func TestCheckServiceDatabaseRefRejected(t *testing.T) {
	ctx := baseCtx()
	ctx.available = nil // provider availability is checked separately, per region
	ctx.databaseNames = map[string]bool{"global/shared": true}
	errs, _ := checkService(types.ServiceSpec{
		Host: "bridge", Type: "raw", User: "svc", Container: "ghost",
		Environment: map[string]string{"DB": "ref:database/global/shared.connectionUrl"},
	}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "use a grants: entry")
}

// TestCheckSecretsRejectsReservedEnvName: a secret key (which becomes the
// service's env var name) must not claim the reserved INFORGE_* namespace.
func TestCheckSecretsRejectsReservedEnvName(t *testing.T) {
	ctx := baseCtx()
	ctx.available = nil
	errs, _ := checkService(types.ServiceSpec{
		Host: "bridge", Type: "raw", User: "svc", Container: "ghost",
		Environment: map[string]string{"INFORGE_DEPLOYMENT_REGION": "env:SOME_SECRET"},
	}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "reserved")
}

// TestCheckServiceRejectsReservedMeshEnvName: an env var name colliding with a
// reserved mesh certificate path (MTLS_*_PATH) must fail — the host projection
// would otherwise overwrite the user's value with the leaf path.
func TestCheckServiceRejectsReservedMeshEnvName(t *testing.T) {
	ctx := baseCtx()
	ctx.available = nil
	errs, _ := checkService(types.ServiceSpec{
		Host: "bridge", Type: "raw", User: "svc", Container: "ghost",
		Environment: map[string]string{"MTLS_LEAF_CERT_PATH": "env:SOME_SECRET"},
	}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "reserved")
}

// TestCheckServiceRejectsMultilineReload: reload becomes a single ExecReload=
// line in the unit, so a newline would inject extra directives.
func TestCheckServiceRejectsMultilineReload(t *testing.T) {
	ctx := baseCtx()
	ctx.available = nil
	errs, _ := checkService(types.ServiceSpec{
		Host: "bridge", Type: "raw", User: "svc", Container: "ghost",
		Reload: "nginx -s reload\nExecStart=/bin/evil",
	}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "single line")
}

// TestBuildGlobalRefs derives referenceable database names and compute keys from
// the global resource set, keying single-instance computes by both forms.
func TestBuildGlobalRefs(t *testing.T) {
	g := buildGlobalRefs(types.Resources{
		Database: []types.DatabaseSpec{{Name: "shared"}},
		Compute:  []types.ComputeSpec{{Name: "edge", Kind: "vm", InstanceCount: 1}},
	})
	assert.True(t, g.databaseNames["shared"])
	assert.Equal(t, "vm", g.computeKind["edge"])
	assert.Equal(t, "vm", g.computeKind["edge-01"])
}

// TestCheckRegionsFile exercises the regions.yaml validation paths directly: the
// ok/bad fixtures only cover the happy path and base_domain, so the per-region
// slug/providers and global checks need explicit coverage.
func TestCheckRegionsFile(t *testing.T) {
	withProviders := map[string]map[string]any{"hetzner": {}}

	t.Run("valid", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: withProviders},
		}, nil, "regions.yaml")
		assert.False(t, r.failed)
	})

	t.Run("empty table", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{}, nil, "regions.yaml")
		assert.True(t, r.failed)
	})

	t.Run("missing slug", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Providers: withProviders},
		}, nil, "regions.yaml")
		assert.True(t, r.failed)
	})

	t.Run("empty providers block", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1"},
		}, nil, "regions.yaml")
		assert.True(t, r.failed)
	})

	t.Run("global without providers", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: withProviders},
		}, &regions.Global{PlacementRegion: "us-east-1"}, "regions.yaml")
		assert.True(t, r.failed)
	})

	t.Run("global missing placementRegion", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: withProviders},
		}, &regions.Global{Providers: withProviders}, "regions.yaml")
		assert.True(t, r.failed)
	})

	t.Run("global unknown placementRegion", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: withProviders},
		}, &regions.Global{PlacementRegion: "eu-west-1", Providers: withProviders}, "regions.yaml")
		assert.True(t, r.failed)
	})

	t.Run("global with providers and placementRegion", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: withProviders},
		}, &regions.Global{PlacementRegion: "us-east-1", Providers: withProviders}, "regions.yaml")
		assert.False(t, r.failed)
	})

	t.Run("dns authority with provider and zone", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: withProviders,
				Dns: &regions.DnsAuthority{Provider: "cloudflare", Zone: "z1"}},
		}, nil, "regions.yaml")
		assert.False(t, r.failed)
	})

	t.Run("dns authority missing zone fails", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: withProviders,
				Dns: &regions.DnsAuthority{Provider: "cloudflare"}},
		}, nil, "regions.yaml")
		assert.True(t, r.failed)
	})
}

// TestCheckProviderAvailabilityPerRegion confirms the single shared resource set
// must have each declared provider available in EVERY region it deploys into: a
// provider present in one region but absent from another fails for the region
// that lacks it. It runs against the ok fixture (which uses hetzner, cloudflare,
// neon and infisical).
func TestCheckProviderAvailabilityPerRegion(t *testing.T) {
	full := map[string]map[string]any{
		"hetzner": {}, "cloudflare": {}, "neon": {}, "infisical": {},
	}

	t.Run("all providers in every region", func(t *testing.T) {
		r := &reporter{}
		table := regions.Table{
			"us-east-1":    {Slug: "use1", Providers: full},
			"eu-central-1": {Slug: "euc1", Providers: full},
		}
		require.NoError(t, checkProviderAvailability(r, filepath.Join(testdataDir, "ok", "regional"), table, types.ProviderDefaults{}))
		assert.False(t, r.failed, "every region declares every provider the shared set uses")
	})

	t.Run("provider missing in one region", func(t *testing.T) {
		r := &reporter{}
		// eu-central-1 omits neon, which the shared database resource requires.
		noNeon := map[string]map[string]any{
			"hetzner": {}, "cloudflare": {}, "infisical": {},
		}
		table := regions.Table{
			"us-east-1":    {Slug: "use1", Providers: full},
			"eu-central-1": {Slug: "euc1", Providers: noNeon},
		}
		require.NoError(t, checkProviderAvailability(r, filepath.Join(testdataDir, "ok", "regional"), table, types.ProviderDefaults{}))
		assert.True(t, r.failed, "neon is unavailable in eu-central-1")
	})

	t.Run("secrets provider missing", func(t *testing.T) {
		r := &reporter{}
		// The ok set has a service that declares secrets; a region with no
		// secrets provider (infisical) can't write them, so it must fail
		// validation rather than surfacing only at deploy time.
		noSecrets := map[string]map[string]any{
			"hetzner": {}, "cloudflare": {}, "neon": {},
		}
		table := regions.Table{
			"us-east-1": {Slug: "use1", Providers: noSecrets},
		}
		require.NoError(t, checkProviderAvailability(r, filepath.Join(testdataDir, "ok", "regional"), table, types.ProviderDefaults{}))
		assert.True(t, r.failed, "a service declares secrets but the region has no secrets provider")
	})
}

// baseCtx returns a regionContext with one vm host (bridge) and hetzner
// available, for exercising the per-spec semantic checks directly.
func baseCtx() regionContext {
	return regionContext{
		available:        map[string]bool{"hetzner": true},
		computeNames:     map[string]bool{"bridge": true},
		computeKind:      map[string]string{"bridge-01": "vm"},
		computeCanonical: map[string]string{"bridge-01": "bridge-01", "bridge": "bridge-01"},
		computeDeployer:  map[string]bool{"bridge-01": true},
	}
}

// meshCtx seeds a baseCtx with a single-scope gateway and two mesh services (ddns,
// tunneller) that permit the gateway, plus a non-mesh service (nopki), so gateway
// routes and mesh allow-lists resolve (ADR-0032).
func meshCtx() regionContext {
	c := baseCtx()
	c.gatewayScopeCount = 1
	c.meshServices = map[string]bool{"ddns": true, "tunneller": true}
	c.serviceNamesInScope = map[string]bool{"ddns": true, "tunneller": true, "nopki": true}
	c.serviceAllowsGateway = map[string]bool{"ddns": true, "tunneller": true}
	c.servicePkiByName = map[string]string{"ddns": "mesh", "tunneller": "mesh"}
	c.targetUsersByHost = map[string]map[int][]string{}
	c.portUsersByHost = map[string]map[int][]string{}
	return c
}

func meshSvc(m *types.MeshSpec) types.ServiceSpec {
	return types.ServiceSpec{Name: "tenants", Host: "bridge", Type: "raw", User: "svc", Pki: "mesh", Mesh: m}
}

func gw(routes ...types.GatewayRouteSpec) types.GatewaySpec {
	return types.GatewaySpec{Name: "api", Host: "bridge", Pki: "mesh", Subdomain: "api", Routes: routes}
}

// TestCheckGatewayValid: a gateway on a same-scope vm, sole in scope, routing to a
// service that permits the gateway, passes.
func TestCheckGatewayValid(t *testing.T) {
	errs, _ := checkGateway(gw(types.GatewayRouteSpec{Path: "/ddns/", Service: "ddns"}), meshCtx())
	assert.Empty(t, errs)
}

// TestCheckGatewayGlobalHostRejected: a gateway referencing a global compute is
// rejected — like service.host, it is declared in the global slice itself.
func TestCheckGatewayGlobalHostRejected(t *testing.T) {
	c := meshCtx()
	errs, _ := checkGateway(types.GatewaySpec{Name: "api", Host: "global/edge", Subdomain: "api"}, c)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "global slice itself")
}

// TestCheckGatewayUnknownHostRejected: a gateway whose host: does not resolve fails.
func TestCheckGatewayUnknownHostRejected(t *testing.T) {
	c := meshCtx()
	errs, _ := checkGateway(types.GatewaySpec{Name: "api", Host: "ghost", Subdomain: "api"}, c)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "does not resolve to a compute")
}

// TestCheckGatewaySingletonRejected: two gateways in one scope are rejected.
func TestCheckGatewaySingletonRejected(t *testing.T) {
	c := meshCtx()
	c.gatewayScopeCount = 2
	errs, _ := checkGateway(gw(), c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "at most one gateway")
}

// TestCheckGatewayRouteUnknownService: a route to a service not in scope is rejected.
func TestCheckGatewayRouteUnknownService(t *testing.T) {
	errs, _ := checkGateway(gw(types.GatewayRouteSpec{Path: "/x/", Service: "ghost"}), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "does not resolve to a service in this scope")
}

// TestCheckGatewayRouteServiceDisallows: a route to a service that does not permit
// the gateway (no "gateway" in its mesh.allowed_services) is rejected.
func TestCheckGatewayRouteServiceDisallows(t *testing.T) {
	errs, _ := checkGateway(gw(types.GatewayRouteSpec{Path: "/n/", Service: "nopki"}), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "does not permit the gateway")
}

// TestCheckGatewayRootPathRejected: a "/" (root) route path is rejected.
func TestCheckGatewayRootPathRejected(t *testing.T) {
	errs, _ := checkGateway(gw(types.GatewayRouteSpec{Path: "/", Service: "ddns"}), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "non-root path prefix")
}

// TestCheckGatewayDuplicatePath: two routes with the same path are rejected.
func TestCheckGatewayDuplicatePath(t *testing.T) {
	errs, _ := checkGateway(gw(
		types.GatewayRouteSpec{Path: "/ddns/", Service: "ddns"},
		types.GatewayRouteSpec{Path: "/ddns/", Service: "tunneller"},
	), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "declared more than once")
}

// TestCheckGatewayPkiMismatch: a route target in a DIFFERENT mesh than the gateway
// is rejected — the callee would never trust the gateway's client leaf.
func TestCheckGatewayPkiMismatch(t *testing.T) {
	c := meshCtx()
	c.servicePkiByName["ddns"] = "other-mesh"
	errs, _ := checkGateway(gw(types.GatewayRouteSpec{Path: "/ddns/", Service: "ddns"}), c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "must share one pki")
}

// TestCheckGatewaySubdomainCollidesWithApp: a gateway subdomain an app already
// claims in the scope is rejected (same flat FQDN namespace: DNS record + ACME).
func TestCheckGatewaySubdomainCollidesWithApp(t *testing.T) {
	c := meshCtx()
	c.appSubdomainCounts = map[string]int{"api": 1}
	errs, _ := checkGateway(gw(), c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "already used by an app")
}

// TestCheckServiceGatewayNameReserved: a service named "gateway" would mint the
// gateway's mesh identity (CN=<scope>/gateway) and forge daemon-originated
// traffic — the name is reserved.
func TestCheckServiceGatewayNameReserved(t *testing.T) {
	c := meshCtx()
	s := types.ServiceSpec{Name: "gateway", Host: "bridge", Type: "raw", User: "svc", Pki: "mesh"}
	errs, _ := checkService(s, c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "reserved for the north-south gateway")
}

// TestCheckMeshValid: a service exposing a mesh port with a resolvable allow list
// (including the reserved "gateway" token) passes.
func TestCheckMeshValid(t *testing.T) {
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080, AllowedServices: []string{"ddns", "gateway"}}), meshCtx())
	assert.Empty(t, errs)
}

// TestCheckMeshRequiresPki: a service with a mesh block but no pki: is rejected.
func TestCheckMeshRequiresPki(t *testing.T) {
	s := meshSvc(&types.MeshSpec{Port: 8080, AllowedServices: []string{"ddns"}})
	s.Pki = ""
	errs, _ := checkService(s, meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "must declare pki:")
}

// TestCheckMeshBadPort: an out-of-range mesh.port is rejected.
func TestCheckMeshBadPort(t *testing.T) {
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 0, AllowedServices: []string{"ddns"}}), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "mesh.port")
}

// TestCheckMeshAllowUnknown: an allow-list name that is no service is rejected.
func TestCheckMeshAllowUnknown(t *testing.T) {
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080, AllowedServices: []string{"ghost"}}), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "does not resolve to a service")
}

// TestCheckMeshAllowNonMesh: an allow-list name that is a service but not a mesh
// member (no pki:) is rejected.
func TestCheckMeshAllowNonMesh(t *testing.T) {
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080, AllowedServices: []string{"nopki"}}), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "not a mesh member")
}

// TestCheckMeshAllowForbiddenDirection: a regional service naming a global service
// as a caller is rejected (regional→global only).
func TestCheckMeshAllowForbiddenDirection(t *testing.T) {
	c := meshCtx()
	c.forbiddenCallerNames = map[string]bool{"billing": true}
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080, AllowedServices: []string{"billing"}}), c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "regional→global only")
}

// TestCheckMeshAllowCrossScopeCaller: a global service may name a regional caller
// (regional→global), resolved via callerCandidates.
func TestCheckMeshAllowCrossScopeCaller(t *testing.T) {
	c := meshCtx()
	c.meshServices = map[string]bool{}
	c.serviceNamesInScope = map[string]bool{}
	c.callerCandidates = map[string]bool{"ddns": true}
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080, AllowedServices: []string{"ddns"}}), c)
	assert.Empty(t, errs)
}

// TestCheckMeshPortCollision: a mesh.port equal to another service's backend port on
// the host is rejected.
func TestCheckMeshPortCollision(t *testing.T) {
	c := meshCtx()
	c.targetUsersByHost = map[string]map[int][]string{"bridge-01": {8080: {"other"}}}
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080, AllowedServices: []string{"ddns"}}), c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "belongs to a single service")
}

// A service may not run on a multi-instance compute: the host DNS / "<compute>.vm"
// record is derived from the bare compute name and can't address one instance.
func TestCheckServiceRejectsMultiInstanceHost(t *testing.T) {
	ctx := baseCtx()
	ctx.computeInstances = map[string]int{"bridge-01": 2}
	errs, _ := checkService(types.ServiceSpec{Name: "api", Host: "bridge", Type: "raw", User: "svc"}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "multi-instance")
}

func TestCheckServiceRejectsSpecKeyHost(t *testing.T) {
	ctx := baseCtx()
	errs, _ := checkService(types.ServiceSpec{Name: "api", Host: "bridge-01", Type: "raw", User: "svc"}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "specKey")
}

// ingressFKCtx seeds a baseCtx with one ingress "web" co-located on the bridge
// host, so a service's ingress FK resolves and its routes are treated as
// co-located (ingress host == service host).
func ingressFKCtx() regionContext {
	c := baseCtx()
	c.ingressNames = map[string]bool{"web": true}
	c.ingressHost = map[string]string{"web": "bridge-01"}
	c.computeNetwork = map[string]string{"bridge-01": "net", "bridge": "net"}
	return c
}

func TestCheckServiceIngress(t *testing.T) {
	svc := func(in ...types.RouteSpec) types.ServiceSpec {
		return types.ServiceSpec{Host: "bridge", Type: "raw", User: "svc", Ingress: "web", Routes: in}
	}

	// tls-termination route fronted by a resolvable ingress -> OK.
	errs, _ := checkService(svc(types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}), ingressFKCtx())
	assert.Empty(t, errs)

	// A forward route is equally fine -> OK.
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeForward, Listen: 853, Target: 5353}), ingressFKCtx())
	assert.Empty(t, errs)

	// No routes (and no ingress) -> OK.
	errs, _ = checkService(types.ServiceSpec{Host: "bridge", Type: "raw", User: "svc"}, baseCtx())
	assert.Empty(t, errs)

	// Routes without an ingress FK -> FAIL.
	errs, _ = checkService(types.ServiceSpec{Host: "bridge", Type: "raw", User: "svc",
		Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}}}, baseCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "must name the ingress")

	// An ingress FK that resolves to no ingress in scope -> FAIL.
	errs, _ = checkService(types.ServiceSpec{Host: "bridge", Type: "raw", User: "svc", Ingress: "ghost",
		Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}}}, baseCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "does not resolve to an ingress")
}

// TestCheckServiceCrossHostSameNetwork: a service whose backend host is on a
// different network than its ingress host fails the same-network rule; sharing a
// network passes.
func TestCheckServiceCrossHostSameNetwork(t *testing.T) {
	ctx := baseCtx()
	// Two hosts: bridge (backend) and edge (ingress).
	ctx.computeNames["edge"] = true
	ctx.computeKind["edge-01"] = "vm"
	ctx.computeCanonical["edge"] = "edge-01"
	ctx.computeCanonical["edge-01"] = "edge-01"
	ctx.ingressNames = map[string]bool{"web": true}
	ctx.ingressHost = map[string]string{"web": "edge-01"}
	ctx.computeNetwork = map[string]string{"bridge-01": "back-net", "bridge": "back-net", "edge-01": "edge-net", "edge": "edge-net"}

	svc := types.ServiceSpec{Name: "svc", Host: "bridge", Type: "raw", User: "svc", Ingress: "web",
		Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}}}
	errs, _ := checkService(svc, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "share a network")

	// Same network -> OK.
	ctx.computeNetwork["edge-01"] = "back-net"
	ctx.computeNetwork["edge"] = "back-net"
	errs, _ = checkService(svc, ctx)
	assert.Empty(t, errs)
}

// TestCheckServiceCrossHostTargetCollidesWithBackendListen: a cross-host service
// whose target port equals a public listen port held by nginx on its OWN backend
// host (because another ingress is co-located there) is rejected — the backend
// process could not bind that port. The collision check keys on the backend host,
// not the ingress host.
func TestCheckServiceCrossHostTargetCollidesWithBackendListen(t *testing.T) {
	ctx := baseCtx()
	// edge is the (different) ingress host; bridge is the backend host, which also
	// runs nginx (holding :9000) for some co-located ingress.
	ctx.computeNames["edge"] = true
	ctx.computeKind["edge-01"] = "vm"
	ctx.computeCanonical["edge"] = "edge-01"
	ctx.computeCanonical["edge-01"] = "edge-01"
	ctx.ingressNames = map[string]bool{"web": true}
	ctx.ingressHost = map[string]string{"web": "edge-01"} // cross-host: ingress on edge, service on bridge
	ctx.computeNetwork = map[string]string{"bridge-01": "net", "bridge": "net", "edge-01": "net", "edge": "net"}
	ctx.portUsersByHost = map[string]map[int][]string{"bridge-01": {9000: {"other"}}}
	ctx.targetUsersByHost = map[string]map[int][]string{}
	ctx.tlsTermIngressByHost = map[string]bool{}

	errs, _ := checkService(types.ServiceSpec{Name: "api", Host: "bridge", Type: "raw", User: "svc", Ingress: "web",
		Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 9000}}}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "collides with a public listen port on the backend host")
}

func TestCheckServiceIngressRules(t *testing.T) {
	base := func() regionContext {
		c := ingressFKCtx()
		c.portUsersByHost = map[string]map[int][]string{}
		c.forwardUsersByHost = map[string]map[int][]string{}
		c.targetUsersByHost = map[string]map[int][]string{}
		c.tlsTermIngressByHost = map[string]bool{}
		c.ingressHealthPort = map[string]int{}
		return c
	}
	svc := func(in ...types.RouteSpec) types.ServiceSpec {
		return types.ServiceSpec{Name: "svc", Host: "bridge", Type: "raw", User: "svc", Ingress: "web", Routes: in}
	}

	// A tls-termination + a forward route on one service is the bridge shape -> OK.
	ctx := base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{443: {"svc"}, 853: {"svc"}}
	ctx.tlsTermIngressByHost["bridge-01"] = true
	errs, _ := checkService(svc(
		types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Vanity: []string{"key-broker.inforge.example.com"}},
		types.RouteSpec{Type: types.IngressTypeForward, Listen: 853, Target: 5353},
	), ctx)
	assert.Empty(t, errs)

	// An invalid type is rejected.
	errs, _ = checkService(svc(types.RouteSpec{Type: "passthrough", Listen: 443, Target: 8080}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "is invalid")

	// listen is required on every route.
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeTLSTermination, Target: 8080}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "listen")

	// target is required on every route.
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "target")

	// listen and target must differ when co-located (nginx occupies the public port).
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 443}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "must differ")

	// target collides with ANOTHER route's public listen port on the ingress host -> FAIL.
	ctx = base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{443: {"svc"}, 8080: {"other"}}
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "collides with a public listen port")

	// two different services binding the same backend target -> FAIL.
	ctx = base()
	ctx.targetUsersByHost["bridge-01"] = map[int][]string{8080: {"svc", "other"}}
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "belongs to a single service")

	// vanity on a forward is meaningless -> FAIL.
	ctx = base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{853: {"svc"}}
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeForward, Listen: 853, Target: 5353, Vanity: []string{"x"}}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "remove vanity")

	// Two forwards share one port -> FAIL (one passthrough per port: a single map default).
	ctx = base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{443: {"svc", "other"}}
	ctx.forwardUsersByHost["bridge-01"] = map[int][]string{443: {"svc", "other"}}
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeForward, Listen: 443, Target: 8080}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "single-service-exclusive")

	// A forward coexisting with a tls-termination on the SAME port -> OK (ssl_preread
	// demuxes: known SNIs to the terminator, the unknown SNI to the forward). Only
	// this service has the forward on 443, so it is the single map default.
	ctx = base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{443: {"svc", "other"}}
	ctx.forwardUsersByHost["bridge-01"] = map[int][]string{443: {"svc"}}
	ctx.tlsTermIngressByHost["bridge-01"] = true
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeForward, Listen: 443, Target: 8080}), ctx)
	assert.Empty(t, errs)

	// A forward on :80 collides with a tls-termination on the ingress (ACME owns :80).
	ctx = base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{80: {"svc"}}
	ctx.tlsTermIngressByHost["bridge-01"] = true
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeForward, Listen: 80, Target: 8080}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "ACME owns :80")
}

func TestCheckServiceHealthRules(t *testing.T) {
	base := func() regionContext {
		c := ingressFKCtx()
		c.portUsersByHost = map[string]map[int][]string{}
		c.forwardUsersByHost = map[string]map[int][]string{}
		c.targetUsersByHost = map[string]map[int][]string{}
		c.tlsTermIngressByHost = map[string]bool{}
		c.ingressHealthPort = map[string]int{"web": 81}
		return c
	}
	svc := func(hp int, routes ...types.RouteSpec) types.ServiceSpec {
		return types.ServiceSpec{Name: "svc", Host: "bridge", Type: "raw", User: "svc", Ingress: "web", HealthProbesPort: hp, Routes: routes}
	}

	// A backend health port distinct from everything -> OK.
	errs, _ := checkService(svc(8081, types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}), base())
	assert.Empty(t, errs)

	// Co-located backend health port == the ingress's public health port -> FAIL.
	errs, _ = checkService(svc(81), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "public health port")

	// Backend health port collides with the service's own route target -> FAIL.
	errs, _ = checkService(svc(8080, types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "route target")

	// Co-located health port in the reserved internal loopback range -> FAIL.
	errs, _ = checkService(svc(nginx.LoopbackBase), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "reserved internal range")

	// Backend health port collides with a public listen port on the backend host -> FAIL.
	pubCtx := base()
	pubCtx.portUsersByHost["bridge-01"] = map[int][]string{9999: {"other"}}
	errs, _ = checkService(svc(9999), pubCtx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "public listen port")

	// Backend health port is another service's backend target on the same host -> FAIL.
	tgtCtx := base()
	tgtCtx.targetUsersByHost["bridge-01"] = map[int][]string{9998: {"other"}}
	errs, _ = checkService(svc(9998), tgtCtx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "belongs to a single service")

	// A health endpoint without an ingress -> FAIL.
	s := svc(8081)
	s.Ingress = ""
	errs, _ = checkService(s, base())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "|"), "must name the ingress")
}

func TestCheckIngressHealthPort(t *testing.T) {
	base := func(healthPort int) (types.IngressSpec, regionContext) {
		// baseCtx already resolves "bridge" -> single-instance vm "bridge-01".
		c := baseCtx()
		c.portUsersByHost = map[string]map[int][]string{"bridge-01": {443: {"svc"}}}
		return types.IngressSpec{Name: "edge", Container: "bridge", Host: "bridge", HealthProbesPort: healthPort}, c
	}

	// Health port 81, distinct from the 443 listen -> no health error.
	s, ctx := base(81)
	errs, _ := checkIngress(s, ctx)
	assert.NotContains(t, strings.Join(errs, "|"), "health_probes_port")

	// Health port equals a route listen on this host -> FAIL.
	s, ctx = base(443)
	errs, _ = checkIngress(s, ctx)
	assert.Contains(t, strings.Join(errs, "|"), "collides with a route listen port")

	// Health port 80 is reserved for ACME -> FAIL.
	s, ctx = base(80)
	errs, _ = checkIngress(s, ctx)
	assert.Contains(t, strings.Join(errs, "|"), "must not be 80")

	// Health port in the reserved ssl_preread loopback range -> FAIL.
	s, ctx = base(nginx.LoopbackBase)
	errs, _ = checkIngress(s, ctx)
	assert.Contains(t, strings.Join(errs, "|"), "reserved internal range")

	// Two ingresses sharing one compute host -> FAIL.
	s, ctx = base(81)
	ctx.ingressNamesByHost = map[string][]string{"bridge-01": {"other-ingress", "edge"}}
	errs, _ = checkIngress(s, ctx)
	assert.Contains(t, strings.Join(errs, "|"), "hosts at most one ingress")
}

func TestCheckServiceExposedPorts(t *testing.T) {
	// A private-only service: no ingress, no routes, just exposed_ports.
	svc := func(eps ...types.ExposedPort) types.ServiceSpec {
		return types.ServiceSpec{Name: "tunneller", Host: "bridge", Type: "raw", User: "svc", ExposedPorts: eps}
	}
	tcp := func(p int) types.ExposedPort { return types.ExposedPort{Proto: "tcp", Port: p} }
	udp := func(p int) types.ExposedPort { return types.ExposedPort{Proto: "udp", Port: p} }

	// Private-only service with no ingress -> OK (exposed_ports require no ingress).
	errs, _ := checkService(svc(tcp(9444)), baseCtx())
	assert.Empty(t, errs)

	// tcp/N and udp/N coexist on one service (distinct OS binds) -> OK.
	errs, _ = checkService(svc(tcp(9444), udp(9444)), baseCtx())
	assert.Empty(t, errs)

	// Duplicate (proto, port) on the same service -> FAIL.
	errs, _ = checkService(svc(tcp(9444), tcp(9444)), baseCtx())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "declared more than once")

	// tcp exposed port equals the service's own route target -> FAIL.
	s := svc(tcp(8080))
	s.Ingress = "web"
	s.Routes = []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}}
	errs, _ = checkService(s, ingressFKCtx())
	assert.Contains(t, strings.Join(errs, "|"), "route target")

	// tcp exposed port equals the service's own health_probes_port -> FAIL.
	s = svc(tcp(8081))
	s.Ingress = "web"
	s.HealthProbesPort = 8081
	c := ingressFKCtx()
	c.ingressHealthPort = map[string]int{"web": 81}
	errs, _ = checkService(s, c)
	assert.Contains(t, strings.Join(errs, "|"), "health_probes_port")

	// tcp exposed port equals a public listen port on the backend host -> FAIL.
	pubCtx := baseCtx()
	pubCtx.portUsersByHost = map[string]map[int][]string{"bridge-01": {9999: {"other"}}}
	errs, _ = checkService(svc(tcp(9999)), pubCtx)
	assert.Contains(t, strings.Join(errs, "|"), "public listen port")

	// tcp exposed port is another service's backend target on the same host -> FAIL.
	tgtCtx := baseCtx()
	tgtCtx.targetUsersByHost = map[string]map[int][]string{"bridge-01": {9998: {"other"}}}
	errs, _ = checkService(svc(tcp(9998)), tgtCtx)
	assert.Contains(t, strings.Join(errs, "|"), "belongs to a single service")

	// udp exposed port is another service's udp exposed port on the same host -> FAIL.
	udpCtx := baseCtx()
	udpCtx.udpExposedUsersByHost = map[string]map[int][]string{"bridge-01": {9444: {"other"}}}
	errs, _ = checkService(svc(udp(9444)), udpCtx)
	assert.Contains(t, strings.Join(errs, "|"), "belongs to a single service")

	// udp exposed port does NOT collide with another service's TCP backend target -> OK.
	mixCtx := baseCtx()
	mixCtx.targetUsersByHost = map[string]map[int][]string{"bridge-01": {9444: {"other"}}}
	errs, _ = checkService(svc(udp(9444)), mixCtx)
	assert.Empty(t, errs)

	// tcp exposed port in the reserved loopback range when the host runs an ingress -> FAIL.
	loopCtx := baseCtx()
	loopCtx.ingressNamesByHost = map[string][]string{"bridge-01": {"edge"}}
	errs, _ = checkService(svc(tcp(nginx.LoopbackBase)), loopCtx)
	assert.Contains(t, strings.Join(errs, "|"), "reserved internal range")

	// Same reserved-range port, but the host runs no ingress -> OK (no terminator).
	errs, _ = checkService(svc(tcp(nginx.LoopbackBase)), baseCtx())
	assert.Empty(t, errs)

	// An invalid proto -> FAIL.
	errs, _ = checkService(svc(types.ExposedPort{Proto: "sctp", Port: 9444}), baseCtx())
	assert.Contains(t, strings.Join(errs, "|"), "proto")
}

// TestCheckServiceHealthCrossHostNetwork: a health-only service (no routes) whose
// host is on a different network than its ingress is rejected — the same-network
// rule now covers health, not just routes.
func TestCheckServiceHealthCrossHostNetwork(t *testing.T) {
	c := baseCtx()
	c.computeNames = map[string]bool{"bridge": true, "gateway": true}
	c.computeCanonical = map[string]string{"bridge-01": "bridge-01", "bridge": "bridge-01", "gateway-01": "gateway-01", "gateway": "gateway-01"}
	c.computeKind = map[string]string{"bridge-01": "vm", "gateway-01": "vm"}
	c.computeDeployer = map[string]bool{"gateway-01": true}
	c.computeNetwork = map[string]string{"bridge-01": "net", "gateway-01": "othernet"}
	c.ingressNames = map[string]bool{"web": true}
	c.ingressHost = map[string]string{"web": "bridge-01"}
	c.ingressHealthPort = map[string]int{"web": 81}
	c.portUsersByHost = map[string]map[int][]string{}
	c.targetUsersByHost = map[string]map[int][]string{}

	// gateway (othernet) hosts the service; ingress web is on bridge (net) -> FAIL.
	s := types.ServiceSpec{Name: "probe", Host: "gateway", Type: "raw", User: "probe", Ingress: "web", HealthProbesPort: 8081}
	errs, _ := checkService(s, c)
	assert.Contains(t, strings.Join(errs, "|"), "share a network")
}

func TestCheckServiceDeployUser(t *testing.T) {
	// A service whose host declares no deploy_user can't be provisioned over SSH.
	ctx := baseCtx()
	ctx.computeDeployer = map[string]bool{"bridge-01": false}
	errs, _ := checkService(types.ServiceSpec{Host: "bridge", Type: "raw", User: "svc"}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "no deploy_user")
}

func TestCheckServiceUser(t *testing.T) {
	// A service that declares no user has no account for the agent to drop
	// privilege to before exec.
	ctx := baseCtx()
	errs, _ := checkService(types.ServiceSpec{Host: "bridge", Type: "raw"}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "must declare the no-login user")
}

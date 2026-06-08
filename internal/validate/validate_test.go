package validate

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/sizes"
	"github.com/wardnet/inforge/internal/types"
)

const testdataDir = "testdata"

func TestValidateResourcesOK(t *testing.T) {
	err := ValidateResources("ok", testdataDir)
	assert.NoError(t, err, "the ok environment should validate cleanly")
}

func TestValidateResourcesBad(t *testing.T) {
	err := ValidateResources("bad", testdataDir)
	require.Error(t, err, "the bad environment should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

func TestValidateResourcesNamingAlias(t *testing.T) {
	err := ValidateResources("naming-alias", testdataDir)
	assert.NoError(t, err, "the naming-alias environment should validate cleanly")
}

func TestValidateResourcesNamingAliasMulti(t *testing.T) {
	err := ValidateResources("naming-alias-multi", testdataDir)
	require.Error(t, err, "the naming-alias-multi environment should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesGlobalOK validates an environment whose regional secret
// resolves a GLOBAL database output (ref:database/global/shared.connectionUrl) —
// the one allowed cross-region reference.
func TestValidateResourcesGlobalOK(t *testing.T) {
	err := ValidateResources("global-ok", testdataDir)
	assert.NoError(t, err, "a regional secret referencing a global database should validate cleanly")
}

// TestValidateResourcesGlobalBad validates an environment whose GLOBAL secret
// references a REGIONAL database: the global slice is validated in a global-only
// context, so the regional database is not found and validation fails — enforcing
// "global → global only".
func TestValidateResourcesGlobalBad(t *testing.T) {
	err := ValidateResources("global-bad", testdataDir)
	require.Error(t, err, "a global resource referencing a regional one should fail validation")
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
		Provider: "hetzner", Host: "global/edge-01", Type: "raw", User: "svc",
	}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "defined in the global slice itself")
}

// TestCheckSecretsGlobalDatabaseRef: a regional secret resolves a global database
// when the regional context is seeded with the global/<name> key (as
// validateResourceSet does from buildGlobalRefs).
func TestCheckSecretsGlobalDatabaseRef(t *testing.T) {
	ctx := baseCtx()
	ctx.available = nil // provider availability is checked separately, per region
	ctx.databaseNames = map[string]bool{"global/shared": true}
	errs, _ := checkSecrets(types.SecretsSpec{
		Provider: "infisical",
		Secrets: map[string]types.SecretsEntry{
			"DB": {Source: "ref:database/global/shared.connectionUrl"},
		},
	}, ctx)
	assert.Empty(t, errs, "a regional secret may reference a global database output")

	// Without the global seed the same ref is not found.
	ctx2 := baseCtx()
	ctx2.available = nil
	ctx2.databaseNames = map[string]bool{}
	errs, _ = checkSecrets(types.SecretsSpec{
		Provider: "infisical",
		Secrets: map[string]types.SecretsEntry{
			"DB": {Source: "ref:database/global/shared.connectionUrl"},
		},
	}, ctx2)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], `database "global/shared" not found`)
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
		}, &regions.Global{}, "regions.yaml")
		assert.True(t, r.failed)
	})

	t.Run("global with providers", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: withProviders},
		}, &regions.Global{Providers: withProviders}, "regions.yaml")
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
		require.NoError(t, checkProviderAvailability(r, filepath.Join(testdataDir, "ok"), table))
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
		require.NoError(t, checkProviderAvailability(r, filepath.Join(testdataDir, "ok"), table))
		assert.True(t, r.failed, "neon is unavailable in eu-central-1")
	})
}

// baseCtx returns a regionContext with one vm host (bridge-01) and hetzner
// available, for exercising the per-spec semantic checks directly.
func baseCtx() regionContext {
	return regionContext{
		available:        map[string]bool{"hetzner": true},
		computeKind:      map[string]string{"bridge-01": "vm"},
		computeCanonical: map[string]string{"bridge-01": "bridge-01"},
		computeDeployer:  map[string]bool{"bridge-01": true},
		tlsByCompute:     map[string]bool{},
	}
}

func TestCheckTLSTermination(t *testing.T) {
	ctx := baseCtx()

	errs, _ := checkTLSTermination(types.TLSTerminationSpec{Provider: "hetzner", Compute: "bridge-01"}, ctx)
	assert.Empty(t, errs, "a terminator on a vm host should validate")

	errs, _ = checkTLSTermination(types.TLSTerminationSpec{Provider: "hetzner", Compute: "ghost-01"}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "does not resolve to a compute instance")

	errs, _ = checkTLSTermination(types.TLSTerminationSpec{Provider: "nope", Compute: "bridge-01"}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "not defined in this region's regions.yaml providers")

	// A terminator on a host with no deploy_user can't be realized over SSH.
	noDeployer := baseCtx()
	noDeployer.computeDeployer = map[string]bool{"bridge-01": false}
	errs, _ = checkTLSTermination(types.TLSTerminationSpec{Provider: "hetzner", Compute: "bridge-01"}, noDeployer)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "no deploy_user")
}

// A service may not run on a multi-instance compute: the host DNS / "<compute>.vm"
// record is derived from the bare compute name and can't address one instance.
func TestCheckServiceRejectsMultiInstanceHost(t *testing.T) {
	ctx := baseCtx()
	ctx.computeInstances = map[string]int{"bridge-01": 2}
	errs, _ := checkService(types.ServiceSpec{Name: "api", Provider: "hetzner", Host: "bridge-01", Type: "raw", User: "svc"}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "multi-instance")
}

func TestCheckServiceIngress(t *testing.T) {
	ingress := []types.IngressSpec{{Port: 8080, TLS: types.IngressTLSTerminate}}

	// Ingress with a terminator on the same host -> OK.
	ctx := baseCtx()
	ctx.tlsByCompute["bridge-01"] = true
	errs, _ := checkService(types.ServiceSpec{Provider: "hetzner", Host: "bridge-01", Type: "raw", User: "svc", Ingress: ingress}, ctx)
	assert.Empty(t, errs)

	// Ingress but no terminator targets the host -> FAIL.
	ctx = baseCtx()
	errs, _ = checkService(types.ServiceSpec{Provider: "hetzner", Host: "bridge-01", Type: "raw", User: "svc", Ingress: ingress}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "no tls-termination resource")

	// No ingress -> the terminator requirement does not apply.
	ctx = baseCtx()
	errs, _ = checkService(types.ServiceSpec{Provider: "hetzner", Host: "bridge-01", Type: "raw", User: "svc"}, ctx)
	assert.Empty(t, errs)
}

func TestCheckServiceIngressModes(t *testing.T) {
	base := func() regionContext {
		c := baseCtx()
		c.tlsByCompute["bridge-01"] = true
		return c
	}
	svc := func(in ...types.IngressSpec) types.ServiceSpec {
		return types.ServiceSpec{Name: "svc", Provider: "hetzner", Host: "bridge-01", Type: "raw", User: "svc", Ingress: in}
	}

	// passthrough is accepted.
	errs, _ := checkService(svc(types.IngressSpec{Port: 5432, TLS: types.IngressTLSPassthrough}), base())
	assert.Empty(t, errs)

	// catchall + tls:terminate is contradictory.
	errs, _ = checkService(svc(types.IngressSpec{Port: 9000, Catchall: true, TLS: types.IngressTLSTerminate}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "passthrough-only")

	// More than one catch-all on the host.
	ctx := base()
	ctx.catchallByHost = map[string]int{"bridge-01": 2}
	errs, _ = checkService(svc(types.IngressSpec{Port: 9000, Catchall: true}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "catch-all")

	// proxy_protocol on a terminate route warns (no effect), not an error.
	errs, warns := checkService(svc(types.IngressSpec{Port: 80, ProxyProtocol: "v2"}), base())
	assert.Empty(t, errs)
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0], "proxy_protocol has no effect")

	// A terminate route + a catch-all on one service is the bridge shape -> OK.
	errs, _ = checkService(svc(
		types.IngressSpec{Port: 8080, TLS: types.IngressTLSTerminate, Vanity: []string{"key-broker.inforge.example.com"}},
		types.IngressSpec{Port: 8443, Catchall: true},
	), base())
	assert.Empty(t, errs)

	// Two non-catch-all entries collide on the auto "<svc>.svc" name -> FAIL.
	errs, _ = checkService(svc(
		types.IngressSpec{Port: 8080, TLS: types.IngressTLSTerminate},
		types.IngressSpec{Port: 9090, TLS: types.IngressTLSPassthrough},
	), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "non-catch-all")

	// Vanity on a catch-all is meaningless -> FAIL.
	errs, _ = checkService(svc(types.IngressSpec{Port: 9000, Catchall: true, Vanity: []string{"x"}}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "catchall has no SNI")
}

func TestCheckServiceDeployUser(t *testing.T) {
	// A service whose host declares no deploy_user can't be provisioned over SSH.
	ctx := baseCtx()
	ctx.computeDeployer = map[string]bool{"bridge-01": false}
	errs, _ := checkService(types.ServiceSpec{Provider: "hetzner", Host: "bridge-01", Type: "raw", User: "svc"}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "no deploy_user")
}

func TestCheckServiceUser(t *testing.T) {
	// A service that declares no user has no account for the bootstrapper to drop
	// privilege to before exec.
	ctx := baseCtx()
	errs, _ := checkService(types.ServiceSpec{Provider: "hetzner", Host: "bridge-01", Type: "raw"}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "must declare the no-login user")
}

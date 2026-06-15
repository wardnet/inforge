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

// TestCheckSecretsGlobalDatabaseRef: a regional service secret may resolve a
// global database when the regional context is seeded with the global/<name>
// key (as validateResourceSet does from buildGlobalRefs).
func TestCheckSecretsGlobalDatabaseRef(t *testing.T) {
	ctx := baseCtx()
	ctx.available = nil // provider availability is checked separately, per region
	ctx.databaseNames = map[string]bool{"global/shared": true}
	errs, _ := checkService(types.ServiceSpec{
		Host: "bridge", Type: "raw", User: "svc", Container: "ghost",
		Environment: map[string]string{"DB": "ref:database/global/shared.connectionUrl"},
	}, ctx)
	assert.Empty(t, errs, "a regional secret may reference a global database output")

	// Without the global seed the same ref is not found.
	ctx2 := baseCtx()
	ctx2.available = nil
	ctx2.databaseNames = map[string]bool{}
	errs, _ = checkService(types.ServiceSpec{
		Host: "bridge", Type: "raw", User: "svc", Container: "ghost",
		Environment: map[string]string{"DB": "ref:database/global/shared.connectionUrl"},
	}, ctx2)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], `database "global/shared" not found`)
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

func TestCheckServiceIngress(t *testing.T) {
	svc := func(in ...types.IngressSpec) types.ServiceSpec {
		return types.ServiceSpec{Host: "bridge", Type: "raw", User: "svc", Ingress: in}
	}

	// tls-termination needs no host resource — nginx is realized from ingress -> OK.
	ctx := baseCtx()
	errs, _ := checkService(svc(types.IngressSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}), ctx)
	assert.Empty(t, errs)

	// A forward entry is equally fine without any host resource -> OK.
	ctx = baseCtx()
	errs, _ = checkService(svc(types.IngressSpec{Type: types.IngressTypeForward, Listen: 853, Target: 5353}), ctx)
	assert.Empty(t, errs)

	// No ingress -> OK.
	ctx = baseCtx()
	errs, _ = checkService(types.ServiceSpec{Host: "bridge", Type: "raw", User: "svc"}, ctx)
	assert.Empty(t, errs)
}

func TestCheckServiceIngressRules(t *testing.T) {
	base := func() regionContext {
		c := baseCtx()
		c.portUsersByHost = map[string]map[int][]string{}
		c.targetUsersByHost = map[string]map[int][]string{}
		c.tlsTermIngressByHost = map[string]bool{}
		return c
	}
	svc := func(in ...types.IngressSpec) types.ServiceSpec {
		return types.ServiceSpec{Name: "svc", Host: "bridge", Type: "raw", User: "svc", Ingress: in}
	}

	// A tls-termination + a forward entry on one service is the bridge shape -> OK.
	ctx := base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{443: {"svc"}, 853: {"svc"}}
	ctx.tlsTermIngressByHost["bridge-01"] = true
	errs, _ := checkService(svc(
		types.IngressSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Vanity: []string{"key-broker.inforge.example.com"}},
		types.IngressSpec{Type: types.IngressTypeForward, Listen: 853, Target: 5353},
	), ctx)
	assert.Empty(t, errs)

	// An invalid type is rejected.
	errs, _ = checkService(svc(types.IngressSpec{Type: "passthrough", Listen: 443, Target: 8080}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "is invalid")

	// listen is required on every entry.
	errs, _ = checkService(svc(types.IngressSpec{Type: types.IngressTypeTLSTermination, Target: 8080}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "listen")

	// target is required on every entry.
	errs, _ = checkService(svc(types.IngressSpec{Type: types.IngressTypeTLSTermination, Listen: 443}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "needs a target")

	// listen and target must differ (nginx occupies the public port on loopback too).
	errs, _ = checkService(svc(types.IngressSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 443}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "must differ")

	// target collides with ANOTHER entry's public listen port on the host -> FAIL.
	ctx = base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{443: {"svc"}, 8080: {"other"}}
	errs, _ = checkService(svc(types.IngressSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "collides with a public listen port")

	// two different services binding the same loopback target -> FAIL.
	ctx = base()
	ctx.targetUsersByHost["bridge-01"] = map[int][]string{8080: {"svc", "other"}}
	errs, _ = checkService(svc(types.IngressSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "belongs to a single service")

	// vanity on a forward is meaningless -> FAIL.
	ctx = base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{853: {"svc"}}
	errs, _ = checkService(svc(types.IngressSpec{Type: types.IngressTypeForward, Listen: 853, Target: 5353, Vanity: []string{"x"}}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "remove vanity")

	// A forward port shared with another service -> FAIL (single-service-exclusive).
	ctx = base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{443: {"svc", "other"}}
	errs, _ = checkService(svc(types.IngressSpec{Type: types.IngressTypeForward, Listen: 443, Target: 8080}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "single-service-exclusive")

	// A forward on :80 collides with a tls-termination on the host (ACME owns :80).
	ctx = base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{80: {"svc"}}
	ctx.tlsTermIngressByHost["bridge-01"] = true
	errs, _ = checkService(svc(types.IngressSpec{Type: types.IngressTypeForward, Listen: 80, Target: 8080}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "ACME owns :80")
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
	// A service that declares no user has no account for the bootstrapper to drop
	// privilege to before exec.
	ctx := baseCtx()
	errs, _ := checkService(types.ServiceSpec{Host: "bridge", Type: "raw"}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "must declare the no-login user")
}

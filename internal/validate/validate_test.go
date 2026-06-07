package validate

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/regions"
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

func TestCheckServiceIngress(t *testing.T) {
	ingress := &types.IngressSpec{Hostname: "api", Port: 8080}

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

package validate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// baseCtx returns a regionContext with one vm host (bridge-01) and hetzner
// available, for exercising the per-spec semantic checks directly.
func baseCtx() regionContext {
	return regionContext{
		available:        map[string]bool{"hetzner": true},
		computeKind:      map[string]string{"bridge-01": "vm"},
		computeCanonical: map[string]string{"bridge-01": "bridge-01"},
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
	assert.Contains(t, errs[0], "not defined in variables.yaml providers")
}

func TestCheckServiceIngress(t *testing.T) {
	ingress := &types.IngressSpec{Hostname: "api", Port: 8080}

	// Ingress with a terminator on the same host -> OK.
	ctx := baseCtx()
	ctx.tlsByCompute["bridge-01"] = true
	errs, _ := checkService(types.ServiceSpec{Provider: "hetzner", Host: "bridge-01", Type: "raw", Ingress: ingress}, ctx)
	assert.Empty(t, errs)

	// Ingress but no terminator targets the host -> FAIL.
	ctx = baseCtx()
	errs, _ = checkService(types.ServiceSpec{Provider: "hetzner", Host: "bridge-01", Type: "raw", Ingress: ingress}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "no tls-termination resource")

	// No ingress -> the terminator requirement does not apply.
	ctx = baseCtx()
	errs, _ = checkService(types.ServiceSpec{Provider: "hetzner", Host: "bridge-01", Type: "raw"}, ctx)
	assert.Empty(t, errs)
}

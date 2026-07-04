package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wardnet/inforge/internal/types"
)

// renewScopeHasWork gates both the whole-run no-op fast path and each scope's
// credential requirement (ADR-0033). In per-service mode only the named
// mtls_files: service counts — a plain mesh member's copy lives with the mesh
// proxy, so releasing it must not demand INFORGE_SECRETS_KEY; in full mode any
// pki: member does.
func TestRenewScopeHasWork(t *testing.T) {
	res := types.Resources{Service: []types.ServiceSpec{
		{Name: "api"},                 // no pki
		{Name: "bridge", Pki: "mesh"}, // plain mesh member
		{Name: "tunneller", Pki: "mesh", MtlsFiles: true},      // raw-plane opt-in
		{Name: "bridge-staging", Pki: "mesh", MtlsFiles: true}, // name must match exactly
	}}
	// Per-service mode: only an mtls_files match counts.
	assert.False(t, renewScopeHasWork(res, "api"))
	assert.False(t, renewScopeHasWork(res, "bridge"))
	assert.True(t, renewScopeHasWork(res, "tunneller"))
	assert.False(t, renewScopeHasWork(res, "missing"))
	// Full mode: any pki: member means per-host mesh aggregates to write.
	assert.True(t, renewScopeHasWork(res, ""))
	assert.False(t, renewScopeHasWork(types.Resources{Service: []types.ServiceSpec{{Name: "api"}}}, ""))
	// A gateway is a mesh member too (client leaf in its host's aggregate) —
	// but only in full mode; releases never target the gateway.
	gwOnly := types.Resources{Gateway: []types.GatewaySpec{{Name: "api", Pki: "mesh"}}}
	assert.True(t, renewScopeHasWork(gwOnly, ""))
	assert.False(t, renewScopeHasWork(gwOnly, "api"))
}

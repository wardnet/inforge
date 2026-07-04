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
	svcs := []types.ServiceSpec{
		{Name: "api"},                 // no pki
		{Name: "bridge", Pki: "mesh"}, // plain mesh member
		{Name: "tunneller", Pki: "mesh", MtlsFiles: true},      // raw-plane opt-in
		{Name: "bridge-staging", Pki: "mesh", MtlsFiles: true}, // name must match exactly
	}
	// Per-service mode: only an mtls_files match counts.
	assert.False(t, renewScopeHasWork(svcs, "api"))
	assert.False(t, renewScopeHasWork(svcs, "bridge"))
	assert.True(t, renewScopeHasWork(svcs, "tunneller"))
	assert.False(t, renewScopeHasWork(svcs, "missing"))
	// Full mode: any pki: member means per-host mesh aggregates to write.
	assert.True(t, renewScopeHasWork(svcs, ""))
	assert.False(t, renewScopeHasWork([]types.ServiceSpec{{Name: "api"}}, ""))
}

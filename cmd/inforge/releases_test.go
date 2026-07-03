package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wardnet/inforge/internal/types"
)

// Only an mtls_files: opted-in mesh member has a service-side leaf to mint at
// release time — a plain mesh member's copy lives with the mesh proxy
// (ADR-0033), so mintReleasedServiceLeaf returns before touching a provider.
func TestAnyServiceNeedsMtlsFiles(t *testing.T) {
	svcs := []types.ServiceSpec{
		{Name: "api"},                 // no pki
		{Name: "bridge", Pki: "mesh"}, // plain mesh member
		{Name: "tunneller", Pki: "mesh", MtlsFiles: true},      // raw-plane opt-in
		{Name: "bridge-staging", Pki: "mesh", MtlsFiles: true}, // name must match exactly
	}
	assert.False(t, anyServiceNeedsMtlsFiles(svcs, "api"))
	assert.False(t, anyServiceNeedsMtlsFiles(svcs, "bridge"))
	assert.True(t, anyServiceNeedsMtlsFiles(svcs, "tunneller"))
	assert.False(t, anyServiceNeedsMtlsFiles(svcs, "missing"))
}

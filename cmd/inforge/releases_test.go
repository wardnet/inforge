package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wardnet/inforge/internal/types"
)

func TestFilterServicesByName(t *testing.T) {
	svcs := []types.ServiceSpec{
		{Name: "bridge", Pki: "mesh"},
		{Name: "api"},
		{Name: "bridge-staging"},
	}
	got := filterServicesByName(svcs, "bridge")
	assert.Len(t, got, 1)
	assert.Equal(t, "bridge", got[0].Name)

	assert.Empty(t, filterServicesByName(svcs, "missing"))
}

// A released service that joins no mesh has no leaf to mint — the filtered set
// reports no pki, so mintReleasedServiceLeaf returns before touching a provider.
func TestFilteredNonMeshHasNoPki(t *testing.T) {
	svcs := []types.ServiceSpec{{Name: "api"}}
	assert.False(t, anyServiceHasPki(filterServicesByName(svcs, "api")))

	mesh := []types.ServiceSpec{{Name: "bridge", Pki: "mesh"}}
	assert.True(t, anyServiceHasPki(filterServicesByName(mesh, "bridge")))
}

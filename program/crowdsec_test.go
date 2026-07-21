package program

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/types"
)

func edgeFixture() types.Resources {
	return types.Resources{
		Compute: []types.ComputeSpec{
			{Name: "edge", Kind: "vm", InstanceCount: 1},
			{Name: "gwhost", Kind: "vm", InstanceCount: 1},
			{Name: "worker", Kind: "vm", InstanceCount: 1},
		},
		Ingress: []types.IngressSpec{{Name: "main", Host: "edge"}},
		Gateway: []types.GatewaySpec{{Name: "gw", Host: "gwhost"}},
	}
}

func TestCrowdsecEdgeHostsIncludesIngressAndGatewayOnly(t *testing.T) {
	res := edgeFixture()
	canonical := naming.CanonicalComputeKeys(res.Compute)
	edge := crowdsecEdgeHosts(res, canonical)
	assert.True(t, edge[canonical["edge"]], "ingress host is an edge")
	assert.True(t, edge[canonical["gwhost"]], "gateway host is an edge")
	assert.False(t, edge[canonical["worker"]], "a plain worker gets no CrowdSec")
	assert.Len(t, edge, 2)
}

func TestCrowdsecEdgeHostsHonorsOptOut(t *testing.T) {
	res := edgeFixture()
	res.Ingress[0].Security = ptrBool(false) // this ingress opts out
	canonical := naming.CanonicalComputeKeys(res.Compute)
	edge := crowdsecEdgeHosts(res, canonical)
	assert.False(t, edge[canonical["edge"]], "opted-out ingress host is excluded")
	assert.True(t, edge[canonical["gwhost"]], "the gateway host is a separate edge and stays in")
}

func TestCrowdsecEdgeHostsExplicitTrueStillIncluded(t *testing.T) {
	res := edgeFixture()
	res.Gateway[0].Security = ptrBool(true) // explicit opt-in == absent
	canonical := naming.CanonicalComputeKeys(res.Compute)
	edge := crowdsecEdgeHosts(res, canonical)
	assert.True(t, edge[canonical["gwhost"]])
}

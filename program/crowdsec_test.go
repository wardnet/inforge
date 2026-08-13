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
		// The gateway is fronted by the ingress (ADR-0045). Its own host (gwhost) is
		// PRIVATE and is never a CrowdSec edge.
		Gateway: []types.GatewaySpec{{Name: "gw", Host: "gwhost", Ingress: "main"}},
	}
}

// The public edge is the INGRESS tier only (ADR-0045): a gateway is never a public
// edge, so its host contributes no CrowdSec host.
func TestCrowdsecEdgeHostsIsIngressOnly(t *testing.T) {
	res := edgeFixture()
	canonical := naming.CanonicalComputeKeys(res.Compute)
	edge := crowdsecEdgeHosts(res, canonical)
	assert.True(t, edge[canonical["edge"]], "ingress host is an edge")
	assert.False(t, edge[canonical["gwhost"]], "the gateway host is private, never a CrowdSec edge")
	assert.False(t, edge[canonical["worker"]], "a plain worker gets no CrowdSec")
	assert.Len(t, edge, 1)
}

func TestCrowdsecEdgeHostsHonorsIngressOptOut(t *testing.T) {
	res := edgeFixture()
	res.Ingress[0].Security = ptrBool(false) // this ingress opts out
	canonical := naming.CanonicalComputeKeys(res.Compute)
	edge := crowdsecEdgeHosts(res, canonical)
	assert.False(t, edge[canonical["edge"]], "opted-out ingress host is excluded")
	assert.Empty(t, edge, "no other public edge remains — the gateway host is never one")
}

// The gateway's `security:` field no longer affects CrowdSec host selection: a gateway
// is never a public edge, whatever the flag says.
func TestCrowdsecEdgeHostsIgnoresGatewaySecurityFlag(t *testing.T) {
	res := edgeFixture()
	res.Gateway[0].Security = ptrBool(true)
	canonical := naming.CanonicalComputeKeys(res.Compute)
	edge := crowdsecEdgeHosts(res, canonical)
	assert.False(t, edge[canonical["gwhost"]], "the gateway host stays out regardless of its security flag")
}

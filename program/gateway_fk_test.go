package program

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/types"
)

// gatewayFKFixture is a minimal, VALID scope: one host running both the ingress and
// the gateway it fronts (the co-located shape prd uses).
func gatewayFKFixture() types.Resources {
	return types.Resources{
		Compute: []types.ComputeSpec{{Name: "edge", Kind: "vm", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "main", Host: "edge"}},
		Gateway: []types.GatewaySpec{{Name: "api", Host: "edge", Ingress: "main"}},
	}
}

func TestCheckGatewayFKsAcceptsAResolvedGateway(t *testing.T) {
	assert.NoError(t, checkGatewayFKs(gatewayFKFixture()))
}

func TestCheckGatewayFKsAcceptsAScopeWithNoGateway(t *testing.T) {
	res := gatewayFKFixture()
	res.Gateway = nil
	assert.NoError(t, checkGatewayFKs(res))
}

// The production regression. ADR-0045 made `ingress:` required; the deployed manifests
// did not have it. Every gateway derivation skips what it cannot resolve, so the deploy
// rendered nginx with NO gateway server and reported success — a total north-south
// outage applied by a green run. A gateway that resolves to nothing must stop the deploy.
func TestCheckGatewayFKsRejectsAMissingIngressFK(t *testing.T) {
	res := gatewayFKFixture()
	res.Gateway[0].Ingress = ""

	err := checkGatewayFKs(res)

	require.Error(t, err, "a gateway with no ingress must fail the deploy, not vanish from it")
	assert.Contains(t, err.Error(), "api", "the error names the gateway")
	assert.Contains(t, err.Error(), "ingress", "the error names the missing FK")
}

func TestCheckGatewayFKsRejectsAnUnresolvableIngressFK(t *testing.T) {
	res := gatewayFKFixture()
	res.Gateway[0].Ingress = "ghost"

	err := checkGatewayFKs(res)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ghost")
}

func TestCheckGatewayFKsRejectsAnUnresolvableHostFK(t *testing.T) {
	res := gatewayFKFixture()
	res.Gateway[0].Host = "ghost"

	err := checkGatewayFKs(res)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ghost")
}

// The guard must agree with the derivation it protects: anything checkGatewayFKs
// accepts, gatewayEdgeByHost must actually render — otherwise the skip is still live.
func TestAcceptedGatewayIsActuallyRendered(t *testing.T) {
	res := gatewayFKFixture()
	require.NoError(t, checkGatewayFKs(res))

	canonical := naming.CanonicalComputeKeys(res.Compute)
	terms, routings, _ := gatewayEdgeByHost(res, canonical, "use1", "example.com", "")

	assert.Len(t, terms[canonical["edge"]], 1, "the fronting ingress host gets a termination server")
	assert.Len(t, routings[canonical["edge"]], 1, "the gateway host gets a routing server")
}

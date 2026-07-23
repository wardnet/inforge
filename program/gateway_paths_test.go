package program

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/nginx"
	"github.com/wardnet/inforge/internal/types"
)

// gatewayPathsRes is the shared fixture for the ADR-0034/0045 derivations: a
// gateway on host `edge`, fronted by the scope ingress `web` on host `back` (a
// SPLIT gateway — the gateway and its ingress are on different hosts), listing two
// services — tenants (on back, with health) and billing (on edge, no health) —
// plus an ingress-fronted service that must stay out of the gateway health tier (D12).
func gatewayPathsRes() types.Resources {
	return types.Resources{
		Compute: []types.ComputeSpec{
			{Name: "edge", Container: "edge", InstanceCount: 1},
			{Name: "back", Container: "back", InstanceCount: 1},
		},
		Ingress: []types.IngressSpec{{Name: "web", Host: "back"}},
		Gateway: []types.GatewaySpec{{
			Name: "api", Container: "edge", Host: "edge", Ingress: "web", Pki: "mesh", Subdomain: "api",
			Services:         []string{"tenants", "billing"},
			HealthProbePaths: []string{"/healthz"},
		}},
		Service: []types.ServiceSpec{
			{Name: "tenants", Container: "back", Host: "back", Pki: "mesh",
				HealthProbesPort: 8081, HealthProbePaths: []string{"/livez", "/readyz"},
				Mesh: &types.MeshSpec{Port: 8080, AllowedServices: []string{"gateway"},
					PublicPaths:   []string{"/v*/account/**", "/status"},
					InternalPaths: []string{"/internal/**"}}},
			{Name: "billing", Container: "edge", Host: "edge", Pki: "mesh",
				Mesh: &types.MeshSpec{Port: 9090, AllowedServices: []string{"gateway"},
					PublicPaths: []string{"/billing/**"}}},
			// Ingress-fronted (D12: the ingress wins) — even if a gateway listed it,
			// its health lives at the ingress host.
			{Name: "webapp", Container: "back", Host: "back", Ingress: "web",
				HealthProbesPort: 3001, HealthProbePaths: []string{"/healthz"}},
		},
	}
}

// TestToGatewayNginxRoutes: the routing table is derived — one route per (listed
// service, public glob); internal paths never reach the gateway.
func TestToGatewayNginxRoutes(t *testing.T) {
	res := gatewayPathsRes()
	routes := toGatewayNginxRoutes(res.Gateway[0].Services, res.Service)
	assert.ElementsMatch(t, []types.IngressGatewayRoute{
		{Pattern: "/v*/account/**", Service: "tenants"},
		{Pattern: "/status", Service: "tenants"},
		{Pattern: "/billing/**", Service: "billing"},
	}, routes)
}

// TestGatewayEdgeByHostSplitsTerminationAndRouting: a private gateway (ADR-0045)
// derives a TERMINATION server on its fronting ingress host (back-01) and a ROUTING
// server on its own host (edge-01), carrying the derived routes + health paths. Being
// split, each half records the peer host whose private IP the provider resolves.
func TestGatewayEdgeByHostSplitsTerminationAndRouting(t *testing.T) {
	res := gatewayPathsRes()
	canonical := map[string]string{"edge": "edge-01", "back": "back-01"}
	terms, routings, peers := gatewayEdgeByHost(res, canonical, "use1", "wardnet.network", "")
	// Termination on the ingress host; routing on the gateway host.
	require.Len(t, terms["back-01"], 1)
	require.Len(t, routings["edge-01"], 1)
	assert.Empty(t, terms["edge-01"])
	assert.Empty(t, routings["back-01"])
	route := routings["edge-01"][0]
	assert.Equal(t, []string{"/healthz"}, route.HealthProbePaths)
	assert.Len(t, route.Routes, 3)
	assert.Empty(t, route.ListenAddr, "split routing server binds the gateway host's own private IP, filled by the provider")
	assert.Empty(t, route.RealIPFrom, "split routing real_ip source resolves from the ingress host's private IP")
	assert.Empty(t, terms["back-01"][0].Backend, "split termination backend resolves from the gateway host's private IP")
	// Peer-IP needs: the termination (on back-01) needs the gateway host (edge-01);
	// the routing (on edge-01) needs the ingress host (back-01).
	assert.Equal(t, "edge-01", peers["back-01"]["api"])
	assert.Equal(t, "back-01", peers["edge-01"]["api"])
}

// TestGatewayEdgeByHostCoLocated: when the gateway shares its ingress's host, both
// halves render on one host with loopback addresses and no peer-IP resolution.
func TestGatewayEdgeByHostCoLocated(t *testing.T) {
	res := gatewayPathsRes()
	res.Ingress = []types.IngressSpec{{Name: "web", Host: "edge"}} // move the ingress onto the gateway host
	canonical := map[string]string{"edge": "edge-01", "back": "back-01"}
	terms, routings, peers := gatewayEdgeByHost(res, canonical, "use1", "wardnet.network", "")
	require.Len(t, terms["edge-01"], 1)
	require.Len(t, routings["edge-01"], 1)
	assert.Equal(t, "127.0.0.1", terms["edge-01"][0].Backend)
	assert.Equal(t, "127.0.0.1", routings["edge-01"][0].ListenAddr)
	assert.Equal(t, "127.0.0.1", routings["edge-01"][0].RealIPFrom)
	assert.Empty(t, peers, "co-located gateways need no cross-host IP resolution")
}

// TestResolveGatewayHealthServices: only gateway-listed, health-declaring,
// ingress-less services join the gateway health tier (D12). The listener renders on
// the gateway's FRONTING INGRESS host (ADR-0045), and co-location is relative to it.
func TestResolveGatewayHealthServices(t *testing.T) {
	res := gatewayPathsRes()
	canonical := map[string]string{"edge": "edge-01", "back": "back-01"}
	gsvcs := resolveGatewayHealthServices(res, canonical)
	require.Len(t, gsvcs, 1, "billing has no health port; webapp has an ingress (D12)")
	gs := gsvcs[0]
	assert.Equal(t, "tenants", gs.svc.Name)
	assert.Equal(t, "back-01", gs.gwHost, "health renders on the fronting ingress host, not the gateway host")
	assert.Equal(t, "back-01", gs.svcHost)
	assert.True(t, gs.coLocated, "tenants shares the ingress host (back), so its health backend is loopback")
}

// TestGatewayHealthByHost: the health entries render on the gateway's FRONTING
// INGRESS host (ADR-0045) with the declared probe paths. tenants shares that host
// (back), so its backend is loopback.
func TestGatewayHealthByHost(t *testing.T) {
	res := gatewayPathsRes()
	canonical := map[string]string{"edge": "edge-01", "back": "back-01"}
	byHost := gatewayHealthByHost(resolveGatewayHealthServices(res, canonical), "prd", "use1", "wardnet.network")
	require.Len(t, byHost["back-01"], 1)
	h := byHost["back-01"][0]
	assert.Equal(t, "tenants.svc.prd.use1.wardnet.network", h.FQDN)
	assert.Equal(t, 8081, h.Target)
	assert.Equal(t, []string{"/livez", "/readyz"}, h.Paths)
	assert.Equal(t, "127.0.0.1", h.Backend, "tenants is co-located with the ingress host, so its health backend is loopback")
}

// TestFirewallPlanByHostGatewayHealth: a private gateway (ADR-0045) opens its public
// pair (443/80) and its health port on the FRONTING INGRESS host (back-01), never on
// the gateway host; the split gateway host (edge-01) opens only its private routing
// port (GatewayHTTPPort) to the network CIDR.
func TestFirewallPlanByHostGatewayHealth(t *testing.T) {
	res := gatewayPathsRes()
	got := firewallPlanByHost(res, false)
	assert.Contains(t, got["back-01"].Public, 443, "gateway TLS termination is public on the ingress host")
	assert.Contains(t, got["back-01"].Public, 80, "gateway ACME is public on the ingress host")
	assert.Contains(t, got["back-01"].Public, 81, "gateway health listener is public on the ingress host")
	assert.NotContains(t, got["edge-01"].Public, 443, "the gateway host is never public")
	assert.NotContains(t, got["edge-01"].Public, 80)
	assert.Contains(t, got["edge-01"].Private, nginx.GatewayHTTPPort, "the split gateway host opens its routing port privately")
}

// TestDerivedRecordsGatewayHealth: a gateway-routed service's ServiceFQDN A record
// points at the gateway's FRONTING INGRESS host (ADR-0045: health follows the gateway
// behind the ingress); an ingress-fronted health service's record points at its own
// ingress host (D12 keeps the two exclusive). Here both resolve to back-01.
func TestDerivedRecordsGatewayHealth(t *testing.T) {
	res := gatewayPathsRes()
	got := derivedRecords(res, "prd", "use1", "wardnet.network", "")
	byRecord := map[string]string{}
	for _, d := range got {
		byRecord[d.rec.RecordName] = d.hostKey
	}
	assert.Equal(t, "back-01", byRecord["tenants.svc.prd.use1"], "gateway-routed health record points at the fronting ingress host")
	assert.Equal(t, "back-01", byRecord["webapp.svc.prd.use1"], "ingress-fronted health-only service gets its record at the ingress host")
	_, hasBilling := byRecord["billing.svc.prd.use1"]
	assert.False(t, hasBilling, "no health port -> no service record")
}

// TestMeshLocalPathsThreaded: the callee plane carries public ∪ internal globs.
func TestMeshLocalPathsThreaded(t *testing.T) {
	res := gatewayPathsRes()
	canonical := map[string]string{"edge": "edge-01", "back": "back-01"}
	allowedFor := func(svc types.ServiceSpec) []string { return svc.Mesh.AllowedServices }
	mh := meshInputsByHost(res, canonical, "us-east-1", allowedFor)["back-01"]
	require.NotNil(t, mh)
	require.Len(t, mh.local, 1)
	assert.Equal(t, []string{"/v*/account/**", "/status", "/internal/**"}, mh.local[0].Paths)
}

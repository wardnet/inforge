package program

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/types"
)

// gatewayPathsRes is the shared fixture for the ADR-0034 derivations: a gateway
// on its own host (edge) listing two services — tenants (co-located with the
// gateway? no: on back, cross-host, with health) and billing (on edge,
// co-located, no health) — plus an ingress-fronted service that must stay out of
// the gateway health tier (D12).
func gatewayPathsRes() types.Resources {
	return types.Resources{
		Compute: []types.ComputeSpec{
			{Name: "edge", Container: "edge", InstanceCount: 1},
			{Name: "back", Container: "back", InstanceCount: 1},
		},
		Ingress: []types.IngressSpec{{Name: "web", Host: "back"}},
		Gateway: []types.GatewaySpec{{
			Name: "api", Container: "edge", Host: "edge", Pki: "mesh", Subdomain: "api",
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

// TestGatewaysByHostThreadsRoutesAndHealth: the derived IngressGateway carries
// the derived routes and the gateway's own health probe paths.
func TestGatewaysByHostThreadsRoutesAndHealth(t *testing.T) {
	res := gatewayPathsRes()
	canonical := map[string]string{"edge": "edge-01", "back": "back-01"}
	byHost := gatewaysByHost(res, canonical, "use1", "wardnet.network", "")
	require.Len(t, byHost["edge-01"], 1)
	g := byHost["edge-01"][0]
	assert.Equal(t, []string{"/healthz"}, g.HealthProbePaths)
	assert.Len(t, g.Routes, 3)
}

// TestResolveGatewayHealthServices: only gateway-listed, health-declaring,
// ingress-less services join the gateway health tier (D12), with co-location
// resolved against the gateway host.
func TestResolveGatewayHealthServices(t *testing.T) {
	res := gatewayPathsRes()
	canonical := map[string]string{"edge": "edge-01", "back": "back-01"}
	gsvcs := resolveGatewayHealthServices(res, canonical)
	require.Len(t, gsvcs, 1, "billing has no health port; webapp has an ingress (D12)")
	gs := gsvcs[0]
	assert.Equal(t, "tenants", gs.svc.Name)
	assert.Equal(t, "edge-01", gs.gwHost)
	assert.Equal(t, "back-01", gs.svcHost)
	assert.False(t, gs.coLocated)
}

// TestGatewayHealthByHost: the health entries render on the gateway's host with
// the declared probe paths; a cross-host backend is left empty for the provider
// to substitute the private IP.
func TestGatewayHealthByHost(t *testing.T) {
	res := gatewayPathsRes()
	canonical := map[string]string{"edge": "edge-01", "back": "back-01"}
	byHost := gatewayHealthByHost(resolveGatewayHealthServices(res, canonical), "prd", "use1", "wardnet.network")
	require.Len(t, byHost["edge-01"], 1)
	h := byHost["edge-01"][0]
	assert.Equal(t, "tenants.svc.prd.use1.wardnet.network", h.FQDN)
	assert.Equal(t, 8081, h.Target)
	assert.Equal(t, []string{"/livez", "/readyz"}, h.Paths)
	assert.Empty(t, h.Backend, "cross-host backend is the provider's to fill")
}

// TestFirewallPlanByHostGatewayHealth: the gateway host opens its public health
// port (default 81) alongside 443/80; the cross-host backend opens its health
// port privately to the network CIDR.
func TestFirewallPlanByHostGatewayHealth(t *testing.T) {
	res := gatewayPathsRes()
	got := firewallPlanByHost(res, false)
	assert.Contains(t, got["edge-01"].Public, 81, "gateway health listener is public on the gateway host")
	assert.Contains(t, got["edge-01"].Public, 443)
	assert.Contains(t, got["edge-01"].Public, 80)
	assert.Contains(t, got["back-01"].Private, 8081, "cross-host backend health port opens privately")
}

// TestDerivedRecordsGatewayHealth: a gateway-routed service's ServiceFQDN A
// record points at the GATEWAY host; an ingress-fronted health service's record
// points at the ingress host (D12 keeps the two exclusive).
func TestDerivedRecordsGatewayHealth(t *testing.T) {
	res := gatewayPathsRes()
	got := derivedRecords(res, "prd", "use1", "wardnet.network", "")
	byRecord := map[string]string{}
	for _, d := range got {
		byRecord[d.rec.RecordName] = d.hostKey
	}
	assert.Equal(t, "edge-01", byRecord["tenants.svc.prd.use1"], "gateway-routed health record points at the gateway host")
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

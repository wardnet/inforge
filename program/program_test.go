package program

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/agent"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/service"
	"github.com/wardnet/inforge/internal/types"
)

func TestDeployUsersByHost(t *testing.T) {
	computes := []types.ComputeSpec{
		{Name: "bridge", InstanceCount: 1, DeployUser: &types.DeployUserSpec{Name: "deploy"}},
		{Name: "worker", InstanceCount: 2}, // no deploy user
	}
	got := naming.DeployUsersByHost(computes)

	assert.Equal(t, "deploy", got["bridge-01"])
	assert.Equal(t, "", got["worker-01"])
	assert.Equal(t, "", got["worker-02"])
	// The bare name is not a host key — only expanded specKeys are.
	_, ok := got["bridge"]
	assert.False(t, ok)
}

func TestIngressRoutesByHostDerivesEnvScopedFQDN(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "web", Host: "bridge"}},
		Service: []types.ServiceSpec{
			// Two tls-termination services through the same ingress, declared out of
			// order, to exercise grouping + stable sorting. One service has no routes.
			{Name: "web", Host: "bridge-01", Ingress: "web", Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 8080, Target: 3000}}},
			{Name: "api", Host: "bridge", Ingress: "web", Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}}},
			{Name: "worker", Host: "bridge-01"},
		},
	}
	canonical := naming.CanonicalComputeKeys(res.Compute)

	got, crossHost, err := ingressRoutesByHost(res, canonical, "prd", "use1", "wardnet.network")
	require.NoError(t, err)
	assert.Empty(t, crossHost, "all routes are co-located, so no cross-host backends")

	// The ingress's host (bridge-01) is the realization key; both services land there.
	require.Len(t, got, 1)
	routes := got["bridge-01"]
	require.Len(t, routes, 2)

	// Sorted by listen port: api (443) before web (8080). FQDNs is the auto-derived
	// "<svc>.svc" name (one entry, since the route carries server_name as a list).
	assert.Equal(t, "api", routes[0].Service)
	assert.Equal(t, types.IngressTypeTLSTermination, routes[0].Type)
	assert.Equal(t, []string{"api.svc.prd.use1.wardnet.network"}, routes[0].FQDNs)
	assert.Equal(t, 443, routes[0].Listen)
	assert.Equal(t, 8080, routes[0].Target)
	assert.Equal(t, "127.0.0.1", routes[0].Backend, "co-located backend is loopback")

	assert.Equal(t, "web", routes[1].Service)
	assert.Equal(t, []string{"web.svc.prd.use1.wardnet.network"}, routes[1].FQDNs)
	assert.Equal(t, 8080, routes[1].Listen)
	assert.Equal(t, 3000, routes[1].Target)

	// The FQDN matches ServiceFQDN exactly — derivation lives in one place.
	assert.Equal(t, naming.ServiceFQDN("prd", "use1", "api", "wardnet.network"), routes[0].FQDNs[0])
}

// A cross-host route (service on a different host than its ingress) leaves Backend
// empty and records the service → backend host in crossHost, so the caller can
// supply the backend's private IP.
func TestIngressRoutesByHostCrossHost(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{
			{Name: "edge", InstanceCount: 1},
			{Name: "back", InstanceCount: 1},
		},
		Ingress: []types.IngressSpec{{Name: "web", Host: "edge"}},
		Service: []types.ServiceSpec{
			{Name: "api", Host: "back", Ingress: "web", Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}}},
		},
	}
	got, crossHost, err := ingressRoutesByHost(res, naming.CanonicalComputeKeys(res.Compute), "prd", "use1", "wardnet.network")
	require.NoError(t, err)

	// Realized on the ingress host (edge-01), proxying to the backend (back-01).
	routes := got["edge-01"]
	require.Len(t, routes, 1)
	assert.Equal(t, "api", routes[0].Service)
	assert.Equal(t, "", routes[0].Backend, "a cross-host route's backend is filled by the provider")
	assert.Equal(t, "back-01", crossHost["edge-01"]["api"], "the backend host is recorded for IP wiring")
}

// A single service carrying a tls-termination route (with vanity) plus a forward
// route is the bridge shape: the tls-termination route carries every SNI (auto +
// vanity, sorted) as one server_name list; the forward route carries no SNI and a
// target. Routes sort by listen port.
func TestIngressRoutesByHostTerminatePlusForward(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "web", Host: "bridge"}},
		Service: []types.ServiceSpec{
			{Name: "bridge", Host: "bridge-01", Ingress: "web", Routes: []types.RouteSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Vanity: []string{
					"key-broker.{BASE_DOMAIN}",
					"key-broker.inforge.wardnet.network",
				}},
				{Type: types.IngressTypeForward, Listen: 853, Target: 5353},
			}},
		},
	}
	got, _, err := ingressRoutesByHost(res, naming.CanonicalComputeKeys(res.Compute), "prd", "use1", "wardnet.network")
	require.NoError(t, err)
	routes := got["bridge-01"]
	require.Len(t, routes, 2) // 1 tls-termination (multi-SNI) + 1 forward

	term := routes[0]
	assert.Equal(t, types.IngressTypeTLSTermination, term.Type)
	assert.Equal(t, 443, term.Listen)
	assert.Equal(t, 8080, term.Target)
	assert.Equal(t, []string{
		"bridge.svc.prd.use1.wardnet.network",
		"key-broker.inforge.wardnet.network",
		"key-broker.wardnet.network",
	}, term.FQDNs, "auto + vanity FQDNs, sorted")

	fwd := routes[1]
	assert.Equal(t, types.IngressTypeForward, fwd.Type)
	assert.Equal(t, 853, fwd.Listen)
	assert.Equal(t, 5353, fwd.Target)
	assert.Empty(t, fwd.FQDNs, "a forward route carries no SNI")
}

// Two services may share one listen port through one ingress when both are
// tls-termination (nginx demuxes by SNI); they render two routes on the same port.
func TestIngressRoutesByHostSharedTLSListen(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "web", Host: "bridge"}},
		Service: []types.ServiceSpec{
			{Name: "a", Host: "bridge-01", Ingress: "web", Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 3000}}},
			{Name: "b", Host: "bridge-01", Ingress: "web", Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 4000}}},
		},
	}
	got, _, err := ingressRoutesByHost(res, naming.CanonicalComputeKeys(res.Compute), "prd", "use1", "wardnet.network")
	require.NoError(t, err)
	routes := got["bridge-01"]
	require.Len(t, routes, 2)
	assert.Equal(t, 443, routes[0].Listen)
	assert.Equal(t, 443, routes[1].Listen)
	assert.Equal(t, "a", routes[0].Service)
	assert.Equal(t, "b", routes[1].Service)
}

// TestDerivedRecordsBridge asserts the bridge yields exactly the host record plus
// the service's auto + vanity records, all pointing at the INGRESS host. The
// service's two routes (tls-termination + forward) both derive "<svc>.svc", which
// collapses to one record.
func TestDerivedRecordsBridge(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", Container: "bridge", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "web", Host: "bridge"}},
		Service: []types.ServiceSpec{
			{Name: "bridge", Container: "bridge", Host: "bridge-01", Ingress: "web", Routes: []types.RouteSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Vanity: []string{
					"key-broker.{BASE_DOMAIN}",
					"key-broker.inforge.wardnet.network",
				}},
				{Type: types.IngressTypeForward, Listen: 853, Target: 5353},
			}},
		},
	}

	got := derivedRecords(res, "prd", "use1", "wardnet.network", "")

	type rn struct{ name, record, host string }
	var flat []rn
	for _, d := range got {
		flat = append(flat, rn{d.rec.Name, d.rec.RecordName, d.hostKey})
		assert.Equal(t, "bridge", d.rec.Container)
	}
	assert.Equal(t, []rn{
		{"bridge-vm-prd-use1", "bridge.vm.prd.use1", "bridge-01"},
		{"bridge-svc-prd-use1", "bridge.svc.prd.use1", "bridge-01"},
		{"key-broker", "key-broker", "bridge-01"}, // key-broker.{BASE_DOMAIN} -> key-broker.wardnet.network
		{"key-broker-inforge", "key-broker.inforge", "bridge-01"},
	}, flat)
}

// TestDerivedRecordsCrossHostIngress: a service's ingress FQDNs resolve to the
// INGRESS host's public IP, not the backend's (ADR-0026). The host's own "<vm>"
// records still point at their own compute.
func TestDerivedRecordsCrossHostIngress(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{
			{Name: "edge", Container: "edge", InstanceCount: 1},
			{Name: "back", Container: "back", InstanceCount: 1},
		},
		Ingress: []types.IngressSpec{{Name: "web", Host: "edge"}},
		Service: []types.ServiceSpec{
			{Name: "api", Container: "back", Host: "back", Ingress: "web", Routes: []types.RouteSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080},
			}},
		},
	}
	got := derivedRecords(res, "prd", "use1", "wardnet.network", "")
	byRecord := map[string]string{}
	for _, d := range got {
		byRecord[d.rec.RecordName] = d.hostKey
	}
	assert.Equal(t, "edge-01", byRecord["edge.vm.prd.use1"])
	assert.Equal(t, "back-01", byRecord["back.vm.prd.use1"])
	// The service FQDN points at the ingress host (edge), not the backend (back).
	assert.Equal(t, "edge-01", byRecord["api.svc.prd.use1"], "service FQDN resolves to the ingress host")
}

// Two tls-termination services through one ingress sharing the same vanity FQDN on
// the same listen port collide on one SNI, which ingressRoutesByHost must reject.
func TestIngressRoutesByHostRejectsDuplicateSNI(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "web", Host: "bridge"}},
		Service: []types.ServiceSpec{
			{Name: "a", Host: "bridge-01", Ingress: "web", Routes: []types.RouteSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Vanity: []string{"dup.example.com"}}}},
			{Name: "b", Host: "bridge-01", Ingress: "web", Routes: []types.RouteSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 9090, Vanity: []string{"dup.example.com"}}}},
		},
	}
	_, _, err := ingressRoutesByHost(res, naming.CanonicalComputeKeys(res.Compute), "prd", "use1", "wardnet.network")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dup.example.com")
}

// The same SNI on different listen ports is legitimate (nginx demuxes per port).
func TestIngressRoutesByHostAllowsSameSNIDifferentListen(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "web", Host: "bridge"}},
		Service: []types.ServiceSpec{
			{Name: "a", Host: "bridge-01", Ingress: "web", Routes: []types.RouteSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Vanity: []string{"shared.example.com"}}}},
			{Name: "b", Host: "bridge-01", Ingress: "web", Routes: []types.RouteSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 8443, Target: 9090, Vanity: []string{"shared.example.com"}}}},
		},
	}
	_, _, err := ingressRoutesByHost(res, naming.CanonicalComputeKeys(res.Compute), "prd", "use1", "wardnet.network")
	require.NoError(t, err)
}

func TestIngressRoutesByHostNoRoutes(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Service: []types.ServiceSpec{{Name: "worker", Host: "bridge-01"}},
	}
	got, _, err := ingressRoutesByHost(res, naming.CanonicalComputeKeys(res.Compute), "prd", "use1", "wardnet.network")
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestFirewallPlanByHost locks the firewall derivation for the ingress tier: an
// ingress host opens, publicly, its services' route listen ports plus :80 iff a
// tls-termination route exists. A host with nothing contributes no entry.
func TestFirewallPlanByHost(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{
			{Name: "bridge", InstanceCount: 1},
			{Name: "plain", InstanceCount: 1}, // no ingress -> absent from the map
		},
		Ingress: []types.IngressSpec{{Name: "web", Host: "bridge"}},
		Service: []types.ServiceSpec{
			{Name: "api", Host: "bridge-01", Ingress: "web", Routes: []types.RouteSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080},
				{Type: types.IngressTypeForward, Listen: 853, Target: 5353},
				{Type: types.IngressTypeForward, Listen: 9000, Target: 9001},
			}},
			{Name: "web", Host: "bridge", Ingress: "web", Routes: []types.RouteSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 3000}, // dup 443
			}},
		},
	}

	got := firewallPlanByHost(res)
	// :80 added once (tls-termination present), 443 de-duplicated across services,
	// 853 + 9000 (the forward listen ports), sorted. All co-located -> no private.
	assert.Equal(t, []int{80, 443, 853, 9000}, got["bridge-01"].Public)
	assert.Empty(t, got["bridge-01"].Private)
	_, hasPlain := got["plain-01"]
	assert.False(t, hasPlain, "a host with no ingress and no backend ports contributes nothing")
}

// A forward-only ingress host (no tls-termination route) opens only its forward
// listen ports — no :80, since ACME never runs there.
func TestFirewallPlanByHostForwardOnlyNoPort80(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "web", Host: "bridge"}},
		Service: []types.ServiceSpec{
			{Name: "dns", Host: "bridge-01", Ingress: "web", Routes: []types.RouteSpec{
				{Type: types.IngressTypeForward, Listen: 853, Target: 5353},
			}},
		},
	}
	assert.Equal(t, []int{853}, firewallPlanByHost(res)["bridge-01"].Public)
}

// TestFirewallPlanByHostCrossHost: a cross-host backend opens its route target
// ports privately, scoped to its network CIDR; the ingress host opens the public
// listen ports.
func TestFirewallPlanByHostCrossHost(t *testing.T) {
	res := types.Resources{
		Network: []types.NetworkSpec{{Name: "net", CIDR: "10.0.0.0/16"}},
		Compute: []types.ComputeSpec{
			{Name: "edge", Network: "net", InstanceCount: 1},
			{Name: "back", Network: "net", InstanceCount: 1},
		},
		Ingress: []types.IngressSpec{{Name: "web", Host: "edge"}},
		Service: []types.ServiceSpec{
			{Name: "api", Host: "back", Ingress: "web", Routes: []types.RouteSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080},
			}},
		},
	}
	got := firewallPlanByHost(res)
	assert.Equal(t, []int{80, 443}, got["edge-01"].Public, "ingress host opens public listen + ACME")
	assert.Empty(t, got["edge-01"].Private)
	assert.Equal(t, []int{8080}, got["back-01"].Private, "backend opens its target port privately")
	assert.Equal(t, "10.0.0.0/16", got["back-01"].PrivateSourceCIDR)
	assert.Empty(t, got["back-01"].Public, "backend opens no public ingress port")
}

// TestFirewallPlanByHostExposedPorts: a private-only service (no ingress, no routes)
// contributes its exposed_ports as proto-aware private binds scoped to the host's
// network CIDR — never public (ADR-0029).
func TestFirewallPlanByHostExposedPorts(t *testing.T) {
	res := types.Resources{
		Network: []types.NetworkSpec{{Name: "net", CIDR: "10.0.0.0/16"}},
		Compute: []types.ComputeSpec{{Name: "edge", Network: "net", InstanceCount: 1}},
		Service: []types.ServiceSpec{
			{Name: "tunneller", Host: "edge", ExposedPorts: []types.ExposedPort{
				{Proto: "udp", Port: 51820},
				{Proto: "tcp", Port: 9444},
			}},
		},
	}
	got := firewallPlanByHost(res)
	assert.Equal(t, []types.ExposedPort{{Proto: "tcp", Port: 9444}, {Proto: "udp", Port: 51820}}, got["edge-01"].PrivateExposed,
		"exposed ports are sorted by proto then port")
	assert.Equal(t, "10.0.0.0/16", got["edge-01"].PrivateSourceCIDR, "exposed ports scope to the host's network CIDR")
	assert.Empty(t, got["edge-01"].Public, "a private-only service opens no public port")
	assert.Empty(t, got["edge-01"].Private, "exposed ports are carried in PrivateExposed, not Private")
}

// TestDerivedRecordsApps: an app's FQDN is a grey-cloud A record at its ingress
// host (the clean dotted form, regional carries the slug). The host's own "<vm>"
// record is unaffected.
func TestDerivedRecordsApps(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "edge", Container: "frontend", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "web", Host: "edge"}},
		App: []types.AppSpec{
			{Name: "dashboard", Container: "frontend", Ingress: "web", Subdomain: "my", Spa: true},
		},
	}
	got := derivedRecords(res, "prd", "use1", "wardnet.network", "")
	byRecord := map[string]struct{ host, container string }{}
	for _, d := range got {
		byRecord[d.rec.RecordName] = struct{ host, container string }{d.hostKey, d.rec.Container}
		assert.False(t, d.rec.Proxied, "app records are grey-cloud so LE HTTP-01 reaches the origin")
	}
	assert.Equal(t, "edge-01", byRecord["edge.vm.prd.use1"].host)
	// AppFQDN regional form: my.use1.wardnet.network -> zone-relative "my.use1".
	assert.Equal(t, "edge-01", byRecord["my.use1"].host, "app FQDN resolves to its ingress host")
	assert.Equal(t, "frontend", byRecord["my.use1"].container)
}

// TestDerivedRecordsAppsGlobalForm: with an empty slug (global scope) an app's
// record is the flat "<subdomain>" zone-relative form (no env, no slug segment).
func TestDerivedRecordsAppsGlobalForm(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "edge", Container: "frontend", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "web", Host: "edge"}},
		App: []types.AppSpec{
			{Name: "marketing", Container: "frontend", Ingress: "web", Subdomain: "www"},
		},
	}
	got := derivedRecords(res, "prd", "", "wardnet.network", "")
	byRecord := map[string]string{}
	for _, d := range got {
		byRecord[d.rec.RecordName] = d.hostKey
	}
	// AppFQDN global form: www.wardnet.network -> zone-relative "www".
	assert.Equal(t, "edge-01", byRecord["www"], "global app FQDN is the flat subdomain form")
}

// TestFirewallPlanByHostApps: an app-only ingress host opens public 80 + 443 and
// no private ports (apps have no backend).
func TestFirewallPlanByHostApps(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "edge", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "web", Host: "edge"}},
		App: []types.AppSpec{
			{Name: "dashboard", Ingress: "web", Subdomain: "my", Spa: true},
		},
	}
	got := firewallPlanByHost(res)
	assert.Equal(t, []int{80, 443}, got["edge-01"].Public, "app ingress host opens HTTPS + ACME")
	assert.Empty(t, got["edge-01"].Private, "apps have no backend, so no private ports")
}

// TestIngressAppsByHostResolvesFQDNAndRoot: apps group under their ingress host
// with the resolved FQDN, document root (the current symlink), and SPA flag.
func TestIngressAppsByHostResolvesFQDNAndRoot(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "edge", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "web", Host: "edge"}},
		App: []types.AppSpec{
			{Name: "dashboard", Ingress: "web", Subdomain: "my", Spa: true},
		},
	}
	got := ingressAppsByHost(res, naming.CanonicalComputeKeys(res.Compute), "use1", "wardnet.network", "")
	apps := got["edge-01"]
	require.Len(t, apps, 1)
	assert.Equal(t, "dashboard", apps[0].Name)
	assert.Equal(t, "my.use1.wardnet.network", apps[0].FQDN)
	assert.Equal(t, "/srv/wardnet/app/dashboard/current", apps[0].Root)
	assert.True(t, apps[0].Spa)
}

// TestRealizeIngressRejectsAppRouteSNICollision: an app FQDN equal to a :443
// tls-termination route's vanity SNI on the same ingress host is rejected — nginx
// could not demux two server blocks sharing one (listen 443, server_name).
func TestRealizeIngressRejectsAppRouteSNICollision(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "edge", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "web", Host: "edge"}},
		Service: []types.ServiceSpec{
			{Name: "api", Host: "edge-01", Ingress: "web", Routes: []types.RouteSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Vanity: []string{"my.use1.wardnet.network"}},
			}},
		},
		App: []types.AppSpec{
			{Name: "dashboard", Ingress: "web", Subdomain: "my", Spa: true}, // -> my.use1.wardnet.network
		},
	}
	canonical := naming.CanonicalComputeKeys(res.Compute)
	routesByHost, _, err := ingressRoutesByHost(res, canonical, "prd", "use1", "wardnet.network")
	require.NoError(t, err)
	appsByHost := ingressAppsByHost(res, canonical, "use1", "wardnet.network", "")

	err = checkAppSNICollisions(routesByHost, appsByHost)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "my.use1.wardnet.network")
	assert.Contains(t, err.Error(), "service api")
}

// TestCheckAppSNICollisionsAllows: the same FQDN on a non-:443 route does not
// collide (nginx demuxes per listen port), and a distinct app FQDN is fine.
func TestCheckAppSNICollisionsAllows(t *testing.T) {
	routesByHost := map[string][]types.IngressRoute{
		"edge-01": {
			// Same name as the app, but on a different listen port -> no collision.
			{Service: "api", Type: types.IngressTypeTLSTermination, Listen: 8443, FQDNs: []string{"my.use1.wardnet.network"}},
		},
	}
	appsByHost := map[string][]types.IngressApp{
		"edge-01": {{Name: "dashboard", FQDN: "my.use1.wardnet.network"}},
	}
	require.NoError(t, checkAppSNICollisions(routesByHost, appsByHost))
}

// TestAppProvisionScript: the placeholder is written and `current` is symlinked
// only when nothing already occupies it — so a re-run never clobbers a release.
func TestAppProvisionScript(t *testing.T) {
	script := appProvisionScript("dashboard")
	// The placeholder index is written (its bytes are base64-encoded into the script).
	assert.Contains(t, script, "/srv/wardnet/app/dashboard/placeholder/index.html")
	assert.Contains(t, script, "base64 -d | sudo tee")
	// The symlink is guarded: created only when no file and no symlink exist.
	assert.Contains(t, script, "if [ ! -e '/srv/wardnet/app/dashboard/current' ] && [ ! -L '/srv/wardnet/app/dashboard/current' ]; then")
	assert.Contains(t, script, "ln -s 'placeholder' '/srv/wardnet/app/dashboard/current'")
	assert.NotContains(t, script, "ln -sf", "must not force-overwrite an existing current symlink")
}

// TestServiceProvisionScriptEnablesNeverStarts guards the headline constraint:
// provisioning writes + enables the unit but must NEVER start/restart it —
// ExecStart=<folder>/run doesn't exist until release delivers code, so a start
// here would fail the whole deploy.
func TestServiceProvisionScriptEnablesNeverStarts(t *testing.T) {
	script := serviceProvisionScript(types.ServiceSpec{Name: "api", User: "svc"}, "1.2.3")

	assert.Contains(t, script, "systemctl daemon-reload")
	assert.Contains(t, script, "systemctl enable 'wardnet-api.service'")
	assert.NotContains(t, script, "systemctl start", "provisioning must not start the unit")
	assert.NotContains(t, script, "systemctl restart", "provisioning must not restart the unit")
	assert.NotContains(t, script, "enable --now", "enable must not start the unit")

	// Writes the unit file and creates the service folder + user.
	assert.Contains(t, script, "/etc/systemd/system/wardnet-api.service")
	assert.Contains(t, script, "/srv/wardnet/api")
	assert.Contains(t, script, "useradd --system --shell /usr/sbin/nologin 'svc'")
}

func TestServiceProvisionScriptNoUser(t *testing.T) {
	script := serviceProvisionScript(types.ServiceSpec{Name: "api"}, "1.2.3")
	assert.NotContains(t, script, "useradd", "no user declared -> no useradd")
}

// TestServiceProvisionScriptDownloadsAgent guards that provisioning
// downloads the inforge-agent binary, pinned to the inforge version, with
// host-side arch detection and the version quoted (injection-safe).
func TestServiceProvisionScriptDownloadsAgent(t *testing.T) {
	script := serviceProvisionScript(types.ServiceSpec{Name: "api", User: "svc"}, "1.2.3")

	// Version is single-quoted into a shell var, then composed with ${arch}.
	assert.Contains(t, script, "ver='1.2.3'")
	assert.Contains(t, script, "arch=$(uname -m)")
	assert.Contains(t, script, "x86_64) arch=amd64")
	assert.Contains(t, script, "aarch64) arch=arm64")
	assert.Contains(t, script, "asset=\"inforge-agent_${ver}_linux_${arch}\"")
	assert.Contains(t, script, "releases/download/v${ver}")
	assert.Contains(t, script, "curl -fsSL")
	// The download must be checksum-verified before it is installed as root.
	assert.Contains(t, script, "checksums.txt")
	assert.Contains(t, script, "sha256sum")
	assert.Contains(t, script, "checksum mismatch")
	assert.Contains(t, script, "trap 'rm -f \"$tmp\" \"$sums\"' EXIT", "temp files cleaned on any exit")
	assert.Contains(t, script, "install -m 0755 \"$tmp\" '/usr/local/bin/inforge-agent'")
	// The raw inforge version must never be interpolated unquoted into the shell.
	assert.NotContains(t, script, "inforge-agent_1.2.3_linux")
}

// TestRenderDescriptorRoundTrips proves the descriptor inforge writes parses
// back through the agent's own validator — the producer (this program)
// and consumer (inforge-agent) cannot drift because both use the same
// Descriptor struct and SupportedVersion constant.
func TestRenderDescriptorRoundTrips(t *testing.T) {
	svc := types.ServiceSpec{Name: "ghost", Container: "ghost", User: "ghost"}
	bundle := &types.ServiceSecretsBundle{
		ProviderKind: "infisical",
		URL:          "https://app.infisical.com",
		Environment:  "prod",
		SecretPath:   "/ghost",
		Env:          map[string]string{"DATABASE_URL": "infra/DATABASE_URL"},
	}

	// Provider-supplied cloud/host identity flows off the host's outputs (ADR-0030).
	host := types.ComputeOutputs{
		CloudProvider:    "hetzner",
		CloudRegion:      "us-east",
		AvailabilityZone: "ash",
		MachineType:      "cx23",
	}
	out, err := renderDescriptor(svc, host, bundle, "ws-123", "prd", "us-east-1", "use1", "wardnet.network", "ghost-01")
	require.NoError(t, err)

	d, err := agent.ParseDescriptor([]byte(out))
	require.NoError(t, err)
	assert.Equal(t, agent.SupportedVersion, d.Version)
	assert.Equal(t, "ghost", d.Service)
	assert.Equal(t, service.ExecPath("ghost"), d.Exec)
	assert.Equal(t, "ghost", d.User)
	assert.Equal(t, "infisical", d.Provider.Kind)
	assert.Equal(t, "ws-123", d.Provider.Project) // project is the workspace id
	assert.Equal(t, "prod", d.Provider.Environment)
	assert.Equal(t, "/ghost", d.Provider.SecretPath)
	assert.Equal(t, "infra/DATABASE_URL", d.Env["DATABASE_URL"])
	// Deployment context is derived from env/region/slug/baseDomain + service + host.
	assert.Equal(t, "us-east-1", d.Deployment.Region)
	assert.Equal(t, "use1", d.Deployment.RegionSlug)
	assert.Equal(t, "prd", d.Deployment.Environment)
	assert.Equal(t, "wardnet.network", d.Deployment.BaseDomain)
	assert.Equal(t, "ghost.svc.prd.use1.wardnet.network", d.Deployment.FQDN)
	// host.id is the full VM resource name built from the resolved compute key.
	assert.Equal(t, "wardnet-prd-use1-vm-ghost-01", d.Deployment.HostID)
	// Provider-supplied cloud/host resource identity round-trips (ADR-0030).
	assert.Equal(t, "hetzner", d.Deployment.CloudProvider)
	assert.Equal(t, "us-east", d.Deployment.CloudRegion)
	assert.Equal(t, "ash", d.Deployment.AvailabilityZone)
	assert.Equal(t, "cx23", d.Deployment.MachineType)
}

// TestRenderDescriptorGlobalScopeIsRegionLess: a global service is region-less, so
// its descriptor's Deployment.Region (INFORGE_DEPLOYMENT_REGION) must be empty — the
// globalScope output-map key must not leak through as a fake region — matching the
// already-empty RegionSlug and the region-less FQDN/HostID.
func TestRenderDescriptorGlobalScopeIsRegionLess(t *testing.T) {
	svc := types.ServiceSpec{Name: "tenants", Container: "tenants", User: "tenants"}

	// Driven exactly as the scopes loop drives the global slice: region=globalScope,
	// slug="".
	out, err := renderDescriptor(svc, types.ComputeOutputs{}, nil, "", "prd", globalScope, "", "wardnet.network", "tenants-01")
	require.NoError(t, err)

	d, err := agent.ParseDescriptor([]byte(out))
	require.NoError(t, err)
	assert.Equal(t, "", d.Deployment.Region, "global service must carry an empty region, not the literal \"global\"")
	assert.Equal(t, "", d.Deployment.RegionSlug)
	assert.Equal(t, "tenants.svc.prd.wardnet.network", d.Deployment.FQDN, "region-less service FQDN")
	assert.Equal(t, "wardnet-prd-vm-tenants-01", d.Deployment.HostID, "region-less host id")
}

// TestRenderDescriptorSecretLess: a nil bundle renders a secret-less descriptor —
// empty provider, no env — that round-trips through the agent's parser
// (which accepts a provider-less descriptor with no env).
func TestRenderDescriptorSecretLess(t *testing.T) {
	svc := types.ServiceSpec{Name: "ghost", Container: "ghost", User: "ghost"}

	out, err := renderDescriptor(svc, types.ComputeOutputs{}, nil, "", "prd", "us-east-1", "use1", "wardnet.network", "ghost-01")
	require.NoError(t, err)

	d, err := agent.ParseDescriptor([]byte(out))
	require.NoError(t, err)
	assert.Equal(t, agent.SupportedVersion, d.Version)
	assert.Equal(t, "ghost", d.Service)
	assert.Equal(t, service.ExecPath("ghost"), d.Exec)
	assert.Equal(t, "ghost", d.User)
	assert.Equal(t, "", d.Provider.Kind)
	assert.Empty(t, d.Env)
	// A secret-less service still carries the deployment context.
	assert.Equal(t, "ghost.svc.prd.use1.wardnet.network", d.Deployment.FQDN)
	assert.Equal(t, "wardnet-prd-use1-vm-ghost-01", d.Deployment.HostID)
}

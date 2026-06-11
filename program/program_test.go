package program

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/bootstrapper"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/service"
	"github.com/wardnet/inforge/internal/types"
)

func TestDeployUsersByHost(t *testing.T) {
	computes := []types.ComputeSpec{
		{Name: "bridge", InstanceCount: 1, DeployUser: &types.DeployUserSpec{Name: "deploy"}},
		{Name: "worker", InstanceCount: 2}, // no deploy user
	}
	got := deployUsersByHost(computes)

	assert.Equal(t, "deploy", got["bridge-01"])
	assert.Equal(t, "", got["worker-01"])
	assert.Equal(t, "", got["worker-02"])
	// The bare name is not a host key — only expanded specKeys are.
	_, ok := got["bridge"]
	assert.False(t, ok)
}

func TestRoutesByHostDerivesEnvScopedFQDN(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Service: []types.ServiceSpec{
			// Two tls-termination services on the same host, declared out of order,
			// to exercise grouping + stable sorting. One service has no ingress.
			{Name: "web", Host: "bridge-01", Ingress: []types.IngressSpec{{Type: types.IngressTypeTLSTermination, Listen: 8080, Target: 3000}}},
			{Name: "api", Host: "bridge", Ingress: []types.IngressSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}}},
			{Name: "worker", Host: "bridge-01"},
		},
	}
	canonical := naming.CanonicalComputeKeys(res.Compute)

	got, err := routesByHost(res, canonical, "prd", "use1", "wardnet.network")
	require.NoError(t, err)

	// `host: bridge` and `host: bridge-01` both land on the same canonical host.
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

	assert.Equal(t, "web", routes[1].Service)
	assert.Equal(t, []string{"web.svc.prd.use1.wardnet.network"}, routes[1].FQDNs)
	assert.Equal(t, 8080, routes[1].Listen)
	assert.Equal(t, 3000, routes[1].Target)

	// The FQDN matches ServiceFQDN exactly — derivation lives in one place.
	assert.Equal(t, naming.ServiceFQDN("prd", "use1", "api", "wardnet.network"), routes[0].FQDNs[0])
}

// A single service carrying a tls-termination entry (with vanity) plus a forward
// entry is the bridge shape: the tls-termination route carries every SNI (auto +
// vanity, sorted) as one server_name list; the forward route carries no SNI and a
// target. Routes sort by listen port.
func TestRoutesByHostTerminatePlusForward(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Service: []types.ServiceSpec{
			{Name: "bridge", Host: "bridge-01", Ingress: []types.IngressSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Vanity: []string{
					"key-broker.{BASE_DOMAIN}",
					"key-broker.inforge.wardnet.network",
				}},
				{Type: types.IngressTypeForward, Listen: 853, Target: 5353},
			}},
		},
	}
	got, err := routesByHost(res, naming.CanonicalComputeKeys(res.Compute), "prd", "use1", "wardnet.network")
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

// Two services may share one listen port when both are tls-termination (nginx
// demuxes by SNI); they render two routes on the same port.
func TestRoutesByHostSharedTLSListen(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Service: []types.ServiceSpec{
			{Name: "a", Host: "bridge-01", Ingress: []types.IngressSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 3000}}},
			{Name: "b", Host: "bridge-01", Ingress: []types.IngressSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 4000}}},
		},
	}
	got, err := routesByHost(res, naming.CanonicalComputeKeys(res.Compute), "prd", "use1", "wardnet.network")
	require.NoError(t, err)
	routes := got["bridge-01"]
	require.Len(t, routes, 2)
	assert.Equal(t, 443, routes[0].Listen)
	assert.Equal(t, 443, routes[1].Listen)
	assert.Equal(t, "a", routes[0].Service)
	assert.Equal(t, "b", routes[1].Service)
}

// TestDerivedRecordsBridge asserts the bridge yields exactly the host record plus
// the service's auto + vanity records, with the right zone-relative names and
// resource-name components, all pointing at the host. The service's two ingress
// entries (tls-termination + forward) both derive "<svc>.svc", which collapses to
// one record.
func TestDerivedRecordsBridge(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", Container: "bridge", InstanceCount: 1}},
		Service: []types.ServiceSpec{
			{Name: "bridge", Container: "bridge", Host: "bridge-01", Ingress: []types.IngressSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Vanity: []string{
					"key-broker.{BASE_DOMAIN}",
					"key-broker.inforge.wardnet.network",
				}},
				{Type: types.IngressTypeForward, Listen: 853, Target: 5353},
			}},
		},
	}

	got := derivedRecords(res, "prd", "use1", "wardnet.network")

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

// Two tls-termination services on one host sharing the same vanity FQDN on the
// same listen port collide on one SNI, which routesByHost must reject
// (createDNSRecords' guard is a no-op without a DNS authority, so the route side
// guards too).
func TestRoutesByHostRejectsDuplicateSNI(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Service: []types.ServiceSpec{
			{Name: "a", Host: "bridge-01", Ingress: []types.IngressSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Vanity: []string{"dup.example.com"}}}},
			{Name: "b", Host: "bridge-01", Ingress: []types.IngressSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 9090, Vanity: []string{"dup.example.com"}}}},
		},
	}
	_, err := routesByHost(res, naming.CanonicalComputeKeys(res.Compute), "prd", "use1", "wardnet.network")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dup.example.com")
}

// The same SNI on different listen ports is legitimate (nginx demuxes per port),
// so routesByHost must NOT reject it.
func TestRoutesByHostAllowsSameSNIDifferentListen(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Service: []types.ServiceSpec{
			{Name: "a", Host: "bridge-01", Ingress: []types.IngressSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Vanity: []string{"shared.example.com"}}}},
			{Name: "b", Host: "bridge-01", Ingress: []types.IngressSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 8443, Target: 9090, Vanity: []string{"shared.example.com"}}}},
		},
	}
	_, err := routesByHost(res, naming.CanonicalComputeKeys(res.Compute), "prd", "use1", "wardnet.network")
	require.NoError(t, err)
}

func TestRoutesByHostNoIngressNoRoutes(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Service: []types.ServiceSpec{{Name: "worker", Host: "bridge-01"}},
	}
	got, err := routesByHost(res, naming.CanonicalComputeKeys(res.Compute), "prd", "use1", "wardnet.network")
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestIngressPortsByHost locks the firewall derivation: the union of a host's
// service ingress listen ports, plus :80 iff a tls-termination entry exists.
func TestIngressPortsByHost(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{
			{Name: "bridge", InstanceCount: 1},
			{Name: "plain", InstanceCount: 1}, // no ingress -> absent from the map
		},
		Service: []types.ServiceSpec{
			{Name: "api", Host: "bridge-01", Ingress: []types.IngressSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080},
				{Type: types.IngressTypeForward, Listen: 853, Target: 5353},
				{Type: types.IngressTypeForward, Listen: 9000, Target: 9001},
			}},
			{Name: "web", Host: "bridge", Ingress: []types.IngressSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 3000}, // dup 443
			}},
		},
	}

	got := ingressPortsByHost(res)
	// :80 added once (tls-termination present), 443 de-duplicated across services,
	// 853 + 9000 (the forward listen ports), sorted.
	assert.Equal(t, []int{80, 443, 853, 9000}, got["bridge-01"])
	_, hasPlain := got["plain-01"]
	assert.False(t, hasPlain, "a host with no ingress contributes no ports")
}

// A forward-only host (no tls-termination entry) opens only its forward listen
// ports — no :80, since ACME never runs there.
func TestIngressPortsByHostForwardOnlyNoPort80(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Service: []types.ServiceSpec{
			{Name: "dns", Host: "bridge-01", Ingress: []types.IngressSpec{
				{Type: types.IngressTypeForward, Listen: 853, Target: 5353},
			}},
		},
	}
	assert.Equal(t, []int{853}, ingressPortsByHost(res)["bridge-01"])
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

// TestServiceProvisionScriptDownloadsBootstrapper guards that provisioning
// downloads the inforge-bootstrap binary, pinned to the inforge version, with
// host-side arch detection and the version quoted (injection-safe).
func TestServiceProvisionScriptDownloadsBootstrapper(t *testing.T) {
	script := serviceProvisionScript(types.ServiceSpec{Name: "api", User: "svc"}, "1.2.3")

	// Version is single-quoted into a shell var, then composed with ${arch}.
	assert.Contains(t, script, "ver='1.2.3'")
	assert.Contains(t, script, "arch=$(uname -m)")
	assert.Contains(t, script, "x86_64) arch=amd64")
	assert.Contains(t, script, "aarch64) arch=arm64")
	assert.Contains(t, script, "asset=\"inforge-bootstrap_${ver}_linux_${arch}\"")
	assert.Contains(t, script, "releases/download/v${ver}")
	assert.Contains(t, script, "curl -fsSL")
	// The download must be checksum-verified before it is installed as root.
	assert.Contains(t, script, "checksums.txt")
	assert.Contains(t, script, "sha256sum")
	assert.Contains(t, script, "checksum mismatch")
	assert.Contains(t, script, "trap 'rm -f \"$tmp\" \"$sums\"' EXIT", "temp files cleaned on any exit")
	assert.Contains(t, script, "install -m 0755 \"$tmp\" '/usr/local/bin/inforge-bootstrap'")
	// The raw inforge version must never be interpolated unquoted into the shell.
	assert.NotContains(t, script, "inforge-bootstrap_1.2.3_linux")
}

// TestRenderDescriptorRoundTrips proves the descriptor inforge writes parses
// back through the bootstrapper's own validator — the producer (this program)
// and consumer (inforge-bootstrap) cannot drift because both use the same
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

	out, err := renderDescriptor(svc, bundle, "ws-123", "prd", "us-east-1", "use1", "wardnet.network")
	require.NoError(t, err)

	d, err := bootstrapper.ParseDescriptor([]byte(out))
	require.NoError(t, err)
	assert.Equal(t, bootstrapper.SupportedVersion, d.Version)
	assert.Equal(t, "ghost", d.Service)
	assert.Equal(t, service.ExecPath("ghost"), d.Exec)
	assert.Equal(t, "ghost", d.User)
	assert.Equal(t, "infisical", d.Provider.Kind)
	assert.Equal(t, "ws-123", d.Provider.Project) // project is the workspace id
	assert.Equal(t, "prod", d.Provider.Environment)
	assert.Equal(t, "/ghost", d.Provider.SecretPath)
	assert.Equal(t, "infra/DATABASE_URL", d.Env["DATABASE_URL"])
	// Deployment context is derived from env/region/slug/baseDomain + service name.
	assert.Equal(t, "us-east-1", d.Deployment.Region)
	assert.Equal(t, "use1", d.Deployment.RegionSlug)
	assert.Equal(t, "prd", d.Deployment.Environment)
	assert.Equal(t, "wardnet.network", d.Deployment.BaseDomain)
	assert.Equal(t, "prd.use1.ghost", d.Deployment.Namespace)
	assert.Equal(t, "ghost.svc.prd.use1.wardnet.network", d.Deployment.FQDN)
}

// TestRenderDescriptorSecretLess: a nil bundle renders a secret-less descriptor —
// empty provider, no env — that round-trips through the bootstrapper's parser
// (which accepts a provider-less descriptor with no env).
func TestRenderDescriptorSecretLess(t *testing.T) {
	svc := types.ServiceSpec{Name: "ghost", Container: "ghost", User: "ghost"}

	out, err := renderDescriptor(svc, nil, "", "prd", "us-east-1", "use1", "wardnet.network")
	require.NoError(t, err)

	d, err := bootstrapper.ParseDescriptor([]byte(out))
	require.NoError(t, err)
	assert.Equal(t, bootstrapper.SupportedVersion, d.Version)
	assert.Equal(t, "ghost", d.Service)
	assert.Equal(t, service.ExecPath("ghost"), d.Exec)
	assert.Equal(t, "ghost", d.User)
	assert.Equal(t, "", d.Provider.Kind)
	assert.Empty(t, d.Env)
	// A secret-less service still carries the deployment context.
	assert.Equal(t, "ghost.svc.prd.use1.wardnet.network", d.Deployment.FQDN)
	assert.Equal(t, "prd.use1.ghost", d.Deployment.Namespace)
}


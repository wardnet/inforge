package program

import (
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/registry"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
)

// infraMocks records every registered resource's logical name and returns a
// superset of outputs so a network + compute + database all resolve under one
// mock. Surplus outputs are harmless: each provider reads only the fields it
// declares.
type infraMocks struct {
	mu    sync.Mutex
	names []string
}

func (m *infraMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *infraMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	m.names = append(m.names, args.Name)
	m.mu.Unlock()
	// hcloud resource IDs are numeric (a subnet references its network by numeric
	// networkId); return a numeric ID for hcloud resources, a name-derived one
	// otherwise.
	id := args.Name + "-id"
	if strings.HasPrefix(args.TypeToken, "hcloud:") {
		id = "12345"
	}
	return id, resource.PropertyMap{
		"name":          resource.NewStringProperty(args.Name),
		"ipRange":       resource.NewStringProperty("10.0.0.0/8"),
		"ipv4Address":   resource.NewStringProperty("1.2.3.4"),
		"projectId":     resource.NewStringProperty("proj-id"),
		"branchId":      resource.NewStringProperty("br-id"),
		"connectionUrl": resource.NewStringProperty("postgresql://u:p@h/db"),
	}, nil
}

func (m *infraMocks) has(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range m.names {
		if n == name {
			return true
		}
	}
	return false
}

// TestCreateInfraGlobalScope exercises the global-first instantiation path end to
// end: createInfra with an EMPTY slug must realize network, compute, and database
// resources under the reserved "global" output slot with REGION-LESS names. This
// is the headline path of the slice — naming.Resource / naming.ResourceInstance
// with an empty slug, flowing through real provider Create calls and through
// assembleManifest's tags.ContainerTag — exercised here, not just in unit tests.
func TestCreateInfraGlobalScope(t *testing.T) {
	mocks := &infraMocks{}
	networkOutputs := map[string]map[string]types.NetworkOutputs{}
	computeOutputs := map[string]map[string]types.ComputeOutputs{}
	databaseOutputs := map[string]map[string]types.DatabaseOutputs{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		// A global registry: built from the global block's providers with an empty
		// slug (region "global" is absent from the empty table → slug "").
		config := map[string]map[string]any{
			"hetzner": {
				"apiToken":     "t",
				"location":     "ash",
				"network_zone": "us-east",
				"serverTypes":  map[string]any{"SMALL": "cx23"},
				"images":       map[string]any{"ubuntu-24.04": "ubuntu-24.04"},
			},
		}
		reg := registry.BuildRegistry(ctx, config, nil, types.SSHConfig{}, regions.Table{}, "proj", "prd", globalScope, tags.Ephemeral{}, nil)

		res := types.Resources{
			Network: []types.NetworkSpec{{
				Name: "corenet", Container: "core", Provider: "hetzner",
				CIDR:    "10.0.0.0/16",
				Subnets: []types.SubnetSpec{{Name: "main", CIDR: "10.0.0.0/24"}},
			}},
			Compute: []types.ComputeSpec{{
				Name: "edge", Container: "core", Provider: "hetzner",
				Network: "corenet", Size: "SMALL", Image: "ubuntu-24.04",
				Kind: "vm", InstanceCount: 1,
			}},
		}

		return createInfra(ctx, reg, res, "prd", globalScope, "", "example.com",
			types.ProviderDefaults{}, networkOutputs, computeOutputs, databaseOutputs)
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	// Outputs land in the reserved "global" slot. Databases are realized separately
	// (provisionDatabaseClusters, ADR-0036), not by createInfra.
	require.Contains(t, computeOutputs, globalScope)
	assert.Contains(t, computeOutputs[globalScope], "edge-01", "global compute output under the global slot")
	assert.Contains(t, networkOutputs[globalScope], "corenet/main", "global subnet output under the global slot")

	// Names are region-less (no slug segment): the empty slug propagated through
	// naming.ResourceInstance and naming.Resource.
	assert.True(t, mocks.has(naming.ResourceInstance("prd", "", "vm", "edge", 1)),
		"global server should have a region-less name (wardnet-prd-vm-edge-01)")
}

// TestCreateInfraResolvesProviderDefaults reproduces the production preview
// failure `hetzner provider received spec with provider=""`: a network/compute/
// database spec that OMITS `provider:` (relying on inforge.yaml providerDefaults)
// must still realize. createInfra resolved the provider to SELECT the registry
// entry but handed the provider the raw spec whose Provider was still empty, and
// the provider's own guard rejected it — so the dispatch must stamp the resolved
// provider onto the spec before Create/AttachNetwork.
func TestCreateInfraResolvesProviderDefaults(t *testing.T) {
	mocks := &infraMocks{}
	networkOutputs := map[string]map[string]types.NetworkOutputs{}
	computeOutputs := map[string]map[string]types.ComputeOutputs{}
	databaseOutputs := map[string]map[string]types.DatabaseOutputs{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		config := map[string]map[string]any{
			"hetzner": {
				"apiToken":     "t",
				"location":     "ash",
				"network_zone": "us-east",
				"serverTypes":  map[string]any{"SMALL": "cx23"},
				"images":       map[string]any{"ubuntu-24.04": "ubuntu-24.04"},
			},
		}
		reg := registry.BuildRegistry(ctx, config, nil, types.SSHConfig{}, regions.Table{}, "proj", "prd", globalScope, tags.Ephemeral{}, nil)

		// Every spec OMITS Provider — exactly the consumer's manifests after moving
		// the provider to inforge.yaml providerDefaults.
		res := types.Resources{
			Network: []types.NetworkSpec{{
				Name: "corenet", Container: "core",
				CIDR:    "10.0.0.0/16",
				Subnets: []types.SubnetSpec{{Name: "main", CIDR: "10.0.0.0/24"}},
			}},
			Compute: []types.ComputeSpec{{
				Name: "edge", Container: "core",
				Network: "corenet", Size: "SMALL", Image: "ubuntu-24.04",
				Kind: "vm", InstanceCount: 1,
			}},
		}

		defaults := types.ProviderDefaults{
			Compute: "hetzner",
		}
		return createInfra(ctx, reg, res, "prd", globalScope, "", "example.com",
			defaults, networkOutputs, computeOutputs, databaseOutputs)
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	assert.Contains(t, networkOutputs[globalScope], "corenet/main", "network realized from provider default")
	assert.Contains(t, computeOutputs[globalScope], "edge-01", "compute realized from provider default")
}

// TestDerivedRecordsGlobalService: a global service (empty slug) derives REGION-LESS
// records — the host <compute>.vm.<env>, the service <svc>.svc.<env>, and any vanity
// — all keyed to the service's ingress host. This mirrors the consumer's global
// `tenants` slice (tls-termination :443 with vanity account.<base>) and is the DNS
// half of the global-realization fix: before it, the global scope never reached
// createDNSRecords, so none of these records deployed.
func TestDerivedRecordsGlobalService(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "tenants", Container: "tenants", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "tenants", Host: "tenants"}},
		Service: []types.ServiceSpec{
			{Name: "tenants", Container: "tenants", Host: "tenants-01", Ingress: "tenants", Routes: []types.RouteSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Vanity: []string{
					"account.{BASE_DOMAIN}",
				}},
			}},
		},
	}

	// Empty slug → the global slice's region-less naming.
	got := derivedRecords(res, "prd", "", "wardnet.network", "")

	type rn struct{ name, record, host string }
	var flat []rn
	for _, d := range got {
		flat = append(flat, rn{d.rec.Name, d.rec.RecordName, d.hostKey})
		assert.Equal(t, "tenants", d.rec.Container)
	}
	assert.Equal(t, []rn{
		{"tenants-vm-prd", "tenants.vm.prd", "tenants-01"},
		{"tenants-svc-prd", "tenants.svc.prd", "tenants-01"},
		{"account", "account", "tenants-01"}, // account.{BASE_DOMAIN} -> account.wardnet.network
	}, flat, "global records carry no slug segment")
}

// TestGlobalServiceRealizesRegionLess: the global scope must reach the SAME host
// pipeline regions use — realizeIngress (nginx/ACME) and provisionServices (the
// systemd unit) — with an EMPTY slug, so the cloud-init gate and the unit-provision
// step register under region-less names and the unit waits on the gate. Before the
// fix this never ran for the global scope (post-processing looped over regions
// only), so a global service deployed no nginx and no unit. Mirrors
// TestTLSAndServiceShareOneGate but with slug "" / region globalScope.
func TestGlobalServiceRealizesRegionLess(t *testing.T) {
	mocks := newCommandMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		// A global registry: built from the global block's providers with the
		// placement region's DNS authority, keyed under globalScope (empty slug).
		reg := registry.BuildRegistry(ctx,
			map[string]map[string]any{"hetzner": {"apiToken": "x"}},
			&regions.DnsAuthority{Provider: "cloudflare", Zone: "z"},
			types.SSHConfig{DeployPrivateKey: "priv"}, nil, "proj", "prd", globalScope, tags.Ephemeral{}, nil)
		res := types.Resources{
			Compute: []types.ComputeSpec{
				{Name: "tenants", Provider: "hetzner", InstanceCount: 1, DeployUser: &types.DeployUserSpec{Name: "deploy"}},
			},
			Ingress: []types.IngressSpec{{Name: "tenants", Host: "tenants"}},
			Service: []types.ServiceSpec{
				{Name: "tenants", Container: "tenants", Host: "tenants-01", Type: "raw", User: "tenants",
					Ingress: "tenants",
					Routes:  []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}}},
			},
		}
		computeOut := map[string]types.ComputeOutputs{
			"tenants-01": {PublicIP: pulumi.String("1.2.3.4").ToStringOutput()},
		}
		gates := map[string]pulumi.Resource{}
		// Empty slug throughout — exactly how the scopes loop drives the global slice.
		if err := realizeIngress(ctx, reg, res, computeOut, gates, nil, "priv", "prd", "", "wardnet.network", "", types.ProviderDefaults{}); err != nil {
			return err
		}
		return provisionServices(ctx, res, computeOut, map[string]serviceMaterial{}, gates, "priv", "prd", globalScope, "", "wardnet.network", "1.2.3")
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	// The gate and the unit-provision step are region-less (slug ""), and the unit
	// waits on the host's single cloud-init gate.
	gate := gateName("prd", "", "tenants-01")
	svc := naming.Resource("prd", "", "svc", "tenants")
	require.Contains(t, mocks.captured, gate, "global host realizes a region-less cloud-init gate")
	require.Contains(t, mocks.captured, svc+"-provision", "global service realizes a region-less systemd unit")
	assert.True(t, mocks.dependsOn(svc+"-provision", gate), "unit provision must wait on the gate")

	// No region slug leaked into any registered command name.
	mocks.mu.Lock()
	defer mocks.mu.Unlock()
	for name := range mocks.captured {
		assert.NotContains(t, name, "-use1-", "global realization names must be region-less")
	}
}

// An out-of-zone vanity FQDN must never reach the DNS provider. The validate-time
// guard (checkVanityZone) does not run on the `inforge deploy`/`preview` path — only
// `inforge validate` calls it — so the realization path carries the same guard:
// records are created ZONE-RELATIVE inside the authority's zone, so "shop.other.net"
// is not a record on other.net, it deploys as shop.other.net.<base_domain>, a wrong
// hostname whose ACME cert can never issue. Fail before any record is written.
func TestCreateDNSRecordsRejectsOutOfZoneVanity(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "edge", Container: "core", InstanceCount: 1}},
		Ingress: []types.IngressSpec{{Name: "web", Host: "edge"}},
		Service: []types.ServiceSpec{
			{Name: "api", Container: "core", Host: "edge-01", Ingress: "web", Routes: []types.RouteSpec{
				{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Vanity: []string{"shop.other.net"}},
			}},
		},
	}
	computeOut := map[string]types.ComputeOutputs{"edge-01": {PublicIP: pulumi.String("1.2.3.4").ToStringOutput()}}
	authority := &regions.DnsAuthority{Provider: "cloudflare", Zone: "zone-id"}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		reg := registry.BuildRegistry(ctx, map[string]map[string]any{
			"cloudflare": {"apiToken": "t"},
		}, authority, types.SSHConfig{}, regions.Table{}, "proj", "prd", globalScope, tags.Ephemeral{}, nil)
		return createDNSRecords(ctx, reg, authority, res, computeOut, "prd", "", "wardnet.network", "")
	}, pulumi.WithMocks("project", "stack", &infraMocks{}))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "shop.other.net")
	assert.Contains(t, err.Error(), "not under base_domain")
}

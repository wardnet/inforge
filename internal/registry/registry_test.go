package registry

import (
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
	cfprovider "github.com/wardnet/inforge/providers/cloudflare"
	"github.com/wardnet/inforge/providers/hetzner"
	"github.com/wardnet/inforge/providers/infisical"
	"github.com/wardnet/inforge/providers/neon"
)

// provMocks records the (typeToken, name) of every resource registered through it.
// Provider resources (e.g. type "pulumi:providers:hcloud") are registered like any
// other, so the mock sees them and we can assert their names are unique.
type provMocks struct {
	mu        sync.Mutex
	providers map[string][]string // typeToken -> resource names registered
}

func (m *provMocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *provMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	if strings.HasPrefix(args.TypeToken, "pulumi:providers:") {
		m.mu.Lock()
		if m.providers == nil {
			m.providers = map[string][]string{}
		}
		m.providers[args.TypeToken] = append(m.providers[args.TypeToken], args.Name)
		m.mu.Unlock()
	}
	return args.Name + "-id", resource.PropertyMap{}, nil
}

// TestProviderResourceNamesAreScopePerRegistry guards against the duplicate-URN
// regression that broke `inforge preview` once a config realized Hetzner +
// Cloudflare resources in more than one scope. program.Run builds one registry per
// scope — one per region plus the region-less global slice — all over the SAME
// Pulumi context. Each registry lazily registers its own hcloud/cloudflare
// *provider* resource; two registries registering a provider under the same name
// produce the same URN, and the real engine then fails the whole run with
// `Duplicate resource URN '…::pulumi:providers:hcloud::hcloud'`.
//
// The provider resource name is what determines the URN, so this asserts the
// invariant directly: across the two scopes, each provider type must be registered
// under distinct names. (The mock monitor does not itself reject duplicate URNs,
// so we check the names rather than rely on RunErr returning an error.)
func TestProviderResourceNamesAreScopePerRegistry(t *testing.T) {
	config := map[string]map[string]any{
		"hetzner":    {"apiToken": "t"},
		"cloudflare": {"apiToken": "t"},
	}
	mocks := &provMocks{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		// Mirror program.Run: a global-slice registry and a regional registry over
		// the same context. Distinct region strings → distinct scopes.
		for _, region := range []string{"global", "us-east-1"} {
			reg := BuildRegistry(ctx, config, &regions.DnsAuthority{Zone: "z"}, types.SSHConfig{}, regions.Table{}, "proj", "prd", region, tags.Ephemeral{})
			// Compute("hetzner") forces hcloud.NewProvider; DNS("cloudflare") forces
			// cf.NewProvider. Both are the singleton provider resources that collide.
			if _, cerr := reg.Compute("hetzner"); cerr != nil {
				return cerr
			}
			if _, derr := reg.DNS("cloudflare"); derr != nil {
				return derr
			}
		}
		return nil
	}, pulumi.WithMocks("proj", "stack", mocks))
	require.NoError(t, err)

	// Both provider types must have been registered once per scope (2 scopes), and
	// every registration must carry a distinct name — else the URNs collide.
	for _, typeToken := range []string{"pulumi:providers:hcloud", "pulumi:providers:cloudflare"} {
		names := mocks.providers[typeToken]
		require.Len(t, names, 2, "%s: expected one provider per scope", typeToken)
		assert.Len(t, dedupe(names), len(names),
			"%s: provider names must be unique per scope, got %v (duplicate URN regression)", typeToken, names)
	}
}

func dedupe(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

func TestRegistryUnknownProvider(t *testing.T) {
	// nil ctx + nil regionTable: providers are built lazily, so construction is
	// not triggered during this test. Only the "unknown provider" paths are hit.
	r := BuildRegistry(nil, map[string]map[string]any{"hetzner": {"apiToken": "x"}}, nil, types.SSHConfig{}, nil, "test-project", "test", "us-east-1", tags.Ephemeral{})

	// "hetzner" is now a known network provider — must succeed.
	np, err := r.Network("hetzner")
	require.NoError(t, err)
	assert.IsType(t, (*hetzner.HetznerNetwork)(nil), np)

	// "hetzner" is now a known compute provider — must succeed.
	cp, err := r.Compute("hetzner")
	require.NoError(t, err)
	assert.IsType(t, (*hetzner.HetznerCompute)(nil), cp)

	// "cloudflare" is now a known DNS provider — must succeed.
	dp, err := r.DNS("cloudflare")
	require.NoError(t, err)
	assert.IsType(t, (*cfprovider.CloudflareDns)(nil), dp)

	// "neon" is now a known database provider — must succeed.
	dbp, err := r.Database("neon")
	require.NoError(t, err)
	assert.IsType(t, (*neon.NeonDatabaseAdapter)(nil), dbp)

	_, err = r.Database("unknown-db")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown provider: "unknown-db"`)

	// "infisical" is a known service-secrets provisioner — must succeed.
	ssp, err := r.ServiceSecretsProvisioner("infisical")
	require.NoError(t, err)
	assert.IsType(t, (*infisical.InfisicalSecretsAdapter)(nil), ssp)

	_, err = r.ServiceSecretsProvisioner("unknown-secrets")
	require.Error(t, err)

	// Unknown network provider still errors.
	_, err = r.Network("unknown-cloud")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown provider: "unknown-cloud"`)
}

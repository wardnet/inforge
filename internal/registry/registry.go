// Package registry maps a provider name (e.g. "hetzner") to the implementation
// satisfying a provider interface. Real providers are constructed lazily on
// first use and memoised via sync.Once so that a provider object is created at
// most once per BuildRegistry call.
package registry

import (
	"fmt"
	"sync"

	cf "github.com/pulumi/pulumi-cloudflare/sdk/v6/go/cloudflare"
	hcloud "github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
	cfprovider "github.com/wardnet/inforge/providers/cloudflare"
	"github.com/wardnet/inforge/providers/hetzner"
)

// ProviderRegistry resolves provider names to provider implementations for one
// region. Each lookup may fail if no provider is registered under that name.
type ProviderRegistry interface {
	Network(name string) (types.NetworkProvider, error)
	Compute(name string) (types.ComputeProvider, error)
	Storage(name string) (types.StorageProvider, error)
	Ingress(name string) (types.IngressProvider, error)
	Mesh(name string) (types.MeshProvider, error)
	DNS(name string) (types.DnsProvider, error)
	Database(name string) (types.DatabaseProvider, error)
}

type registry struct {
	ctx         *pulumi.Context
	project     string
	env         string
	region      string
	slug        string
	eph         tags.Ephemeral
	config      map[string]map[string]any
	dns         *regions.DnsAuthority
	ssh         types.SSHConfig
	regionTable regions.Table
	// sshKeys is the cross-scope SSH key cache shared by every registry of one
	// program run so env-scoped Hetzner SSH keys register under a single URN.
	sshKeys *hetzner.SSHKeyCache

	hetznerProviderOnce sync.Once
	hetznerProvider     *hcloud.Provider
	hetznerProviderErr  error

	hetznerNetOnce sync.Once
	hetznerNet     *hetzner.HetznerNetwork

	hetznerCompOnce sync.Once
	hetznerComp     *hetzner.HetznerCompute

	hetznerTLSOnce sync.Once
	hetznerTLS     *hetzner.HetznerTLS

	hetznerMeshOnce sync.Once
	hetznerMesh     *hetzner.HetznerMesh

	cfProviderOnce sync.Once
	cfProvider     *cf.Provider
	cfProviderErr  error

	cfDnsOnce sync.Once
	cfDns     *cfprovider.CloudflareDns
}

// BuildRegistry constructs a ProviderRegistry from the provider config, SSH
// material, and region table for one region. project, env, and region are used
// to label cloud resources; eph carries the ADR-0028 ephemeral-env labels (zero
// value for a static env). ctx is stored and used lazily when provider objects
// are first constructed — it must be the context passed to the Pulumi program's
// run function. sshKeys is the cross-scope SSH key cache shared by every
// registry of one program run (see NewSSHKeyCache); pass nil for a standalone
// registry (e.g. tests) and hetzner.NewCompute allocates a private cache.
func BuildRegistry(ctx *pulumi.Context, config map[string]map[string]any, dns *regions.DnsAuthority, ssh types.SSHConfig, regionTable regions.Table, project, env, region string, eph tags.Ephemeral, sshKeys *hetzner.SSHKeyCache) ProviderRegistry {
	slug, _ := regionTable.Slug(region) // already validated by loader
	return &registry{
		ctx:         ctx,
		project:     project,
		env:         env,
		region:      region,
		slug:        slug,
		eph:         eph,
		config:      config,
		dns:         dns,
		ssh:         ssh,
		regionTable: regionTable,
		sshKeys:     sshKeys,
	}
}

// NewSSHKeyCache returns the shared SSH key cache program.Run threads into every
// BuildRegistry call so env-scoped Hetzner SSH keys register exactly once across
// all realization scopes. It wraps hetzner.NewSSHKeyCache so callers need not
// import the provider package directly.
func NewSSHKeyCache() *hetzner.SSHKeyCache {
	return hetzner.NewSSHKeyCache()
}

// hetznerProv lazily creates the shared hcloud.Provider for the Hetzner
// provider implementations. Subsequent calls return the cached instance. A
// registration failure is cached and returned to every caller: a nil provider
// would silently degrade every resource built with it onto Pulumi's ambient
// default provider (a different, unconfigured account), so the run must fail.
// A nil ctx (a registry built outside a Pulumi program, e.g. in tests) yields a
// nil provider and no error — nothing is registered.
func (r *registry) hetznerProv() (*hcloud.Provider, error) {
	r.hetznerProviderOnce.Do(func() {
		if r.ctx == nil {
			return
		}
		token := providerCfgString(r.config, "hetzner", "apiToken")
		// The provider resource name is scoped by region so that a program building
		// more than one registry over the same Pulumi context — one per region plus
		// the region-less global slice — does not register two providers under the
		// same URN (pulumi:providers:hcloud::hcloud). r.region is "global" for the
		// global slice and the region name otherwise, so it is always unique.
		p, err := hcloud.NewProvider(r.ctx, fmt.Sprintf("hcloud-%s", r.region), &hcloud.ProviderArgs{
			Token: pulumi.String(token),
		})
		if err != nil {
			r.hetznerProviderErr = fmt.Errorf("register hetzner provider for scope %q: %w", r.region, err)
			return
		}
		r.hetznerProvider = p
	})
	return r.hetznerProvider, r.hetznerProviderErr
}

func (r *registry) Network(name string) (types.NetworkProvider, error) {
	switch name {
	case "hetzner":
		prov, err := r.hetznerProv()
		if err != nil {
			return nil, err
		}
		r.hetznerNetOnce.Do(func() {
			realizations := hetzner.ExtractRegionConfigs(r.region, r.config)
			r.hetznerNet = hetzner.New(prov, r.project, r.slug, r.eph, realizations)
		})
		return r.hetznerNet, nil
	default:
		return nil, unknownProvider(name)
	}
}

// hetznerCompute builds (once) and returns the scope's HetznerCompute. It backs both
// Compute and Storage — the same instance implements ComputeProvider and
// StorageProvider, so a volume attaches to a server the same provider created (it holds
// the h.servers map).
func (r *registry) hetznerCompute() (*hetzner.HetznerCompute, error) {
	prov, err := r.hetznerProv()
	if err != nil {
		return nil, err
	}
	r.hetznerCompOnce.Do(func() {
		realizations := hetzner.ExtractRegionConfigs(r.region, r.config)
		r.hetznerComp = hetzner.NewCompute(
			r.ssh.AuthorizedKeys,
			r.ssh.DeployPublicKey,
			providerCfgString(r.config, "hetzner", "apiToken"),
			prov,
			r.project,
			r.slug,
			r.eph,
			realizations,
			r.sshKeys,
		)
	})
	return r.hetznerComp, nil
}

func (r *registry) Compute(name string) (types.ComputeProvider, error) {
	switch name {
	case "hetzner":
		c, err := r.hetznerCompute()
		if err != nil {
			return nil, err
		}
		return c, nil
	default:
		return nil, unknownProvider(name)
	}
}

// Storage returns the provider that provisions persistent volumes for a compute host
// (ADR-0036). For Hetzner it is the same instance as Compute, so the volume attaches to
// a server that provider built.
func (r *registry) Storage(name string) (types.StorageProvider, error) {
	switch name {
	case "hetzner":
		c, err := r.hetznerCompute()
		if err != nil {
			return nil, err
		}
		return c, nil
	default:
		return nil, unknownProvider(name)
	}
}

// Ingress resolves the provider that realizes a host's nginx ingress proxy. The
// Hetzner realization installs nginx over SSH using the env's deploy private key
// for transport.
func (r *registry) Ingress(name string) (types.IngressProvider, error) {
	switch name {
	case "hetzner":
		r.hetznerTLSOnce.Do(func() {
			r.hetznerTLS = hetzner.NewTLS(r.ssh.DeployPrivateKey, r.slug)
		})
		return r.hetznerTLS, nil
	default:
		return nil, unknownProvider(name)
	}
}

// Mesh resolves the provider that realizes a host's east-west mesh proxy (the
// second, private nginx; ADR-0032). Like Ingress it inherits the host's compute
// provider and installs nginx over SSH using the env's deploy private key.
func (r *registry) Mesh(name string) (types.MeshProvider, error) {
	switch name {
	case "hetzner":
		r.hetznerMeshOnce.Do(func() {
			r.hetznerMesh = hetzner.NewMesh(r.ssh.DeployPrivateKey, r.slug)
		})
		return r.hetznerMesh, nil
	default:
		return nil, unknownProvider(name)
	}
}

// cfProv lazily creates the shared cf.Provider. Like hetznerProv, a registration
// failure is cached and surfaced rather than swallowed — a nil provider would send
// every DNS record to Pulumi's ambient default provider.
func (r *registry) cfProv() (*cf.Provider, error) {
	r.cfProviderOnce.Do(func() {
		if r.ctx == nil {
			return
		}
		token := providerCfgString(r.config, "cloudflare", "apiToken")
		// Scoped by region for the same reason as the hcloud provider: each scope's
		// registry registers its own provider, and a shared name would collide on
		// URN once more than one scope realizes DNS records.
		p, err := cf.NewProvider(r.ctx, fmt.Sprintf("cloudflare-%s", r.region), &cf.ProviderArgs{
			ApiToken: pulumi.StringPtr(token),
		})
		if err != nil {
			r.cfProviderErr = fmt.Errorf("register cloudflare provider for scope %q: %w", r.region, err)
			return
		}
		r.cfProvider = p
	})
	return r.cfProvider, r.cfProviderErr
}

func (r *registry) DNS(name string) (types.DnsProvider, error) {
	switch name {
	case "cloudflare":
		prov, err := r.cfProv()
		if err != nil {
			return nil, err
		}
		r.cfDnsOnce.Do(func() {
			// The zone comes from the region's DNS authority (regions.yaml dns block),
			// not the providers credentials block.
			zoneID := ""
			if r.dns != nil {
				zoneID = r.dns.Zone
			}
			// Record tagging defaults on; non-Enterprise zones must set
			// providers.cloudflare.tagRecords: false (record tags are Enterprise-only).
			tagRecords := providerCfgBool(r.config, "cloudflare", "tagRecords", true)
			r.cfDns = cfprovider.New(zoneID, r.project, r.env, r.slug, tagRecords, prov)
		})
		return r.cfDns, nil
	default:
		return nil, unknownProvider(name)
	}
}

// Database resolves a managed database provider. ADR-0036 retired Neon and made
// self-hosted Postgres the realization in use (program.provisionDatabaseClusters,
// which does NOT go through this method). The managed seam (types.DatabaseProvider)
// is retained so a managed provider can be re-registered here later; today no
// provider is registered, so every name is unknown.
func (r *registry) Database(name string) (types.DatabaseProvider, error) {
	return nil, unknownProvider(name)
}

func unknownProvider(name string) error {
	return fmt.Errorf("unknown provider: %q", name)
}

// providerCfgString returns the string value for key in cfg[provider], or ""
// if the provider or key is absent or the value is not a string.
func providerCfgString(cfg map[string]map[string]any, provider, key string) string {
	if v, ok := cfg[provider][key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// providerCfgBool returns the bool value for key in cfg[provider], or def if the
// provider or key is absent or the value is not a bool.
func providerCfgBool(cfg map[string]map[string]any, provider, key string, def bool) bool {
	if v, ok := cfg[provider][key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

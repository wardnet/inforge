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
	"github.com/wardnet/inforge/internal/types"
	cfprovider "github.com/wardnet/inforge/providers/cloudflare"
	"github.com/wardnet/inforge/providers/hetzner"
	"github.com/wardnet/inforge/providers/infisical"
	"github.com/wardnet/inforge/providers/neon"
)

// ProviderRegistry resolves provider names to provider implementations for one
// region. Each lookup may fail if no provider is registered under that name.
type ProviderRegistry interface {
	Network(name string) (types.NetworkProvider, error)
	Compute(name string) (types.ComputeProvider, error)
	TLSTermination(name string) (types.TLSTerminationProvider, error)
	DNS(name string) (types.DnsProvider, error)
	Database(name string) (types.DatabaseProvider, error)
	ServiceSecretsProvisioner(name string) (types.ServiceSecretsProvisioner, error)
}

type registry struct {
	ctx         *pulumi.Context
	project     string
	env         string
	region      string
	slug        string
	config      map[string]map[string]any
	ssh         types.SSHConfig
	regionTable regions.Table

	hetznerProviderOnce sync.Once
	hetznerProvider     *hcloud.Provider

	hetznerNetOnce sync.Once
	hetznerNet     *hetzner.HetznerNetwork

	hetznerCompOnce sync.Once
	hetznerComp     *hetzner.HetznerCompute

	hetznerTLSOnce sync.Once
	hetznerTLS     *hetzner.HetznerTLS

	cfProviderOnce sync.Once
	cfProvider     *cf.Provider

	cfDnsOnce sync.Once
	cfDns     *cfprovider.CloudflareDns

	neonDbOnce sync.Once
	neonDb     *neon.NeonDatabaseAdapter

	infisicalOnce    sync.Once
	infisicalSecrets *infisical.InfisicalSecretsAdapter
}

// BuildRegistry constructs a ProviderRegistry from the provider config, SSH
// material, and region table for one region. project, env, and region are used
// to label cloud resources. ctx is stored and used lazily when provider objects
// are first constructed — it must be the context passed to the Pulumi program's
// run function.
func BuildRegistry(ctx *pulumi.Context, config map[string]map[string]any, ssh types.SSHConfig, regionTable regions.Table, project, env, region string) ProviderRegistry {
	slug, _ := regionTable.Slug(region) // already validated by loader
	return &registry{
		ctx:         ctx,
		project:     project,
		env:         env,
		region:      region,
		slug:        slug,
		config:      config,
		ssh:         ssh,
		regionTable: regionTable,
	}
}

// hetznerProv lazily creates the shared hcloud.Provider for the Hetzner
// provider implementations. Subsequent calls return the cached instance.
func (r *registry) hetznerProv() *hcloud.Provider {
	r.hetznerProviderOnce.Do(func() {
		if r.ctx == nil {
			return
		}
		token := providerCfgString(r.config, "hetzner", "apiToken")
		p, _ := hcloud.NewProvider(r.ctx, "hcloud", &hcloud.ProviderArgs{
			Token: pulumi.String(token),
		})
		r.hetznerProvider = p
	})
	return r.hetznerProvider
}

func (r *registry) Network(name string) (types.NetworkProvider, error) {
	switch name {
	case "hetzner":
		r.hetznerNetOnce.Do(func() {
			realizations := hetzner.ExtractRegionConfigs(r.config)
			r.hetznerNet = hetzner.New(r.hetznerProv(), r.project, r.slug, realizations)
		})
		return r.hetznerNet, nil
	default:
		return nil, unknownProvider(name)
	}
}

func (r *registry) Compute(name string) (types.ComputeProvider, error) {
	switch name {
	case "hetzner":
		r.hetznerCompOnce.Do(func() {
			realizations := hetzner.ExtractRegionConfigs(r.config)
			r.hetznerComp = hetzner.NewCompute(
				r.ssh.AuthorizedKeys,
				r.ssh.DeployPublicKey,
				providerCfgString(r.config, "hetzner", "apiToken"),
				r.hetznerProv(),
				r.project,
				r.slug,
				realizations,
			)
		})
		return r.hetznerComp, nil
	default:
		return nil, unknownProvider(name)
	}
}

// TLSTermination resolves the provider that realizes tls-termination resources.
// The Hetzner realization installs Caddy over SSH using the env's deploy private
// key for transport.
func (r *registry) TLSTermination(name string) (types.TLSTerminationProvider, error) {
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

func (r *registry) cfProv() *cf.Provider {
	r.cfProviderOnce.Do(func() {
		if r.ctx == nil {
			return
		}
		token := providerCfgString(r.config, "cloudflare", "apiToken")
		p, _ := cf.NewProvider(r.ctx, "cloudflare", &cf.ProviderArgs{
			ApiToken: pulumi.StringPtr(token),
		})
		r.cfProvider = p
	})
	return r.cfProvider
}

func (r *registry) DNS(name string) (types.DnsProvider, error) {
	switch name {
	case "cloudflare":
		r.cfDnsOnce.Do(func() {
			zoneID := providerCfgString(r.config, "cloudflare", "zoneId")
			// Record tagging defaults on; non-Enterprise zones must set
			// providers.cloudflare.tagRecords: false (record tags are Enterprise-only).
			tagRecords := providerCfgBool(r.config, "cloudflare", "tagRecords", true)
			r.cfDns = cfprovider.New(zoneID, r.project, r.env, r.slug, tagRecords, r.cfProv())
		})
		return r.cfDns, nil
	default:
		return nil, unknownProvider(name)
	}
}

func (r *registry) Database(name string) (types.DatabaseProvider, error) {
	switch name {
	case "neon":
		r.neonDbOnce.Do(func() {
			apiKey := providerCfgString(r.config, "neon", "apiKey")
			r.neonDb = neon.New(apiKey, r.project, r.slug)
		})
		return r.neonDb, nil
	default:
		return nil, unknownProvider(name)
	}
}

func (r *registry) ServiceSecretsProvisioner(name string) (types.ServiceSecretsProvisioner, error) {
	switch name {
	case "infisical":
		return r.infisicalAdapter(), nil
	default:
		return nil, unknownProvider(name)
	}
}

// infisicalAdapter lazily creates the shared InfisicalSecretsAdapter.
// Subsequent calls return the cached instance.
func (r *registry) infisicalAdapter() *infisical.InfisicalSecretsAdapter {
	r.infisicalOnce.Do(func() {
		clientId := providerCfgString(r.config, "infisical", "clientId")
		clientSecret := providerCfgString(r.config, "infisical", "clientSecret")
		siteUrl := providerCfgString(r.config, "infisical", "siteUrl")
		r.infisicalSecrets = infisical.New(clientId, clientSecret, siteUrl, r.slug)
	})
	return r.infisicalSecrets
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

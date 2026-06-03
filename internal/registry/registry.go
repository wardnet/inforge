// Package registry maps a provider name (e.g. "hetzner") to the implementation
// satisfying a provider interface. Real providers are constructed lazily on
// first use and memoised via sync.Once so that a provider object is created at
// most once per BuildRegistry call.
package registry

import (
	"fmt"
	"maps"
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
	DNS(name string) (types.DnsProvider, error)
	Database(name string) (types.DatabaseProvider, error)
	Secrets(name string) (types.SecretsBackendProvider, error)
	ManifestContributors() []types.ComputeInstanceManifestContributor
}

type registry struct {
	ctx         *pulumi.Context
	project     string
	config      map[string]map[string]any
	ssh         types.SSHConfig
	regionTable regions.Table

	hetznerProviderOnce sync.Once
	hetznerProvider     *hcloud.Provider

	hetznerNetOnce sync.Once
	hetznerNet     *hetzner.HetznerNetwork

	hetznerCompOnce sync.Once
	hetznerComp     *hetzner.HetznerCompute

	cfProviderOnce sync.Once
	cfProvider     *cf.Provider

	cfDnsOnce sync.Once
	cfDns     *cfprovider.CloudflareDns

	neonDbOnce sync.Once
	neonDb     *neon.NeonDatabaseAdapter

	infisicalOnce    sync.Once
	infisicalSecrets *infisical.InfisicalSecretsAdapter
}

// BuildRegistry constructs a ProviderRegistry from the resolved (merged)
// provider config, SSH material, and region table for one region. project is
// the inforge project name used to label cloud resources. ctx is stored and
// used lazily when provider objects are first constructed — it must be the
// context passed to the Pulumi program's run function.
func BuildRegistry(ctx *pulumi.Context, config map[string]map[string]any, ssh types.SSHConfig, regionTable regions.Table, project string) ProviderRegistry {
	return &registry{
		ctx:         ctx,
		project:     project,
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
			overrides := hetzner.ExtractRegionConfigs(r.regionTable)
			r.hetznerNet = hetzner.New(r.hetznerProv(), r.project, overrides)
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
			overrides := hetzner.ExtractRegionConfigs(r.regionTable)
			r.hetznerComp = hetzner.NewCompute(
				r.ssh.AuthorizedKeys,
				r.ssh.DeployPublicKey,
				r.hetznerProv(),
				r.project,
				overrides,
			)
		})
		return r.hetznerComp, nil
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
			r.cfDns = cfprovider.New(zoneID, r.cfProv())
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
			r.neonDb = neon.New(apiKey)
		})
		return r.neonDb, nil
	default:
		return nil, unknownProvider(name)
	}
}

func (r *registry) Secrets(name string) (types.SecretsBackendProvider, error) {
	switch name {
	case "infisical":
		return r.infisicalAdapter(), nil
	default:
		return nil, unknownProvider(name)
	}
}

func (r *registry) ManifestContributors() []types.ComputeInstanceManifestContributor {
	if _, ok := r.config["infisical"]; !ok {
		return []types.ComputeInstanceManifestContributor{}
	}
	return []types.ComputeInstanceManifestContributor{r.infisicalAdapter()}
}

// infisicalAdapter lazily creates the shared InfisicalSecretsAdapter.
// Subsequent calls return the cached instance.
func (r *registry) infisicalAdapter() *infisical.InfisicalSecretsAdapter {
	r.infisicalOnce.Do(func() {
		clientId := providerCfgString(r.config, "infisical", "clientId")
		clientSecret := providerCfgString(r.config, "infisical", "clientSecret")
		bootstrapSecretEnc := providerCfgString(r.config, "infisical", "bootstrapSecretEnc")
		siteUrl := providerCfgString(r.config, "infisical", "siteUrl")
		r.infisicalSecrets = infisical.New(clientId, clientSecret, bootstrapSecretEnc, siteUrl)
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

// MergeProviders folds per-region provider overrides onto the global provider
// config. For each provider, the override's keys win over the global keys; a
// provider present only in overrides is added. Inner maps are copied so neither
// input is mutated.
func MergeProviders(global, overrides map[string]map[string]any) map[string]map[string]any {
	out := make(map[string]map[string]any, len(global))
	for name, cfg := range global {
		out[name] = copyInner(cfg)
	}
	for name, ov := range overrides {
		merged := copyInner(global[name])
		if merged == nil {
			merged = map[string]any{}
		}
		maps.Copy(merged, ov)
		out[name] = merged
	}
	return out
}

func copyInner(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	maps.Copy(out, m)
	return out
}

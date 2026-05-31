// Package registry maps a provider name (e.g. "hetzner") to the implementation
// satisfying a provider interface. Real providers are constructed lazily on
// first use and memoised via sync.Once so that a provider object is created at
// most once per BuildRegistry call.
package registry

import (
	"fmt"
	"maps"
	"sync"

	hcloud "github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/types"
	"github.com/wardnet/inforge/providers/hetzner"
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
	config      map[string]map[string]any
	ssh         types.SSHConfig
	regionTable regions.Table

	hetznerNetOnce sync.Once
	hetznerNet     *hetzner.HetznerNetwork
}

// BuildRegistry constructs a ProviderRegistry from the resolved (merged)
// provider config, SSH material, and region table for one region. ctx is stored
// and used lazily when provider objects are first constructed — it must be the
// context passed to the Pulumi program's run function.
func BuildRegistry(ctx *pulumi.Context, config map[string]map[string]any, ssh types.SSHConfig, regionTable regions.Table) ProviderRegistry {
	return &registry{
		ctx:         ctx,
		config:      config,
		ssh:         ssh,
		regionTable: regionTable,
	}
}

func (r *registry) Network(name string) (types.NetworkProvider, error) {
	switch name {
	case "hetzner":
		r.hetznerNetOnce.Do(func() {
			overrides := hetzner.ExtractRegionConfigs(r.regionTable)
			if r.ctx == nil {
				// No Pulumi context (e.g. unit tests): create without a provider.
				r.hetznerNet = hetzner.New(nil, overrides)
				return
			}
			token := providerCfgString(r.config, "hetzner", "apiToken")
			p, _ := hcloud.NewProvider(r.ctx, "hcloud", &hcloud.ProviderArgs{
				Token: pulumi.String(token),
			})
			r.hetznerNet = hetzner.New(p, overrides)
		})
		return r.hetznerNet, nil
	default:
		return nil, unknownProvider(name)
	}
}

func (*registry) Compute(name string) (types.ComputeProvider, error) {
	return nil, unknownProvider(name)
}

func (*registry) DNS(name string) (types.DnsProvider, error) {
	return nil, unknownProvider(name)
}

func (*registry) Database(name string) (types.DatabaseProvider, error) {
	return nil, unknownProvider(name)
}

func (*registry) Secrets(name string) (types.SecretsBackendProvider, error) {
	return nil, unknownProvider(name)
}

func (*registry) ManifestContributors() []types.ComputeInstanceManifestContributor {
	return nil
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

// Package registry maps a provider name (e.g. "hetzner") to the implementation
// satisfying a provider interface. Real providers land in later PRs; this phase
// ships a stub that returns "unknown provider" for every lookup, plus the
// MergeProviders helper used to fold per-region overrides onto the global
// provider config.
package registry

import (
	"fmt"
	"maps"

	"github.com/wardnet/inforge/internal/types"
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

// stubRegistry is the phase-1 registry: it holds the resolved provider config
// and SSH material but registers no providers, so every lookup is an error.
//
// When real providers are added, each lookup will construct (and memoise) the
// underlying Pulumi provider and adapter on first use. The intended pattern is
// a per-provider sync.Once guarding a cached value, e.g.:
//
//	var once sync.Once
//	var hcloud *hcloud.Provider
//	once.Do(func() { hcloud = newHcloud(r.config["hetzner"]) })
//
// so that a provider object is created at most once per BuildRegistry call and
// shared across every resource that names it.
type stubRegistry struct {
	config map[string]map[string]any
	ssh    types.SSHConfig
}

// BuildRegistry constructs a ProviderRegistry from the resolved (merged)
// provider config and SSH material for one region.
func BuildRegistry(config map[string]map[string]any, ssh types.SSHConfig) ProviderRegistry {
	return &stubRegistry{config: config, ssh: ssh}
}

func (*stubRegistry) Network(name string) (types.NetworkProvider, error) {
	return nil, unknownProvider(name)
}

func (*stubRegistry) Compute(name string) (types.ComputeProvider, error) {
	return nil, unknownProvider(name)
}

func (*stubRegistry) DNS(name string) (types.DnsProvider, error) {
	return nil, unknownProvider(name)
}

func (*stubRegistry) Database(name string) (types.DatabaseProvider, error) {
	return nil, unknownProvider(name)
}

func (*stubRegistry) Secrets(name string) (types.SecretsBackendProvider, error) {
	return nil, unknownProvider(name)
}

func (*stubRegistry) ManifestContributors() []types.ComputeInstanceManifestContributor {
	return nil
}

func unknownProvider(name string) error {
	return fmt.Errorf("unknown provider: %q", name)
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

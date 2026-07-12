package loader

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/types"
)

// envVarPattern matches the ${NAME} references variables.yaml and regions.yaml
// carry in place of credentials and other environment-supplied values.
var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// The raw types below are the un-substituted form of the two env-substituted
// config files: exactly what is on disk, ${ENV_VAR} placeholders intact.
//
// They are deliberately DISTINCT types from their resolved counterparts
// (types.EnvironmentVariables, regions.Table, regions.Global) rather than the
// same type in an "unresolved" state. That is the whole point: a raw value can
// never be handed to a provider, a registry, or an API client by accident,
// because those take the resolved type and the compiler rejects the raw one. A
// literal `apiToken: ${HCLOUD_TOKEN}` reaching hcloud is a compile error, not a
// runtime surprise.
//
// A caller gets a resolved value only by asking a Resolver for it — and asks
// only for the fields it actually needs. That is what keeps `inforge pki renew`
// and `inforge releases deploy` (which read base_domain and region slugs, and
// nothing else) from demanding an SSH key or a Hetzner token they never touch.
type (
	// RawVariables is variables.yaml with placeholders intact.
	RawVariables types.EnvironmentVariables
	// RawTable is the regions.yaml region table with placeholders intact.
	RawTable map[string]regions.AbstractRegion
	// RawGlobal is the regions.yaml global block with placeholders intact.
	RawGlobal regions.Global
)

// RawRegions is a parsed-but-unresolved regions.yaml: the region table plus the
// optional region-less global block.
type RawRegions struct {
	Table  RawTable
	Global *RawGlobal
}

// Names returns the deploying region names, sorted. Region names are map keys,
// never placeholders, so this needs no resolution.
func (t RawTable) Names() []string {
	out := make([]string, 0, len(t))
	for name := range t {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Slug returns a region's raw slug, erroring when the region is not defined. The
// slug may still hold a placeholder, so this is for structural checks (does the
// region exist?) — resolve it before it names anything real.
func (t RawTable) Slug(abstractRegion string) (string, error) {
	ar, ok := t[abstractRegion]
	if !ok {
		return "", fmt.Errorf("region %q is not defined in regions.yaml", abstractRegion)
	}
	return ar.Slug, nil
}

// Resolver turns raw config into resolved config by expanding ${ENV_VAR}
// references. An unset or empty variable is always an error — there is no
// lenient mode. A caller that does not want to require a value must not resolve
// it: read the raw field instead (structural validation does exactly that,
// checking that base_domain is non-empty without caring whether it is a literal
// or an unexpanded placeholder).
type Resolver struct {
	lookup func(string) (string, bool)
}

// NewResolver resolves against the process environment.
func NewResolver() Resolver {
	return Resolver{lookup: os.LookupEnv}
}

// NewResolverFrom resolves against an explicit lookup — for tests, and for any
// caller that must not read the ambient environment.
func NewResolverFrom(lookup func(string) (string, bool)) Resolver {
	return Resolver{lookup: lookup}
}

// String resolves the placeholders in one field. field is the field's path in
// its source document (e.g. "base_domain", "regions.euc.providers.hetzner.apiToken")
// and appears in the error, so a missing variable names both the config field
// that wanted it and the environment variable that was absent.
func (r Resolver) String(field, raw string) (string, error) {
	var missing []string
	out := envVarPattern.ReplaceAllStringFunc(raw, func(m string) string {
		key := strings.TrimSuffix(strings.TrimPrefix(m, "${"), "}")
		// An env var set to the empty string is treated as absent: a blank
		// credential is never a legitimate value, and silently passing "" on to a
		// provider fails far from the cause.
		val, ok := r.lookup(key)
		if !ok || val == "" {
			missing = append(missing, key)
			return ""
		}
		return val
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("%s: missing required env var: %s", field, strings.Join(missing, ", "))
	}
	return out, nil
}

// Variables resolves every field of variables.yaml. Callers that need the whole
// document (the deploy program, which builds providers and provisions hosts) use
// this and fail fast, before any cloud resource is touched. A caller that needs
// one field resolves just that field with String.
func (r Resolver) Variables(raw RawVariables) (types.EnvironmentVariables, error) {
	var out types.EnvironmentVariables
	fields := []struct {
		path string
		src  string
		dst  *string
	}{
		{"base_domain", raw.BaseDomain, &out.BaseDomain},
		{"ssh.authorizedKeys", raw.SSH.AuthorizedKeys, &out.SSH.AuthorizedKeys},
		{"ssh.deployPublicKey", raw.SSH.DeployPublicKey, &out.SSH.DeployPublicKey},
		{"observability.otlp_endpoint", raw.Observability.OTLPEndpoint, &out.Observability.OTLPEndpoint},
		{"observability.grafana_url", raw.Observability.GrafanaURL, &out.Observability.GrafanaURL},
		{"observability.default_profile", raw.Observability.DefaultProfile, &out.Observability.DefaultProfile},
	}
	for _, f := range fields {
		v, err := r.String(f.path, f.src)
		if err != nil {
			return types.EnvironmentVariables{}, err
		}
		*f.dst = v
	}
	// Carry the non-string observability toggles and any field that never holds a
	// placeholder (DeployPrivateKey is yaml:"-" — injected by the caller, not read
	// from disk).
	out.Observability.BuiltInDashboards = raw.Observability.BuiltInDashboards
	out.Observability.BuiltInAlerts = raw.Observability.BuiltInAlerts
	return out, nil
}

// Regions resolves every value in the region table and the global block —
// notably the provider credentials and the DNS authority's zone. The deploy
// program uses this; nothing else needs a credential.
func (r Resolver) Regions(raw RawRegions) (regions.Table, *regions.Global, error) {
	table := regions.Table{}
	for _, name := range raw.Table.Names() {
		ar := raw.Table[name]
		res, err := r.abstractRegion(fmt.Sprintf("regions.%s", name), ar)
		if err != nil {
			return nil, nil, err
		}
		table[name] = res
	}
	if raw.Global == nil {
		return table, nil, nil
	}
	providers, err := r.providers("global.providers", raw.Global.Providers)
	if err != nil {
		return nil, nil, err
	}
	return table, &regions.Global{
		PlacementRegion: raw.Global.PlacementRegion,
		Providers:       providers,
	}, nil
}

// abstractRegion resolves one region's provider config and DNS authority. The
// slug is a literal by construction (it names cloud resources), but it is
// resolved too so no field is silently exempt.
func (r Resolver) abstractRegion(path string, ar regions.AbstractRegion) (regions.AbstractRegion, error) {
	slug, err := r.String(path+".slug", ar.Slug)
	if err != nil {
		return regions.AbstractRegion{}, err
	}
	providers, err := r.providers(path+".providers", ar.Providers)
	if err != nil {
		return regions.AbstractRegion{}, err
	}
	out := regions.AbstractRegion{Slug: slug, Providers: providers}
	if ar.Dns != nil {
		zone, err := r.String(path+".dns.zone", ar.Dns.Zone)
		if err != nil {
			return regions.AbstractRegion{}, err
		}
		out.Dns = &regions.DnsAuthority{Provider: ar.Dns.Provider, Zone: zone}
	}
	return out, nil
}

// providers resolves a provider-config block. The config is untyped
// (map[string]any per provider), so this walks it generically — an apiToken is a
// string, but serverTypes is a nested map and images a list, and any of them may
// carry a placeholder.
func (r Resolver) providers(path string, cfg map[string]map[string]any) (map[string]map[string]any, error) {
	if cfg == nil {
		return nil, nil
	}
	out := make(map[string]map[string]any, len(cfg))
	for provider, keys := range cfg {
		resolved := make(map[string]any, len(keys))
		for k, v := range keys {
			rv, err := r.value(fmt.Sprintf("%s.%s.%s", path, provider, k), v)
			if err != nil {
				return nil, err
			}
			resolved[k] = rv
		}
		out[provider] = resolved
	}
	return out, nil
}

// value resolves an arbitrary decoded-YAML value, recursing through lists and
// maps. Anything that is not a string is carried through untouched.
func (r Resolver) value(path string, v any) (any, error) {
	switch t := v.(type) {
	case string:
		return r.String(path, t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			rv, err := r.value(fmt.Sprintf("%s[%d]", path, i), e)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			rv, err := r.value(fmt.Sprintf("%s.%s", path, k), e)
			if err != nil {
				return nil, err
			}
			out[k] = rv
		}
		return out, nil
	default:
		return v, nil
	}
}

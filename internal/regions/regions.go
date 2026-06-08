// Package regions holds the region table for an environment: the abstract-region
// → slug mapping plus each region's provider config (credentials + per-provider
// realizations). A project defines this per environment in
// resources/<env>/regions.yaml (see internal/loader); regions.yaml is the single
// authority for which regions deploy and all provider config. Built-in slug
// defaults live here for naming vocabulary. The region slug is used to build
// display names and the <slug> segment of derived DNS names.
package regions

import "fmt"

// AbstractRegion is one entry in the region table: its slug, its DNS authority,
// plus the per-region provider config. Providers maps a provider name (e.g.
// "hetzner") to that provider's config for this region — credentials and the
// region's realization (location, server types, images, …) in a single block.
type AbstractRegion struct {
	Slug      string                    `yaml:"slug"`
	Dns       *DnsAuthority             `yaml:"dns"`
	Providers map[string]map[string]any `yaml:"providers"`
}

// DnsAuthority is the single DNS authority for one (env, region): the provider
// that owns the zone all of the region's auto-derived records are created on.
// Different regions may use different authorities. Credentials stay in the
// matching providers block (e.g. providers.cloudflare.apiToken); the authority
// carries only the provider selector and the zone.
type DnsAuthority struct {
	Provider string `yaml:"provider"` // e.g. "cloudflare"
	Zone     string `yaml:"zone"`     // the authority's zone id records are created in
}

// Table maps an abstract region name (e.g. "us-east-1") to its realization.
type Table map[string]AbstractRegion

// Global is the region-less slot in regions.yaml: provider config only, no slug.
// Resources defined under resources/<env>/global/ realize against it with
// region-less naming. (Consumed by a later slice; parsed and validated here.)
type Global struct {
	Providers map[string]map[string]any `yaml:"providers"`
}

// File is the parsed regions.yaml: the regions map under the top-level
// `regions:` key, plus the optional top-level `global:` block. Keeping global a
// sibling of regions (rather than a reserved entry in the map) makes its
// distinction structural — it runs only the global slice and has no slug.
type File struct {
	Regions Table   `yaml:"regions"`
	Global  *Global `yaml:"global"`
}

// DefaultTable returns a fresh copy of the built-in abstract-region → slug
// vocabulary. It carries slugs only (no provider config); a project supplies the
// providers per region in regions.yaml.
func DefaultTable() Table {
	return Table{
		"us-east-1":    {Slug: "use1"},
		"us-west-1":    {Slug: "usw1"},
		"eu-central-1": {Slug: "euc1"},
		"ap-east-1":    {Slug: "ape1"},
	}
}

// Slug returns the slug for an abstract region, or an error if the region is
// not defined in the table.
func (t Table) Slug(abstractRegion string) (string, error) {
	r, ok := t[abstractRegion]
	if !ok {
		return "", fmt.Errorf("unknown abstract region %q — define it in resources/<env>/regions.yaml or internal/regions", abstractRegion)
	}
	return r.Slug, nil
}

// Validate reports whether an abstract region is defined in the table.
func (t Table) Validate(abstractRegion string) error {
	_, err := t.Slug(abstractRegion)
	return err
}

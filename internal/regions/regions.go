// Package regions holds the abstract-region → slug mapping (the "region
// table"). Built-in defaults live here; a project may replace them per
// environment with resources/<env>/regions.yaml (see internal/loader). The
// region slug is used to build display names and DNS subdomains.
package regions

import "fmt"

// AbstractRegion is one entry in the region table. It carries only the slug —
// no provider topology. Each provider's concrete realization of a region
// (location, server types, images, …) lives in the provider config under
// providers.<name>.regions in variables.yaml.
type AbstractRegion struct {
	Slug string `yaml:"slug"`
}

// Table maps an abstract region name (e.g. "us-east-1") to its slug ("use1").
type Table map[string]AbstractRegion

// DefaultTable returns a fresh copy of the built-in region table. A per-env
// regions.yaml, when present, replaces this wholesale rather than merging.
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

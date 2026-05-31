// Package hetzner implements the Hetzner Cloud provider for inforge. It maps
// abstract region names to Hetzner-specific location + networkZone pairs.
// Built-in defaults live in DefaultRegionConfigs; projects override them via
// the providers.hetzner block in resources/<env>/regions.yaml.
package hetzner

import (
	"fmt"

	"github.com/wardnet/inforge/internal/regions"
)

// RegionConfig is Hetzner's location + networkZone pair for one abstract region.
type RegionConfig struct {
	Location    string
	NetworkZone string
}

// DefaultRegionConfigs provides Hetzner mappings for the built-in abstract
// regions. Projects using custom region names, or that need to override a
// default, add entries under providers.hetzner in resources/<env>/regions.yaml.
var DefaultRegionConfigs = map[string]RegionConfig{
	"us-east-1":    {Location: "ash", NetworkZone: "us-east"},
	"eu-central-1": {Location: "hel1", NetworkZone: "eu-central"},
	"ap-east-1":    {Location: "sin", NetworkZone: "ap-southeast"},
}

// ExtractRegionConfigs reads the providers["hetzner"] block from each entry in
// the region table and builds a RegionConfig map. Only entries that supply both
// "location" and "network_zone" string keys are included; entries that supply
// neither are silently skipped (they fall back to DefaultRegionConfigs at
// resolve time).
func ExtractRegionConfigs(t regions.Table) map[string]RegionConfig {
	out := make(map[string]RegionConfig, len(t))
	for name, entry := range t {
		hcfg, ok := entry.Providers["hetzner"]
		if !ok {
			continue
		}
		loc, _ := hcfg["location"].(string)
		zone, _ := hcfg["network_zone"].(string)
		if loc == "" || zone == "" {
			continue
		}
		out[name] = RegionConfig{Location: loc, NetworkZone: zone}
	}
	return out
}

// ResolveRegion resolves an abstract region name to a Hetzner RegionConfig.
// Project overrides (from regions.yaml) take precedence over DefaultRegionConfigs.
// If neither source has a mapping, a clear error is returned.
func ResolveRegion(abstractRegion string, overrides map[string]RegionConfig) (RegionConfig, error) {
	if cfg, ok := overrides[abstractRegion]; ok {
		return cfg, nil
	}
	if cfg, ok := DefaultRegionConfigs[abstractRegion]; ok {
		return cfg, nil
	}
	return RegionConfig{}, fmt.Errorf(
		"hetzner has no datacenter for abstract region %q — add a mapping under providers.hetzner in resources/<env>/regions.yaml",
		abstractRegion,
	)
}

// Package hetzner implements the Hetzner Cloud provider for inforge. It maps
// each abstract region an environment uses to a complete Hetzner region
// realization — location, network zone, the server type per size, and the image
// id per canonical image — read from that region's
// regions.yaml regions.<region>.providers.hetzner block. Realizations are fully
// explicit per region: there are no built-in defaults and no inheritance.
package hetzner

import (
	"fmt"
)

// RegionConfig is Hetzner's complete realization of one abstract region: its
// location + network zone, the server type for each size name, and the provider
// image id for each canonical image.
type RegionConfig struct {
	Location    string
	NetworkZone string
	ServerTypes map[string]string // size name -> Hetzner server type SKU
	Images      map[string]string // canonical image -> Hetzner image id
}

// ExtractRegionConfigs reads the hetzner realization for a single abstract
// region from that region's provider config (regions.yaml
// regions.<region>.providers) and returns it keyed by the region name, so
// ResolveRegion still looks it up by abstract region. The hetzner block supplies
// "location", "network_zone", "serverTypes" (a size-name → SKU map) and "images"
// (a canonical-image → image-id map) directly — there is no nested regions map.
// Values arrive as map[string]any from yaml, so non-string and empty entries are
// skipped. It is nil-safe and returns an empty map when the region declares no
// hetzner provider. Missing fields are not validated here — ResolveRegion and
// compute.Create fail closed at use time.
func ExtractRegionConfigs(region string, providers map[string]map[string]any) map[string]RegionConfig {
	h := providers["hetzner"]
	if h == nil {
		return map[string]RegionConfig{}
	}
	loc, _ := h["location"].(string)
	zone, _ := h["network_zone"].(string)
	return map[string]RegionConfig{
		region: {
			Location:    loc,
			NetworkZone: zone,
			ServerTypes: extractStringMap(h["serverTypes"]),
			Images:      extractStringMap(h["images"]),
		},
	}
}

// extractStringMap turns a yaml-decoded map[string]any into a map[string]string,
// skipping non-string and empty values. It returns an empty map when v is not a
// map.
func extractStringMap(v any) map[string]string {
	raw, ok := v.(map[string]any)
	if !ok {
		return map[string]string{}
	}
	out := make(map[string]string, len(raw))
	for k, val := range raw {
		if s, ok := val.(string); ok && s != "" {
			out[k] = s
		}
	}
	return out
}

// ResolveRegion returns the Hetzner realization for an abstract region. There is
// no default fallback: a region absent from the realizations, or one missing its
// location/network_zone, is an error — so an incomplete realization fails here at
// the provider boundary with a clear message rather than as an opaque hcloud
// error at apply time. (serverTypes/images are checked per size/image in Create.)
func ResolveRegion(abstractRegion string, realizations map[string]RegionConfig) (RegionConfig, error) {
	cfg, ok := realizations[abstractRegion]
	if !ok {
		return RegionConfig{}, fmt.Errorf(
			"hetzner has no region realization for %q — add it under providers.hetzner.regions.%s in variables.yaml",
			abstractRegion, abstractRegion,
		)
	}
	if cfg.Location == "" {
		return RegionConfig{}, fmt.Errorf(
			"hetzner region realization for %q is missing location — set providers.hetzner.regions.%s.location in variables.yaml",
			abstractRegion, abstractRegion,
		)
	}
	if cfg.NetworkZone == "" {
		return RegionConfig{}, fmt.Errorf(
			"hetzner region realization for %q is missing network_zone — set providers.hetzner.regions.%s.network_zone in variables.yaml",
			abstractRegion, abstractRegion,
		)
	}
	return cfg, nil
}

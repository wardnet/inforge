package neon

import "fmt"

var neonRegions = map[string]string{
	"us-east-1": "aws-us-east-2",
	"eu-west-1": "aws-eu-west-2",
	"ap-east-1": "aws-ap-southeast-1",
}

// ResolveRegion maps an abstract region name to the corresponding Neon region ID.
func ResolveRegion(abstractRegion string) (string, error) {
	if r, ok := neonRegions[abstractRegion]; ok {
		return r, nil
	}
	return "", fmt.Errorf(
		"neon has no region mapping for %q — add a mapping in providers/neon/regions.go or set providers.neon.region in regions.yaml",
		abstractRegion,
	)
}

// resolveRegion picks the physical Neon region for a project. An explicit region
// from provider config (n.region) wins — it is the only source for the region-less
// global slice, which has no abstract region to map. Otherwise the abstract region
// is resolved through the built-in map (the regional default).
func (n *NeonDatabaseAdapter) resolveRegion(abstractRegion string) (string, error) {
	if n.region != "" {
		return n.region, nil
	}
	return ResolveRegion(abstractRegion)
}

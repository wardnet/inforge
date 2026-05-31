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
		"neon has no region mapping for %q — add a mapping in providers/neon/regions.go",
		abstractRegion,
	)
}

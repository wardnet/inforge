// Package images enumerates the canonical OS images a compute resource may
// request. Providers map a canonical image to their own image identifier.
package images

// CanonicalImage is a provider-agnostic OS image identifier.
type CanonicalImage string

// Ubuntu2404 is the only image supported this phase.
const Ubuntu2404 CanonicalImage = "ubuntu-24.04"

// canonical is the set of accepted images.
var canonical = map[CanonicalImage]struct{}{
	Ubuntu2404: {},
}

// IsValid reports whether img is a known canonical image.
func IsValid(img string) bool {
	_, ok := canonical[CanonicalImage(img)]
	return ok
}

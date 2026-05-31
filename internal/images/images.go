// Package images enumerates the canonical OS images a compute resource may
// request. Providers map a canonical image to their own image identifier.
package images

// CanonicalImage is a provider-agnostic OS image identifier.
type CanonicalImage string

const (
	Ubuntu2404 CanonicalImage = "ubuntu-24.04"
	Ubuntu2604 CanonicalImage = "ubuntu-26.04"
)

// canonical is the set of accepted images.
var canonical = map[CanonicalImage]struct{}{
	Ubuntu2404: {},
	Ubuntu2604: {},
}

// IsValid reports whether img is a known canonical image.
func IsValid(img string) bool {
	_, ok := canonical[CanonicalImage(img)]
	return ok
}

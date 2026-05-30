// Package naming builds the canonical identifiers for resources: the specKey
// (a resource instance's foreign-key identity) and the fully-qualified display
// name used by providers.
package naming

import "fmt"

// usage is the fixed top-level namespace segment.
const usage = "wardnet"

// SpecKey returns a resource instance's identity, "<name>-<NN>" with the
// instance number zero-padded to two digits (e.g. SpecKey("bridge", 1) ==
// "bridge-01"). It is the value other resources use as a foreign key.
func SpecKey(name string, instance int) string {
	return fmt.Sprintf("%s-%02d", name, instance)
}

// DisplayName returns the fully-qualified resource name,
// "wardnet-<env>-<resourceType>-<slug>-<specKey>".
func DisplayName(env, resourceType, locationSlug, name string, instance int) string {
	return fmt.Sprintf("%s-%s-%s-%s-%s", usage, env, resourceType, locationSlug, SpecKey(name, instance))
}

// Package naming builds the canonical identifiers for resources: the specKey
// (a resource instance's foreign-key identity) and the fully-qualified resource
// names used by providers.
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

// Resource returns wardnet-<env>-<regionSlug>-<type>-<name>.
func Resource(env, regionSlug, resourceType, name string) string {
	return fmt.Sprintf("%s-%s-%s-%s-%s", usage, env, regionSlug, resourceType, name)
}

// ResourceInstance returns wardnet-<env>-<regionSlug>-<type>-<name>-<NN>.
// Only for resources with instance_count (servers).
func ResourceInstance(env, regionSlug, resourceType, name string, instance int) string {
	return fmt.Sprintf("%s-%s-%s-%s-%s-%02d", usage, env, regionSlug, resourceType, name, instance)
}

// GlobalResource returns wardnet-<env>-<type>-<name> (no region).
// For env-scoped resources like SSH keys.
func GlobalResource(env, resourceType, name string) string {
	return fmt.Sprintf("%s-%s-%s-%s", usage, env, resourceType, name)
}

// RecordName returns the zone-relative DNS record name for a host:
// "<subdomain>.<env>.<regionSlug>" (e.g. "bridge.prd.use1"). The DNS provider
// appends the zone (base domain) to form the FQDN. This is the single source of
// truth for DNS record naming — see RecordFQDN for the absolute form.
func RecordName(env, regionSlug, subdomain string) string {
	return fmt.Sprintf("%s.%s.%s", subdomain, env, regionSlug)
}

// RecordFQDN returns the fully-qualified domain for a host:
// "<subdomain>.<env>.<regionSlug>.<baseDomain>" (e.g.
// "bridge.prd.use1.wardnet.network"). It is RecordName plus the base domain, so
// the DNS record, the VM's cloud-init domain, and the deploy descriptor's host
// DNS all agree.
func RecordFQDN(env, regionSlug, subdomain, baseDomain string) string {
	return fmt.Sprintf("%s.%s", RecordName(env, regionSlug, subdomain), baseDomain)
}

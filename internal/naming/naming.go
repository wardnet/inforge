// Package naming builds the canonical identifiers for resources: the specKey
// (a resource instance's foreign-key identity) and the fully-qualified resource
// names used by providers.
package naming

import (
	"fmt"
	"strings"

	"github.com/wardnet/inforge/internal/types"
)

// usage is the fixed top-level namespace segment.
const usage = "wardnet"

// CanonicalComputeKeys maps every accepted compute foreign-key form to its
// canonical expanded specKey. Each instance i of a compute is keyed by its
// SpecKey (e.g. "bridge-01" -> "bridge-01"); a single-instance compute is
// additionally keyed by its bare name ("bridge" -> "bridge-01"), so that
// `compute: bridge` (a tls-termination FK) and `host: bridge-01` (a service FK)
// resolve to the same host. Validation and the program both resolve compute FKs
// through this map so the two agree on what "the host" is.
func CanonicalComputeKeys(computes []types.ComputeSpec) map[string]string {
	canonical := map[string]string{}
	for _, c := range computes {
		for i := 1; i <= c.InstanceCount; i++ {
			key := SpecKey(c.Name, i)
			canonical[key] = key
		}
		if c.InstanceCount == 1 {
			canonical[c.Name] = SpecKey(c.Name, 1)
		}
	}
	return canonical
}

// DeployUsersByHost maps each expanded compute specKey to its deploy_user (empty
// when the compute declares none — the key is simply absent, so a lookup yields
// ""). inforge SSHes as this user to realize host-level resources; the realization
// path (program) and the deploy descriptors (internal/service, internal/app) share
// this one derivation so they can never disagree on a host's SSH account.
func DeployUsersByHost(computes []types.ComputeSpec) map[string]string {
	byHost := map[string]string{}
	for _, c := range computes {
		if c.DeployUser == nil {
			continue
		}
		for i := 1; i <= c.InstanceCount; i++ {
			byHost[SpecKey(c.Name, i)] = c.DeployUser.Name
		}
	}
	return byHost
}

// SpecKey returns a resource instance's identity, "<name>-<NN>" with the
// instance number zero-padded to two digits (e.g. SpecKey("bridge", 1) ==
// "bridge-01"). It is the value other resources use as a foreign key.
func SpecKey(name string, instance int) string {
	return fmt.Sprintf("%s-%02d", name, instance)
}

// BareComputeName strips the "-NN" instance suffix from a canonical compute
// specKey ("bridge-01" → "bridge") — SpecKey's inverse, and the bare name a
// host's DNS record is derived from. The single home for the strip rule: the
// service deploy descriptor and the mesh deploy descriptor both derive host
// FQDNs through it, so the two can never disagree on what a specKey's bare
// name is.
func BareComputeName(specKey string) string {
	if i := strings.LastIndex(specKey, "-"); i > 0 {
		return specKey[:i]
	}
	return specKey
}

// Resource returns wardnet-<env>-<regionSlug>-<type>-<name>. An EMPTY regionSlug
// is the global scope: the region segment is dropped, yielding the region-less
// wardnet-<env>-<type>-<name> (identical to GlobalResource). Global resources
// (resources/<env>/global/) realize with an empty slug so their names carry no
// region; regional callers always pass a non-empty slug (validated in
// checkRegionsFile), so the regional path is never silently globalised.
func Resource(env, regionSlug, resourceType, name string) string {
	if regionSlug == "" {
		return GlobalResource(env, resourceType, name)
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s", usage, env, regionSlug, resourceType, name)
}

// ResourceInstance returns wardnet-<env>-<regionSlug>-<type>-<name>-<NN>.
// Only for resources with instance_count (servers). An EMPTY regionSlug is the
// global scope: the region segment is dropped, yielding
// wardnet-<env>-<type>-<name>-<NN> (see Resource).
func ResourceInstance(env, regionSlug, resourceType, name string, instance int) string {
	if regionSlug == "" {
		return fmt.Sprintf("%s-%s-%s-%s-%02d", usage, env, resourceType, name, instance)
	}
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
// truth for DNS record naming — see RecordFQDN for the absolute form. An EMPTY
// regionSlug is the global scope: the region segment is dropped, yielding
// "<subdomain>.<env>".
func RecordName(env, regionSlug, subdomain string) string {
	if regionSlug == "" {
		return fmt.Sprintf("%s.%s", subdomain, env)
	}
	return fmt.Sprintf("%s.%s.%s", subdomain, env, regionSlug)
}

// RecordFQDN returns the fully-qualified domain for a record:
// "<subdomain>.<env>.<regionSlug>.<baseDomain>" (e.g.
// "bridge.prd.use1.wardnet.network"). It is RecordName plus the base domain, so
// the DNS record, the VM's cloud-init domain, and the deploy descriptor's host
// DNS all agree.
func RecordFQDN(env, regionSlug, subdomain, baseDomain string) string {
	return fmt.Sprintf("%s.%s", RecordName(env, regionSlug, subdomain), baseDomain)
}

// HostFQDN returns a host's (VM's) fully-qualified domain, derived from its bare
// compute name with the "vm" type segment: "<compute>.vm.<env>.<slug>.<base>"
// (e.g. "bridge.vm.prd.use1.wardnet.network"). This is the host's SSH /
// cloud-init domain and is deterministic from the compute — never authored as a
// free-form subdomain, so it can never be confused with a service record.
func HostFQDN(env, regionSlug, compute, baseDomain string) string {
	return RecordFQDN(env, regionSlug, compute+".vm", baseDomain)
}

// ServiceFQDN returns a service's canonical (auto-derived) public domain with the
// "svc" type segment: "<service>.svc.<env>.<slug>.<base>" (e.g.
// "bridge.svc.prd.use1.wardnet.network"). It is the default SNI/ACME-cert FQDN a
// service's ingress terminates or matches; vanity FQDNs are additive (see
// ExpandVanity).
func ServiceFQDN(env, regionSlug, service, baseDomain string) string {
	return RecordFQDN(env, regionSlug, service+".svc", baseDomain)
}

// AppFQDN returns an app's public fully-qualified domain — the clean dotted form
// a front-end is served on. For a STATIC env (ephemeralSlug == "") it carries NO
// env segment — each environment has its own base_domain: the global scope is
// "<subdomain>.<base>" (e.g. "marketing.wardnet.network") and a regional scope is
// "<subdomain>.<slug>.<base>" (e.g. "my.use1.wardnet.network"). An EMPTY
// regionSlug is the global scope. It is deliberately flatter than
// ServiceFQDN/RecordFQDN (which carry an env and a type segment): an app is a
// public-facing site, not an internal derived record. The no-env-segment property
// is CONDITIONAL — it holds only for a static env; an ephemeral env (below) carries
// its slug identity segment, so callers must not assume an app URL is env-stable.
//
// ephemeralSlug is the ADR-0028 ephemeral-environment exception: a static env
// passes "" and its app URLs are unchanged, but an ephemeral env passes its
// identity slug (e.g. "eph-7f3k"), inserted right after the subdomain
// ("<subdomain>.<ephemeralSlug>.<base>" global /
// "<subdomain>.<ephemeralSlug>.<slug>.<base>" regional). An ephemeral env clones
// the source's base_domain, so without this segment its app hostname would
// collide with the source's; the slug segment keeps the clone's app URLs distinct.
func AppFQDN(subdomain, regionSlug, baseDomain, ephemeralSlug string) string {
	parts := make([]string, 0, 4)
	parts = append(parts, subdomain)
	if ephemeralSlug != "" {
		parts = append(parts, ephemeralSlug)
	}
	if regionSlug != "" {
		parts = append(parts, regionSlug)
	}
	parts = append(parts, baseDomain)
	return strings.Join(parts, ".")
}

// ExpandVanity resolves one vanity entry to a fully-qualified domain. A bare
// token (no dot, no "{" placeholder) is env+region-scoped exactly like a service
// record: "foo" -> "foo.<env>.<slug>.<base>". Anything containing a dot or a
// placeholder is treated as a literal/template FQDN and used as-is after
// expanding {BASE_DOMAIN}, {ENV} and {REGION_SLUG}.
func ExpandVanity(vanity, env, regionSlug, baseDomain string) string {
	if !strings.ContainsAny(vanity, ".{") {
		return RecordFQDN(env, regionSlug, vanity, baseDomain)
	}
	r := strings.NewReplacer(
		"{BASE_DOMAIN}", baseDomain,
		"{ENV}", env,
		"{REGION_SLUG}", regionSlug,
	)
	return r.Replace(vanity)
}

// InZone reports whether fqdn lies inside baseDomain's zone (the apex itself, or
// any name under it). It is ZoneRelative's PRECONDITION: the DNS provider is handed
// a zone-relative name and appends the zone itself, so ZoneRelative on an
// out-of-zone FQDN returns that FQDN unchanged and the record silently lands as
// "<fqdn>.<baseDomain>". Callers that accept an authored FQDN (a route vanity) must
// gate on this — the single home for the rule, shared by the validator and the
// realization path so the two can never disagree on what "in the zone" means.
func InZone(fqdn, baseDomain string) bool {
	return fqdn == baseDomain || strings.HasSuffix(fqdn, "."+baseDomain)
}

// ZoneRelative returns a record's zone-relative name: the FQDN with the trailing
// base domain (the DNS authority's zone) removed. The zone apex (fqdn ==
// baseDomain) becomes "@". Cloudflare appends the zone, so providers consume this
// form rather than the absolute FQDN. fqdn MUST be InZone(fqdn, baseDomain).
func ZoneRelative(fqdn, baseDomain string) string {
	if fqdn == baseDomain {
		return "@"
	}
	return strings.TrimSuffix(fqdn, "."+baseDomain)
}

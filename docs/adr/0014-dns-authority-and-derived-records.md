---
status: accepted
date: 2026-06-08
issue: "#76"
---

# DNS is a per-(env, region) authority; records are derived, not authored

Until now DNS was a per-record resource: a `DnsSpec` (`dns/*.yaml`) with a free-form `subdomain`,
`compute` FK and `provider`, creating one A-record. The host's SSH/cloud-init domain and a service's
ingress SNI/ACME-cert domain were entangled through it: `ingress.hostname` was env-scoped to the SNI
**and** the cert domain, while a host's `DnsSpec{compute, subdomain}` doubled as the SSH helper. A
single VM that fronts multiple services (the `wardnet-infrastructure` **bridge**: terminate its own
SNI → `:8080`, pass every other SNI through → `:8443`) could not express a per-service cert decoupled
from the host record, and the host-resolution lookup (`subdomainFor` / `hostDNS`) was a first-match
scan over `res.DNS` that became order-dependent the moment a host carried more than one record.

We decided to make **DNS a per-(env, region) authority** and **derive every record** instead of
authoring it:

- The authority (provider + zone) is declared once per region in `regions.yaml` under a `dns:` block,
  a sibling of `providers:` (credentials stay in `providers.<name>`). Different regions may use
  different authorities; a region without a `dns:` block creates no records.
- `DnsSpec` and the `dns/` resource directory are removed. inforge derives and creates, on the
  region's authority:
  - one **host** record `<compute>.vm.<env>.<slug>.<base>` per host (its SSH/cloud-init domain),
  - one **service** record `<service>.svc.<env>.<slug>.<base>` per non-catch-all ingress entry,
  - one record per **vanity** FQDN an ingress entry lists.
- Record FQDNs follow the resource naming convention with a type segment (`vm`, `svc`) after the name,
  so host and service records are structurally distinguishable and the host domain is deterministic
  from the compute — eliminating the first-match ambiguity.
- A terminate route gets a DNS record **and** an ACME certificate per FQDN; a named passthrough gets a
  record but no certificate (the backend owns TLS); a catch-all gets neither.

This is coupled with making `ServiceSpec.Ingress` a **list** (ADR-context: a service may carry one
terminate/named entry — which owns the auto `<svc>.svc` name — plus one catch-all, deployed as one
service), so the bridge is one deployable with one DeployTarget.

## Considered options

- **Explicit per-service `DnsSpec` records + a host/service discriminator.** Rejected: keeps DNS
  hand-authored and still needs a discriminator to disambiguate host resolution; more config for the
  consumer with no gain once names are convention-derived.
- **Auto-create cert DNS from ingress, but keep `DnsSpec` for host records.** Rejected: leaves two
  DNS mechanisms and the order-dependent host lookup in place.
- **A per-(env, region) authority; all records derived (chosen).** One DNS mechanism, deterministic
  names, host/service decoupled by construction.

## Consequences

- `regions.AbstractRegion` gains an optional `dns: {provider, zone}`; the Cloudflare provider's zone
  moves from `providers.cloudflare.zoneId` to `dns.zone`. `BuildRegistry` threads the authority so the
  DNS provider is constructed with the authority's zone.
- `types.DnsSpec` → `types.DnsRecord` (a derived record: unique resource-name component + zone-relative
  name + container + proxied). `DnsProvider.Create(DnsSpec)` → `CreateRecord(DnsRecord)`.
- `naming` gains `HostFQDN`, `ServiceFQDN`, `ExpandVanity` (placeholders `{BASE_DOMAIN}` / `{ENV}` /
  `{REGION_SLUG}`, bare-token scoping) and `ZoneRelative` (apex → `@`). `RecordName`/`RecordFQDN`
  remain as building blocks.
- The program's per-record DNS loop and `subdomainFor` are replaced by `createDNSRecords`; the
  ingress→FQDN derivation lives in one `ingressFQDNs` used by **both** the TLS routes and the DNS
  records, so a cert and its A-record cannot drift.
- `validate` drops `DnsSpec` schema/semantic checks and enforces, per service: ≤1 catch-all and ≤1
  non-catch-all entry (the non-catch-all entry owns the auto `<svc>.svc` name).
- The `wardnet-infrastructure` consumer is controlled end-to-end, so the config shape changes without
  a back-compat shim. An explicit per-YAML version field was considered and deferred (it introduces a
  compatibility-check problem); the breaking change ships in the next inforge release tag.

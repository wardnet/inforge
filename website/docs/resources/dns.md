---
sidebar_position: 3
---

# DNS

DNS is **not a resource you author**. inforge derives every record automatically and creates it
against the region's [DNS authority](../configuration/regions-yaml#dns) — the single
provider + zone declared per `(env, region)` in `regions.yaml`. Different regions may use different
authorities.

## Derived records

Every record is an A-record pointing at a host's public IP, named with the resource naming convention
(a type segment after the name):

| Record | FQDN | Source | Certificate? |
|--------|------|--------|--------------|
| Host | `<compute>.vm.<env>.<slug>.<base>` | each [Compute](./compute) host (its SSH / cloud-init domain) | no |
| Service | `<service>.svc.<env>.<slug>.<base>` | any [ingress](./service#ingress) entry | yes, on a `tls-termination` route |
| Vanity | the expanded vanity value | a `tls-termination` entry's `vanity` list | yes |

A `forward` entry gets the `<svc>.svc` DNS record but no certificate (the backend owns TLS). The
`<svc>.svc` name is derived once per service even if it carries several ingress entries.

For environment `prd`, region `us-east-1` (slug `use1`), `base_domain: wardnet.network`:

- host `bridge` → `bridge.vm.prd.use1.wardnet.network`
- service `bridge` ingress → `bridge.svc.prd.use1.wardnet.network`

## Vanity domains

A service's `tls-termination` ingress entry may serve extra public names via its `vanity` list — see
[Service → Ingress](./service#hostnames-dns-and-certificates) for the templating rules
(`{BASE_DOMAIN}`, `{ENV}`, `{REGION_SLUG}`, and bare-token scoping). inforge creates a DNS record and an
ACME certificate entry for each.

## Provider requirements

The Cloudflare authority needs:

- `CLOUDFLARE_API_TOKEN` (the `providers.cloudflare.apiToken` credential) with permission to edit the
  zone
- the zone id in the region's `dns.zone` (see
  [regions.yaml → DNS authority](../configuration/regions-yaml#dns))

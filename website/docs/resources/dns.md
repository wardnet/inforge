---
sidebar_position: 3
---

# DNS

A **DNS** resource creates a DNS record in Cloudflare pointing at a Compute instance's public IP.

## Schema

```yaml
name: bridge             # required
container: bridge        # required
provider: cloudflare     # required
compute: bridge-01       # required — specKey of the Compute to point at
subdomain: bridge        # required — left part of the DNS record
proxied: false           # optional — enable Cloudflare proxy (default false)
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Resource name. |
| `container` | string | Yes | Grouping label. |
| `provider` | string | Yes | Must be `cloudflare`. |
| `compute` | string | Yes | specKey of the Compute this record points at. |
| `subdomain` | string | Yes | Left part of the hostname. Full name becomes `<subdomain>.<env>.<slug>.<baseDomain>`. |
| `proxied` | bool | No | Enable Cloudflare orange-cloud proxy (default `false`). |

## Resulting hostname

The DNS record name is assembled from `subdomain`, the environment, the region slug, and `base_domain`:

```
<subdomain>.<env>.<region-slug>.<base_domain>
```

For a record with `subdomain: bridge` in environment `prd`, region `us-east-1` (slug `use1`) with
`base_domain: example.com`, the record is `bridge.prd.use1.example.com`.

## Example

```yaml title="resources/prd/us-east-1/dns/bridge.yaml"
name: bridge
container: bridge
provider: cloudflare
compute: bridge-01
subdomain: bridge
proxied: false
```

## Provider requirements

The Cloudflare provider needs:

- `CLOUDFLARE_API_TOKEN` environment variable with permission to edit the zone
- `cloudflare.zoneId` in the region provider config under `variables.yaml`

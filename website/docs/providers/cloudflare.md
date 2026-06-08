---
sidebar_position: 2
---

# Cloudflare

The Cloudflare provider is the **DNS authority** inforge creates all [derived DNS
records](/resources/dns) on.

## Configuration

The authority's provider + zone live in each region's
[`dns`](/configuration/regions-yaml#dns-authority) block; the credentials and options live in the
matching `providers.cloudflare` block, both in [`regions.yaml`](/configuration/regions-yaml):

```yaml
regions:
  us-east-1:
    slug: use1
    dns:
      provider: cloudflare
      zone: abc123def456       # Cloudflare Zone ID every derived record is added to
    providers:
      cloudflare:
        apiToken: ${CLOUDFLARE_API_TOKEN}
        tagRecords: false      # optional, default true — see below
```

- `dns.zone` (required to create records) — the Cloudflare Zone ID to add records to.
- `tagRecords` (optional, default `true`) — whether inforge labels each DNS record with its resource
  tags. **DNS record tags are a Cloudflare Enterprise-only feature**; on Free/Pro/Business zones the
  API rejects them with error 9300 (`DNS record has N tags, exceeding the quota of 0`). Set
  `tagRecords: false` on a non-Enterprise zone.

## Required env vars

| Variable | Description |
|----------|-------------|
| `CLOUDFLARE_API_TOKEN` | Cloudflare API token with DNS edit permission for the zone |

## API token permissions

The API token needs **Zone → DNS → Edit** permission for the zone(s) inforge manages.

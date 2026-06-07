---
sidebar_position: 2
---

# Cloudflare

The Cloudflare provider implements **DNS** resources.

## Resources

| Resource | Status |
|----------|--------|
| DNS | Available |

## Configuration

Set in each region's `providers.cloudflare` block in [`regions.yaml`](/configuration/regions-yaml):

```yaml
regions:
  us-east-1:
    slug: use1
    providers:
      cloudflare:
        apiToken: ${CLOUDFLARE_API_TOKEN}
        zoneId: abc123def456   # Cloudflare Zone ID the DNS records are added to
        tagRecords: false      # optional, default true — see below
```

- `zoneId` (required) — the Cloudflare Zone ID to add records to.
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

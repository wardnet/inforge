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

Set in `variables.yaml` under `providers.cloudflare` (global) or per-region:

```yaml
providers:
  cloudflare:
    apiToken: ""   # set via CLOUDFLARE_API_TOKEN

regions:
  - name: us-east-1
    providers:
      cloudflare:
        zoneId: abc123def456   # Cloudflare Zone ID for this region's domain
```

The `zoneId` is required per region — it identifies which DNS zone to add records to.

## Required env vars

| Variable | Description |
|----------|-------------|
| `CLOUDFLARE_API_TOKEN` | Cloudflare API token with DNS edit permission for the zone |

## API token permissions

The API token needs **Zone → DNS → Edit** permission for the zone(s) inforge manages.

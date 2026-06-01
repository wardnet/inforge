---
sidebar_position: 1
---

# Hetzner Cloud

The Hetzner provider implements **Network** and **Compute** resources using
[Hetzner Cloud](https://www.hetzner.com/cloud).

## Resources

| Resource | Status |
|----------|--------|
| Network | Available |
| Compute | Available |

## Configuration

Set in `variables.yaml` under `providers.hetzner` (global default) or `regions[].providers.hetzner`
(per-region override):

```yaml
providers:
  hetzner:
    token: ""   # set via HCLOUD_TOKEN env var
```

The `token` field is typically left empty and supplied via the `HCLOUD_TOKEN` environment variable.

### Per-region overrides

```yaml
regions:
  - name: us-east-1
    providers:
      hetzner:
        location: ash   # Hetzner datacenter location slug
```

## Supported locations

| Location slug | Datacenter |
|--------------|------------|
| `ash` | Ashburn, VA (US East) |
| `hil` | Hillsboro, OR (US West) |
| `fsn1` | Falkenstein (EU West) |
| `nbg1` | Nuremberg (EU Central) |
| `hel1` | Helsinki (EU North) |
| `sin` | Singapore (AP South) |

## Server shapes

Hetzner servers are selected by the compute `size` field:

| Size | Hetzner type | vCPU | Memory |
|------|-------------|------|--------|
| `SMALL` | `cax11` | 2 | 4 GB |
| `MEDIUM` | `cax21` | 4 | 8 GB |
| `LARGE` | `cax31` | 8 | 16 GB |

These are Ampere ARM64 servers. Override sizes in `resources/<env>/sizes.yaml`.

## Firewall rules

inforge creates a firewall per container+region with:

- **Inbound**: TCP 22 (SSH), 80 (HTTP), 443 (HTTPS), 853 (DNS-over-TLS)
- **Outbound**: all TCP, UDP, ICMP

## Required env vars

| Variable | Description |
|----------|-------------|
| `HCLOUD_TOKEN` | Hetzner Cloud API token |

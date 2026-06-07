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

Hetzner configuration lives in each region's `providers.hetzner` block in
[`regions.yaml`](/configuration/regions-yaml): the API token plus that region's **realization**. A
realization is the complete concretization of the region — `location`, `network_zone`, the
`serverTypes` map (size name → server-type SKU) and the `images` map (canonical image → Hetzner image
id):

```yaml
regions:
  eu-central-1:
    slug: euc1
    providers:
      hetzner:
        apiToken: ${HCLOUD_TOKEN}      # supplied via the HCLOUD_TOKEN env var
        location: nbg1
        network_zone: eu-central
        serverTypes: {SMALL: cx23, MEDIUM: cx33, LARGE: cx43}
        images: {ubuntu-24.04: ubuntu-24.04}
```

Realizations are **fully explicit per region** — there are no built-in defaults and no inheritance.
Each region's block must list everything it needs; a `serverTypes`/`images` entry or a region absent
from the block fails the deploy with a clear, actionable error rather than at provider-apply time. Use
YAML anchors to de-duplicate values shared across regions.

This per-region split is deliberate: Hetzner server-type availability varies by location (for example
the Ampere ARM64 `cax*` types are not offered in `ash`), so each region pins the server types that
location actually offers.

## Supported locations

| Location slug | Datacenter | Network zone |
|--------------|------------|--------------|
| `fsn1` | Falkenstein (EU Central) | `eu-central` |
| `nbg1` | Nuremberg (EU Central) | `eu-central` |
| `hel1` | Helsinki (EU Central) | `eu-central` |
| `ash` | Ashburn, VA (US East) | `us-east` |
| `hil` | Hillsboro, OR (US West) | `us-west` |
| `sin` | Singapore (AP Southeast) | `ap-southeast` |

## Server types

The compute `size` name (`SMALL`/`MEDIUM`/`LARGE`, defined cloud-agnostically in the
[size table](/configuration/variables-yaml)) is mapped to a concrete Hetzner **server type** by each
region's `serverTypes`. There is no global default mapping — the size's meaning on Hetzner is whatever
that region declares. Common choices:

| Size | x86 (CX Gen3) | vCPU | Memory | ARM64 (`cax`, not in `ash`) |
|------|---------------|------|--------|------------------------------|
| `SMALL` | `cx23` | 2 | 4 GB | `cax11` |
| `MEDIUM` | `cx33` | 4 | 8 GB | `cax21` |
| `LARGE` | `cx43` | 8 | 16 GB | `cax31` |

The `sizes.yaml` table is just the set of valid size *names* — it carries no cpu/memory, and Hetzner
selects purely on the `serverTypes` mapping above.

## Firewall rules

inforge creates a firewall per compute spec (per region) with:

- **Inbound**: TCP 22 (SSH) is always allowed. Any rules a compute declares in its
  [`firewall`](/resources/compute) block are added on top; with no `firewall` block, SSH is the only
  inbound rule.
- **Outbound**: all TCP, UDP, ICMP

## Required env vars

| Variable | Description |
|----------|-------------|
| `HCLOUD_TOKEN` | Hetzner Cloud API token |

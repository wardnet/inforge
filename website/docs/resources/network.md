---
sidebar_position: 1
---

# Network

A **Network** resource defines a VPC and its subnets for a region. Networks must be created before
Compute resources that reference them.

A network resource lives in a folder under `regional/network/<name>/`:

```
regional/network/ingress/
  manifest.yaml       # required — the network spec
```

## Schema

`manifest.yaml`:

```yaml
name: ingress            # required — used as a foreign key by compute resources
container: ingress       # required — logical grouping label
provider: hetzner        # optional — inherits from inforge.yaml providers.compute if omitted
cidr: 10.0.0.0/16        # required — CIDR block for the network

subnets:                 # optional — list of subnets within the network
  - name: main
    cidr: 10.0.1.0/24
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Resource name. Used as the foreign key in compute specs. |
| `container` | string | Yes | Grouping label. Used in URNs and tags. |
| `provider` | string | No | Must be `hetzner`. Inherits from `inforge.yaml` `providers.compute` if omitted. |
| `cidr` | string | Yes | CIDR block for the network. |
| `subnets` | array | No | List of subnets. Each entry requires `name` and `cidr`. |

## Example

```yaml title="regional/network/ingress/manifest.yaml"
name: ingress
container: ingress
provider: hetzner
cidr: 10.0.0.0/16
subnets:
  - name: main
    cidr: 10.0.1.0/24
  - name: db
    cidr: 10.0.2.0/24
```

## Outputs

For each subnet the Hetzner network provider returns:

| Output | Description |
|--------|-------------|
| `networkID` | Hetzner network ID (referenced internally by compute) |
| `subnetID` | Subnet ID |

Compute resources reference their network by name. When a network has multiple subnets, set
`subnet` to the desired subnet name; otherwise the first declared subnet is used:

```yaml
# in compute/bridge.yaml
network: ingress
subnet: main    # optional when there is only one subnet
```

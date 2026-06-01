---
sidebar_position: 1
---

# Network

A **Network** resource defines a VPC or subnet for a region. Networks must be created before
Compute resources that reference them.

## Schema

```yaml
name: ingress            # required — combined with `instance` to form specKey ingress-01
instance: 1              # required — 1-based index; padded to two digits
container: ingress       # required — logical grouping label
provider: hetzner        # required — only "hetzner" is currently supported

type: public             # optional — "public" (default) or "private"
cidr: 10.0.0.0/16        # required for private networks
subnet_cidr: 10.0.1.0/24 # required for private networks
parent: ""               # optional — specKey of parent network (for private sub-networks)
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Resource name. Combined with `instance` to form the specKey. |
| `instance` | int | Yes | Instance number (1-based). Forms the `name-NN` specKey. |
| `container` | string | Yes | Grouping label. Used in URNs and tags. |
| `provider` | string | Yes | Must be `hetzner`. |
| `type` | string | No | `public` (default) or `private`. |
| `cidr` | string | Conditional | CIDR block for the network. Required when `type: private`. |
| `subnet_cidr` | string | Conditional | Subnet CIDR within the network. Required when `type: private`. |
| `parent` | string | No | specKey of a parent network (for nested private networks). |

## Example

```yaml title="resources/prd/us-east-1/network/ingress-01.yaml"
name: ingress
instance: 1
container: ingress
provider: hetzner
type: private
cidr: 10.0.0.0/16
subnet_cidr: 10.0.1.0/24
```

## Outputs

The Hetzner network provider returns:

| Output | Description |
|--------|-------------|
| `networkID` | Hetzner network ID (referenced internally by compute) |
| `subnetID` | Subnet ID |

Compute resources reference their network by specKey:

```yaml
# in compute/bridge-01.yaml
network: ingress-01
```

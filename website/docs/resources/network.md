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
| `container` | string | Yes | Grouping label. Used in URNs and tags. Networks sharing a container are realized as **one** cloud network — see below. |
| `provider` | string | No | Must be `hetzner`. Inherits from `inforge.yaml` `providers.compute` if omitted. |
| `cidr` | string | Yes | CIDR block for the network. |
| `subnets` | array | No | List of subnets. Each entry requires `name` and `cidr`. Subnet names must be unique across the scope — see below. |

## Collision rules

`inforge validate` rejects two authoring mistakes that would otherwise collide at deploy time:

- **Subnet names are unique across a scope's networks.** A subnet's cloud resource name is
  `wardnet-<env>-<slug>-subnet-<subnet name>` — it does **not** include the owning network — so two
  networks of the same scope declaring a subnet called `main` would resolve to one resource and abort
  the deploy. Give each subnet a distinct name (`ingress-main`, `internal-main`), including across the
  networks of a scope. The same name declared twice within one network is rejected for the same reason.
  A regional and a global network may reuse a name (their scopes carry different slugs).

- **Networks sharing a `container` must declare the same `cidr`.** `container` is the grouping key the
  cloud network is created under: every network spec with the same container in the same scope shares a
  single cloud network, created with the CIDR of the first spec. A second spec that declares a
  *different* CIDR is an error — its subnets would land in a network whose IP range does not cover them
  (and the derived private firewall rules would be written from a CIDR no host actually holds). Either
  make the CIDRs agree, or split the networks into separate containers.

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
# in regional/compute/bridge/manifest.yaml
network: ingress
subnet: main    # optional when there is only one subnet
```

---
sidebar_position: 2
---

# Resources Overview

inforge defines seven resource types. Each resource is a single YAML file under
`resources/<env>/<type>/`. The set is defined once per environment and instantiated into every region
in `regions.yaml`. All files are validated against embedded JSON schemas.

## Resource types

| Type | Directory | Description |
|------|-----------|-------------|
| [Network](../resources/network) | `network/` | VPC / network (Hetzner) |
| [Compute](../resources/compute) | `compute/` | Virtual machine |
| [Database](../resources/database) | `database/` | Managed PostgreSQL (Neon) |
| [Secrets](../resources/secrets) | `secrets/` | Secret references (Infisical) |
| [Service](../resources/service) | `service/` | Application hosted on a VM (with typed nginx ingress) |

[DNS](../resources/dns) is **not** an authored resource type: inforge derives every record (host,
service, vanity) automatically and creates it on the region's
[DNS authority](../configuration/regions-yaml#dns).

## Common fields

Every resource has these required fields:

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Resource name. Combined with `instance` to form the specKey. |
| `container` | string | Logical grouping label (e.g. `bridge`, `ingress`). Used in URNs and tags. |
| `provider` | string | Provider name (`hetzner`, `cloudflare`, `neon`, `infisical`). |

:::info Container vs container runtime
`container` is a grouping label, **not** a Docker/OCI container. Do not confuse it with
a service delivery `type: container` (a reserved delivery mode for future pull-based deployments).
:::

## specKey

A resource instance's identity is its **specKey**: `<name>-<NN>` zero-padded (e.g. `bridge-01`).

For a Compute with `name: bridge` and `instance_count: 2`, inforge expands it into `bridge-01`
and `bridge-02`. Other resources reference compute instances using their specKey as a foreign key.

## The global slice

Alongside the regional set, an environment may define a **global slice** under
`resources/<env>/global/` — resources that deploy **once**, region-less, instead of into every region.
A regional secret may reference a global database or compute output; that is the one allowed
cross-region reference. See [Global resources](./global-resources) for the naming, the `regions.yaml`
`global:` block, and the cross-reference rules.

## Validation

Run validation before every PR:

```bash
inforge validate prd
```

This checks every YAML file against the embedded JSON schemas, reports each file
that fails, and exits non-zero if any file is invalid.

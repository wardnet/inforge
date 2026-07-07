---
sidebar_position: 2
---

# Resources Overview

inforge defines four resource types. Each resource is a **named folder** under
`resources/<env>/regional/<type>/<name>/` containing a `manifest.yaml` validated against an embedded
JSON schema. Optional sidecar files (cloud-init scripts, environment variable maps) live alongside
the manifest in the same folder. The set is defined once per environment and instantiated into every
region in `regions.yaml`.

## Resource types

| Type | Directory | Description |
|------|-----------|-------------|
| [Network](../resources/network) | `regional/network/<name>/` | VPC / network (Hetzner) |
| [Compute](../resources/compute) | `regional/compute/<name>/` | Virtual machine |
| [Database cluster](../resources/database-cluster) | `regional/database-cluster/<name>/` | A self-hosted PostgreSQL engine on a compute host |
| [Database](../resources/database) | `regional/database/<name>/` | A logical PostgreSQL database inside a cluster |
| [Service](../resources/service) | `regional/service/<name>/` | Application hosted on a VM, reached over the east-west mesh and optionally exposed north-south through an ingress |
| [Ingress](../resources/ingress) | `regional/ingress/<name>/` | Shared nginx proxy tier (referencing a compute host) that fronts apps and per-service web/SNI routes |
| [Gateway](../resources/gateway) | `regional/gateway/<name>/` | North-south public edge external daemons HTTPS into; routes each path to a service through the mesh |
| [App](../resources/app) | `regional/app/<name>/` | Static front-end (SPA) served from disk by an ingress's nginx |
| [PKI resource](../resources/pki-resource) | `regional/pki/<name>/` | Standalone root-only CA, consumed by a service through a grant |

[DNS](../resources/dns) is **not** an authored resource type: inforge derives every record (host,
service, vanity) automatically and creates it on the region's
[DNS authority](../configuration/regions-yaml#dns).

[Secrets/environment variables](../resources/secrets) are **not** a standalone resource either: a
service declares the runtime values it needs in a sidecar `environment.yaml` in its folder. There is
no dedicated `secrets/` directory.

## Common fields

Every resource manifest has these required fields:

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Resource name. Should match the folder name by convention. |
| `container` | string | Logical grouping label (e.g. `bridge`, `ingress`). Used in URNs and tags. |
| `provider` | string | Provider name (`hetzner`, `cloudflare`). Optional when a project-level default is set in `inforge.yaml`; an explicit value always takes precedence. A **service** has no `provider` — it is host-managed. |

:::info Container vs container runtime
`container` is a grouping label, **not** a Docker/OCI container. Do not confuse it with
a service delivery `type: container` (a reserved delivery mode for future pull-based deployments).
:::

## specKey

A compute resource's instances have internal identities — **specKeys**: `<name>-<NN>` zero-padded
(e.g. `bridge-01`). For a Compute with `name: bridge` and `instance_count: 2`, inforge expands it
into `bridge-01` and `bridge-02`. specKeys are used internally in derived names (DNS, display) and
are **not written in resource specs** — foreign references (e.g. `service.host`) use the bare
resource `name`.

## The global slice

Alongside the `regional/` set, an environment may define a **global slice** under
`resources/<env>/global/` — resources that deploy **once**, region-less, instead of into every
region. Each global resource is also a named folder (`global/<type>/<name>/manifest.yaml`). A
regional service's `environment.yaml` may reference a global database or compute output; that is the
one allowed cross-region reference. See [Global resources](./global-resources) for the naming, the
`regions.yaml` `global:` block (including the required `placementRegion`), and the cross-reference
rules.

## Validation

Run validation before every PR:

```bash
inforge validate prd
```

This checks every YAML file against the embedded JSON schemas, reports each file
that fails, and exits non-zero if any file is invalid.

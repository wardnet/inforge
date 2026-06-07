---
sidebar_position: 1
---

# Environments & Regions

## Environment

An **environment** is the top-level deployment scope (e.g. `prd`, `dev`, `staging`). Every
inforge command takes exactly one environment. inforge never acts on multiple environments in
a single invocation.

Environments map to directories under `resources/`:

```
resources/prd/     # the "prd" environment
resources/dev/     # the "dev" environment
```

Each environment has its own `variables.yaml`, its own `regions.yaml`, its own Pulumi stack, and its
own state.

## Region target

A **region target** is an abstract region an environment deploys into, declared in
[`regions.yaml`](/configuration/regions-yaml) under `regions:`. Each entry has a name (e.g.
`us-east-1`), a `slug`, and a `providers` block (credentials + that region's realization). The set of
keys under `regions:` *is* the set of regions the environment deploys into.

Resource files live under `resources/<env>/<region>/`:

```
resources/prd/us-east-1/compute/bridge-01.yaml
resources/prd/eu-west-1/compute/bridge-01.yaml
```

## Region slug

A **region slug** is the short location code an abstract region maps to (e.g. `us-east-1` → `use1`).
Slugs are used in display names, DNS subdomains, and resource URNs.

The mapping is defined per environment in [`regions.yaml`](/configuration/regions-yaml), alongside
each region's provider config. Built-in slugs in `internal/regions` provide the naming vocabulary.

## Display name

The canonical resource name format is `wardnet-<env>-<resourceType>-<slug>-<specKey>`.

Example: `wardnet-prd-compute-use1-bridge-01`

## Region table

The per-environment `regions.yaml` is the region table: it maps each abstract region to its `slug`
**and** its `providers` block (where the region physically lives on each cloud — its
[realization](/providers/hetzner) — plus credentials). The built-in slugs in `internal/regions` are
naming vocabulary only:

| Abstract region | Slug |
|----------------|------|
| us-east-1      | use1 |
| us-west-1      | usw1 |
| eu-central-1   | euc1 |
| ap-east-1      | ape1 |

Define the regions an environment deploys into in `resources/<env>/regions.yaml`:

```yaml
regions:
  us-east-1:
    slug: use1
    providers:
      hetzner: { apiToken: "${HCLOUD_TOKEN}", location: ash, network_zone: us-east }
  eu-central-1:
    slug: euc1
    providers:
      hetzner: { apiToken: "${HCLOUD_TOKEN}", location: nbg1, network_zone: eu-central }
```

:::caution
`regions.yaml` is the whole authority — there is no default fallback. An environment with no
`regions.yaml` (or an empty `regions:` map) deploys nothing.
:::

See [regions.yaml](/configuration/regions-yaml) for the full reference.

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

Each environment has its own `variables.yaml`, its own Pulumi stack, and its own state.

## Region target

A **region target** is an abstract region an environment deploys into, declared in
`variables.yaml` under `regions[]`. Each region target has a name (e.g. `us-east-1`) and
per-region provider overrides.

Resource files live under `resources/<env>/<region>/`:

```
resources/prd/us-east-1/compute/bridge-01.yaml
resources/prd/eu-west-1/compute/bridge-01.yaml
```

## Region slug

A **region slug** is the short location code an abstract region maps to (e.g. `us-east-1` → `use1`).
Slugs are used in display names, DNS subdomains, and resource URNs.

The mapping is defined in the built-in **region table** (`internal/regions`). You can override it
with a `resources/<env>/regions.yaml` file that **replaces** the default table entirely.

## Display name

The canonical resource name format is `wardnet-<env>-<resourceType>-<slug>-<specKey>`.

Example: `wardnet-prd-compute-use1-bridge-01`

## Region table

The abstract-region → slug map. Built-in defaults:

| Abstract region | Slug |
|----------------|------|
| us-east-1      | use1 |
| eu-west-1      | euw1 |
| ap-southeast-1 | apse1 |

Override by placing `regions.yaml` in `resources/<env>/`:

```yaml
- abstract: us-east-1
  slug: use1
- abstract: eu-central-1
  slug: euc1
```

:::caution
The override **replaces** the entire default table, not merges with it.
:::

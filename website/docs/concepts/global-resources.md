---
sidebar_position: 3
---

# Global resources

Most of an environment's resources are **regional**: defined once and instantiated into every region
listed in `regions.yaml` (see [Resources overview](./resources-overview)). Some resources, though,
should exist **once** — a database every region reads, a host with no regional twin. Those go in the
**global slice**.

The global slice is not a new resource kind. It is a reserved, region-less **scope**: the same resource
types, the same schemas, the same providers — deployed once instead of per region.

## On-disk shape

The global slice lives under `resources/<env>/global/`, mirroring the regional type directories.
Each resource is a named folder containing `manifest.yaml`, same as regional resources:

```
resources/<env>/
  variables.yaml
  regions.yaml
  regional/
    network/ compute/ database/ service/   # the regional set (per region)
  global/
    network/<name>/manifest.yaml           # the global slice (once)
    compute/<name>/manifest.yaml
    database/<name>/manifest.yaml
    service/<name>/manifest.yaml
            environment.yaml
```

The slice is **optional** — an environment with no `global/` directory deploys nothing globally.

## Provider config: the `global:` block

A global resource realizes against a top-level `global:` block in `regions.yaml`. It carries a
required **`placementRegion`** and a `providers` block. There is **no `slug`** — global resources are
region-less — but `placementRegion` names the region whose provider credentials and realizations will
be used when deploying global resources (validated now; runtime wiring in a follow-up):

```yaml title="resources/prd/regions.yaml"
regions:
  us-east-1:
    slug: use1
    providers:
      neon: { apiKey: ${NEON_API_KEY} }
global:
  placementRegion: us-east-1    # required — must match a key under regions:
  providers:
    # A global Neon database has no abstract region to map to a physical Neon
    # region, so the global block gives it explicitly. (Regional blocks don't
    # need this — their abstract region maps to a Neon region automatically.)
    neon: { apiKey: ${NEON_API_KEY}, region: aws-us-east-2 }
```

`placementRegion` resolves provider-registration lookups only. It does not affect global resource
names (no slug is inserted). Omitting it when a `global:` block is present is a validation error.
See [ADR-0023](https://github.com/wardnet/inforge/blob/main/docs/adr/0023-global-placement-region.md).

## Region-less naming

Regional resources carry the region slug in their cloud name
(`wardnet-<env>-<slug>-<type>-<name>`). Global resources drop it:

| Scope | Example name |
|-------|--------------|
| Regional | `wardnet-prd-use1-db-bridge` |
| Global | `wardnet-prd-db-shared` |

The global slice is created **before** any region, so its outputs are available when a regional
resource references them.

## Cross-reference rules

References between scopes are narrow and **direction-enforced**.

### Allowed: a regional secret → a global database/compute output

A regional [service secret](../resources/secrets) may resolve a global database or compute output by
prefixing the referenced name with `global/`:

```yaml title="regional/service/app/environment.yaml"
DATABASE_URL: ref:database/global/shared.connectionUrl   # the global database
```

This is the **one** cross-region path: a service in any region reads a database (or compute IP) that
lives once, globally. The reference resolves against the global slice regardless of the consuming
service's region.

### Rejected

| Reference | Why |
|-----------|-----|
| `service.host: global/<name>` | A service that runs on a global host is defined **in the global slice itself**, not referenced from a region. |
| `compute.network: global/<name>` | Recognized, but cross-region networking is not trivial to materialize and is **not supported yet**. |
| A **global** resource referencing a **regional** one | Within the global slice, a global resource may reference only other global resources. The global slice is validated in a global-only context, so a regional name is simply not found. |

`inforge validate` enforces all three with explicit messages.

## What realizes today

The global slice realizes the **same resource types as a region**, through the same pipeline — just
once, with region-less names:

- the referenceable infrastructure: **network**, **compute**, **database**;
- **ingress** tiers (nginx + ACME) and **service** host provisioning (the systemd unit, runtime
  secrets, and the mesh leaf from the service's `pki:` membership);
- the derived **DNS records** and ACME certificates — the host record `<compute>.vm.<env>.<base>`,
  the service record `<svc>.svc.<env>.<base>`, and any route `vanity` — written into the DNS
  authority of the `placementRegion` (`regions.<placementRegion>.dns`). Global records carry no slug,
  so they never collide with the slug-bearing regional records.

Because a global slice realizes DNS and ACME against the placement region's authority, `inforge
validate` **rejects** a global slice with any compute (every host derives a `.vm` record) when the
`placementRegion` has no `dns:` block; `inforge deploy` re-checks and fails fast if validate was
skipped. Validation also enforces the full per-type rules (e.g. a global service host must declare a
`deploy_user`).

:::note
The derived host/service records are env-scoped and region-less, so they never collide with the
slug-bearing regional records. One case a shared zone still allows: a **literal** vanity/apex FQDN
(e.g. `account.<base>`) declared identically on both a global service and a service in the placement
region — operator-avoidable and not yet validated.
:::

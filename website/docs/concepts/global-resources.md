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

The global slice lives under `resources/<env>/global/`, mirroring the regional type directories:

```
resources/<env>/
  variables.yaml
  regions.yaml
  network/ compute/ database/ secrets/ service/   # the regional set (per region)
  global/
    network/ compute/ database/ secrets/ service/ # the global slice (once)
```

The slice is **optional** — an environment with no `global/` directory deploys nothing globally.

## Provider config: the `global:` block

A global resource realizes against a sibling top-level `global:` block in `regions.yaml`. It carries
provider config only — **no slug**, because global resources are region-less:

```yaml title="resources/prd/regions.yaml"
regions:
  us-east-1:
    slug: use1
    providers:
      neon: { apiKey: ${NEON_API_KEY} }
      infisical: { clientId: ${INFISICAL_CLIENT_ID}, clientSecret: ${INFISICAL_CLIENT_SECRET} }
global:                       # no slug — region-less
  providers:
    # A global Neon database has no abstract region to map to a physical Neon
    # region, so the global block gives it explicitly. (Regional blocks don't
    # need this — their abstract region maps to a Neon region automatically.)
    neon: { apiKey: ${NEON_API_KEY}, region: aws-us-east-2 }
```

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

A regional [secret](../resources/secrets) may resolve a global database or compute output by prefixing
the referenced name with `global/`:

```yaml title="resources/prd/secrets/app.yaml"
name: app
container: app
provider: infisical
secrets:
  DATABASE_URL:
    source: ref:database/global/shared.connectionUrl   # the global database
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

This slice realizes the global **network**, **compute**, and **database** resources (the referenceable
outputs). Global **service** resources are loaded and validated, but their host-level provisioning is
not wired yet — only the output-producing types deploy globally for now. Note that validation still
enforces the **full** rules for these types (e.g. a global service host must declare a `deploy_user`),
even though they do not deploy yet.

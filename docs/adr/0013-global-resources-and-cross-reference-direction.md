# Global resources and a direction-enforced cross-reference

Builds on [ADR-0012](0012-single-resource-definition-multi-region-instantiation.md) and
[ADR-0011](0011-regions-yaml-region-and-provider-authority.md).

ADR-0012 made an environment's regional resource set a single definition instantiated into every
region. Some resources, though, should exist **once**, region-less — a database other regions read, a
host that has no regional twin. ADR-0012 reserved a `global/` slot for exactly this case; this ADR
realizes it and fixes the rules under which a regional resource may reference a global one.

We decided:

- **A `global/` slice.** `resources/<env>/global/{network,compute,dns,database,secrets,service,
  tls-termination}` holds the globally-deployed set. It is instantiated **once**, before any region,
  with **region-less naming**: `wardnet-<env>-<type>-<name>` (no slug segment). Its provider config is
  a sibling top-level `global:` block in `regions.yaml` (providers only — no slug).
- **One allowed cross-reference, by direction.** A **regional** secrets `ref:` may target a **global**
  database or compute output. The syntax is a `global/` path prefix on the referenced name:
  `ref:database/global/<name>.connectionUrl`, `ref:compute/global/<name>.publicIp`. This is the single
  cross-region path that must work (a service in one region reading a once-deployed global database).
- **Everything else is rejected, explicitly.**
  - `service.host: global/<name>` — a service that runs on a global host is defined in the global
    slice itself, not referenced from a region.
  - `compute.network: global/<name>` — recognized syntactically but rejected: materializing
    cross-region networking is not trivial and is not supported yet.
  - Within the global slice, a global resource may reference **only** other global resources; a global
    resource pointing at a regional one fails as not-found.

## Considered options

- **A special "global" resource kind.** Rejected: global is not a new kind but a **scope**. Modelling
  it as a reserved region slot (`global/`, empty slug) reuses the entire existing resource model, schema
  set, and provider pipeline — global compute is still `compute`, validated by the same schema.
- **Region-to-region references in general.** Rejected for now. The only cross-region need today is
  reading a globally-deployed output, so we permit exactly that one direction (regional → global
  db/compute) and reject the rest with clear messages rather than shipping a general cross-region
  reference resolver whose materialization (especially networking) is unsolved.
- **Allow `service.host` / `compute.network` to point at global resources.** Rejected: a global-hosted
  service belongs in the global slice (define it there), and cross-region networking has no
  realization yet. Recognizing the `global/` prefix and failing with a specific message is clearer than
  a generic "not found".

## Consequences

- **Naming.** `naming.Resource`, `naming.ResourceInstance`, and `naming.RecordName` treat an **empty
  slug** as the global scope, dropping the region segment (`wardnet-<env>-<type>-<name>`,
  `…-<NN>`, `<subdomain>.<env>`). Regional callers always pass a non-empty slug — validated in
  `checkRegionsFile` — so the regional path is never silently globalised.
- **Loader.** `loader.LoadGlobalResources` reads `resources/<env>/global/` into a separate
  `types.Resources`. The regional `LoadResources` walks named type directories and never lists base
  children, so `global/` does not leak into the regional set. The slice is optional (absent → empty).
- **Program.** `program.Run` creates the global slice **first** — network/compute/database into the
  reserved `"global"` output slot, via a registry built from the `global:` providers with an empty slug
  — then instantiates the regional set per region. Because the global outputs are present before any
  region's secrets resolve, a regional `ref:database/global/<name>` resolves against
  `all.Database["global"]`. The global slice's service/dns/tls-termination resources are loaded and
  validated but **not provisioned** this slice (only network/compute/database realize globally).
- **Validation.** Two contexts. The global slice is validated in a **global-only** context, so a global
  resource referencing a regional one fails as not-found (enforcing "global → global only"). The
  regional set is validated with the global database/compute names injected under a `global/` prefix, so
  a regional secrets `ref:` to a global output resolves. `service.host: global/…` and
  `compute.network: global/…` are rejected with explicit messages. Provider availability is checked per
  region for the regional set and against the `global:` block for the global slice.
- **Secrets resolution.** `providers/infisical` `resolveRef` strips a `global/` prefix and resolves
  against the `"global"` slot regardless of the consuming service's region.
- **Neon physical region (new config surface).** The Neon provider previously derived a project's
  physical region from a hardcoded abstract-region → Neon-region map. A global database has **no**
  abstract region to map, so the `global:` block's `providers.neon.region` (a Neon region id, e.g.
  `aws-us-east-2`) now supplies it. The key is **optional and config-first**: when set it overrides the
  map (aligning Neon with ADR-0011's "regions.yaml is the realization authority"); when empty the
  abstract-region map still applies, so **regional Neon behavior is unchanged**. This is a new
  user-facing config key the original Decisions did not enumerate; it is forced (config is the only
  possible source of a region-less project's location), but is called out here for visibility.
- The `wardnet-infrastructure` consumer is controlled end-to-end; the global slice is additive (no
  global slice → no behavior change). Ships as **v1.1.0** (with ADR-0011 and ADR-0012).

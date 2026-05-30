# Compute cpus/memory are resolved from the size table, not declared per compute

A compute resource declares only a `size` (e.g. `SMALL`); its `cpus`/`memory` are resolved from the
size table at build time and are no longer fields on the spec. This deviates from the obvious
"declare cpus and memory inline" model (which the TypeScript source had alongside `size`, redundantly)
so that sizing is centralized, consistent across services, and tunable per environment via
`sizes.yaml` without touching every compute file. `size` is an open string validated against the
resolved table, so projects can define their own tiers.

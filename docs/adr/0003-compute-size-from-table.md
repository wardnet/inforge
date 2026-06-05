# Compute cpus/memory are resolved from the size table, not declared per compute

> **Amended by [ADR-0009](./0009-provider-centric-config-region-realizations.md):** the size table no
> longer carries `cpus`/`memory`. A size is now just a validated *name*; each provider maps that name
> to a concrete SKU via its per-region realization (`providers.<name>.regions[…].serverTypes`). The
> decision below to keep sizing out of the compute spec — declared as a `size` name, validated against
> the size table — still stands; only the table's payload changed.

A compute resource declares only a `size` (e.g. `SMALL`); its `cpus`/`memory` are resolved from the
size table at build time and are no longer fields on the spec. This deviates from the obvious
"declare cpus and memory inline" model (which the TypeScript source had alongside `size`, redundantly)
so that sizing is centralized, consistent across services, and tunable per environment via
`sizes.yaml` without touching every compute file. `size` is an open string validated against the
resolved table, so projects can define their own tiers.

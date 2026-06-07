# Resources are defined once per environment and instantiated into every region

Amends [ADR-0001](0001-one-environment-env-region-layout.md).

ADR-0001 partitioned resource definitions by environment **then region**
(`resources/<env>/<region>/<type>/`). In practice an environment's regional infrastructure is meant
to be identical across regions — the only legitimate per-region difference is the provider realization
(location, server types, credentials), which [ADR-0011](0011-regions-yaml-region-and-provider-authority.md)
already moved entirely into `regions.yaml`. The per-region directories therefore forced operators to
copy-paste the same specs once per region, inviting drift between regions that were supposed to match.

We decided to define the resource set **once** per environment, directly under
`resources/<env>/{network,compute,dns,database,secrets,service,tls-termination}`, and instantiate it
into **every region** listed in `regions.yaml`. There are no per-region resource directories. The
region slug is already part of every cloud resource name
(`wardnet-<env>-<slug>-<type>-<name>`), so the same spec yields distinct, non-colliding instances per
region (`wardnet-prd-use1-vm-bridge`, `wardnet-prd-euc1-vm-bridge`, …). Which regions a set deploys
into is, as of ADR-0011, the set of keys under `regions:`.

## Considered options

- **Keep ADR-0001's per-region directories.** Rejected: the copy-paste duplication and the resulting
  cross-region drift are exactly what this change removes. With ADR-0011 the realization already lives
  in `regions.yaml`, so the per-region resource dirs carried only duplicated, identical specs.
- **A per-region overlay/patch on a shared base.** Rejected as premature: no current consumer needs a
  region to differ from the shared set beyond its realization, and an overlay mechanism adds
  resolution complexity (merge semantics, ordering) for a need that does not yet exist. The reserved
  `global/` slot (a later slice) covers the one known non-uniform case — resources that deploy once,
  region-less — without overlays.
- **Define once, instantiate per region** (chosen). One definition, no drift, and per-region
  uniqueness comes for free from the slug already in every name.

## Consequences

- `internal/loader.LoadResources` returns a single `types.Resources` read from `resources/<env>/`
  (no longer a `map[region]Resources`); `RegionDirs` is removed. `cloud_init` resolves against the
  single `compute/` directory.
- `program.Run` iterates the regions from the `regions.yaml` table (sorted for determinism) and
  instantiates the **same** resource set into each. The output maps stay keyed by region
  (`computeOutputs[region][key]`, …) so per-region outputs remain distinct.
- `internal/validate` validates the shared set **once** — schema plus the region-independent
  foreign-key graph — then checks provider availability **per region**: because the same set deploys
  everywhere, each resource's `provider:` must be declared in *every* region's `providers` block, and
  a provider missing from any region fails for that region. The old "region directory must match a
  declared region" check is gone (region selection no longer comes from on-disk directories).
- `internal/service.BuildDeployDescriptor` takes the single set plus the region table and expands each
  service into one `DeployTarget` **per region** (region-specific host DNS via the slug). A
  single-region environment produces output identical to before; a multi-region one fans each service
  out across regions.
- `cmd/inforge matrix` is unaffected: it still derives the environment from `parts[1]` of a
  `resources/<env>/…` path.
- The `wardnet-infrastructure` consumer is controlled end-to-end, so its on-disk layout migrates to
  the single-definition shape without a back-compat shim. Ships as **v1.1.0** (with ADR-0011).

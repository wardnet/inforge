# regions.yaml is the authority for which regions deploy and all provider config

Supersedes [ADR-0009](0009-provider-centric-config-region-realizations.md).

Under ADR-0009 an environment's configuration was split across two files whose boundary was a source
of confusion: `variables.yaml` declared *which* regions deployed (`regions[]`) **and** owned all
provider config under `providers.<name>` (credentials plus a region-keyed map of realizations), while
`regions.yaml` was an optional, cloud-agnostic abstract-region → slug table. The same region name
therefore appeared in three places — `variables.yaml regions[]`, `variables.yaml
providers.<name>.regions.<region>`, and the slug table — and a region could be listed for deploy
without a realization, or realized without being listed, with the mismatch only surfacing later.

We decided to make **`regions.yaml` the single authority** for an environment's regions. Each entry
under the top-level `regions:` key carries that region's `slug` **and** its `providers` block —
credentials and the region's realization for every provider, in one place. The set of keys under
`regions:` *is* the set of regions the environment deploys into; there is no separate selector.
`variables.yaml` shrinks to `base_domain` + `ssh`.

A sibling top-level `global:` key holds region-less provider config for the global slice (resources
under `resources/<env>/global/`, realized with region-less naming). It is parsed and validated here
but consumed by a later slice. `global` has no `slug` — keeping it a sibling of `regions:` (rather
than a reserved entry inside the map) makes its distinction structural: it runs only the global slice
and is named without a region.

## Considered options

- **Keep the ADR-0009 split** (regions selected in `variables.yaml`, realized under
  `providers.<name>.regions`). Rejected: the three-places duplication and the silent
  selected-but-not-realized / realized-but-not-selected failure modes are exactly what motivated this
  change.
- **A reserved `global` entry inside the `regions:` map.** Rejected: global has no slug and does not
  run the shared regional set, so a magic map key would need special-casing throughout; a sibling key
  expresses the difference in the file's shape.
- **`regions.yaml` owns regions + providers; sibling `global:` key** (chosen). One home per region,
  one authority for the deploy set, and a structurally distinct global slot.

## Consequences

- `variables.yaml` is now just `{base_domain, ssh}`. The `regions[]` selector and the
  `providers` block are gone from it.
- A region's `providers.<name>` block holds that region's realization fields **directly** (for
  Hetzner: `location`, `network_zone`, `serverTypes`, `images`) — there is no nested
  `providers.hetzner.regions.<region>` map, because the enclosing region entry already names the
  region. `hetzner.ExtractRegionConfigs` reads the single region's block.
- Region iteration in the Pulumi program comes from the `regions.yaml` keys, not a `variables.yaml`
  list. An absent `regions.yaml` yields an empty table — the environment deploys nothing — rather
  than falling back to the built-in slug table (which carries no provider config and so could not
  deploy anyway). `regions.DefaultTable()` remains as naming vocabulary.
- Provider availability in `internal/validate` is now **per region** (a resource's `provider:` must be
  declared in *its* region's `providers` block), and `validate` gains a `regions.yaml` check (each
  region needs a `slug` and a non-empty `providers` block; a `global` block, when present, needs
  `providers`). The fully-explicit / no-inheritance / enforce-at-the-provider-boundary decisions from
  ADR-0009 are unchanged.
- The `wardnet-infrastructure` consumer is controlled end-to-end, so the config shape changes without
  a back-compat shim. Ships as **v1.1.0**.

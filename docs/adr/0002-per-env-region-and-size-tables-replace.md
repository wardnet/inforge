# Per-environment region and size tables replace the built-in defaults

The abstract-region→slug map (region table) and the size→cpu/memory map (size table) ship as built-in
defaults in `internal/regions` and `internal/sizes`. A project may override them per environment with
`resources/<env>/regions.yaml` / `sizes.yaml`, but when such a file is present it **replaces** the
defaults wholesale rather than merging. Replace (not merge) was chosen so an environment's resolved
tables are exactly what its files say — predictable and auditable — with no surprise inheritance from
defaults a reader can't see in the file.

## Consequences
An override file must be complete: omitting a size/region that compute or region-target files
reference will fail validation. This is intended.

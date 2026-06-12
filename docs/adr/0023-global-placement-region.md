# `regions.yaml` global block requires a `placementRegion`

The `global:` block in `regions.yaml` carries a `providers` map used to realise global
resources (those under `resources/<env>/global/`). Global resources have region-less names
(no slug) and are created once for the environment. The Pulumi program must look up provider
credentials and realizations for the global slice, but the `global:` block had no `slug` and
no explicit pointer to which region's provider config to use. The program resolved this by
using a hard-coded fallback or the first available region — both implicit and surprising.

## Decisions

- **`global:` gains a required `placementRegion` field** that names one of the abstract
  regions declared under `regions:`:
  ```yaml
  global:
    placementRegion: us-east-1   # must match a key under regions:
    providers:
      neon:
        apiKey: ${NEON_API_KEY}
  ```
- **`placementRegion` resolves the provider-registration lookup only.** Global resources
  retain region-less names — `wardnet-<env>-<type>-<name>`, no slug. The slug from
  `placementRegion` is used internally to find the right provider credentials and realization
  block, not to construct resource names.
- **`inforge validate` reports an error if `placementRegion` is absent from a `global:` block**
  or if its value is not a key declared under `regions:`.
- **An absent `global:` block** means the environment has no global resources and is not
  affected by this change.
- **`placementRegion` is not intended to move resources.** Changing it after initial deploy
  leaves existing global resources in place (they are identified by their region-less Pulumi
  URN); only new provider lookups change. Changing it to a region with a different provider
  type for a given resource class would be a breaking change and is not supported.

## Considered alternatives

**Derive the placement region from the first entry under `regions:`.** Map iteration order
in Go is not guaranteed; silently picking "the first region" was the source of the original
ambiguity. Rejected.

**Allow the global providers block to override slug-based lookups independently.** The global
block already has its own `providers:` map; extending it to also carry a full realization
(location, serverTypes, etc.) would duplicate regions.yaml. `placementRegion` delegates to
an existing, fully specified region entry. Accepted.

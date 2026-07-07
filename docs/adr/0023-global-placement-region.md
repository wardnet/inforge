---
status: accepted
date: 2026-06-12
issue: "#96"
---

# `regions.yaml` global block requires a `placementRegion`

> _Note: the `neon`/`NEON_API_KEY` provider example below is retired ([ADR-0036](0036-self-hosted-postgres-and-cluster-database-split.md)); the `placementRegion` decision is unchanged._

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

## Amendment: `placementRegion` also supplies the DNS authority

The original decision scoped `placementRegion` to *provider-registration lookups only* and the
Pulumi program built the global slice's registry with a **nil DNS authority**, with an in-code
`TODO(ADR-0023)` to wire it later. That left the global slice able to realize only its
region-less network/compute/database — a global `service` or `ingress` validated but produced
**no DNS records, no nginx/ACME, no systemd unit, and no mesh leaf** (the post-processing
pipeline ran over regions only). A region-less, deploy-once authority (e.g. an identity/billing
service) was therefore undeployable.

- **`placementRegion` now also resolves the global slice's DNS authority** —
  `regionTable[placementRegion].Dns`. The placement region already names a fully-specified
  abstract region whose `dns:` block (provider + zone) is defined, so no schema change is
  needed: the global slice writes its **region-less** derived records
  (`<compute>.vm.<env>.<base>`, `<svc>.svc.<env>.<base>`, and any `vanity`) and ACME certs into
  that same zone. Region-less names cannot collide with the slug-bearing regional records.
- **The global slice is realized through the identical pipeline regions use** — `createInfra`,
  then DNS records → app seeds → ingress (nginx/ACME) → service secrets → services (the systemd
  unit + mesh leaf) — driven by a single "scopes" list whose first entry is the global slice
  (empty slug) followed by each region. Global is realized first so a regional
  `ref:database/global/<name>` still resolves against the already-populated global outputs.
- **`inforge validate` rejects a global slice that needs DNS but has no authority.** A global
  slice with **any compute** realizes a `<compute>.vm.<env>` host record (plus service/app
  records), so if the `placementRegion` declares no `dns:` block, validation fails with an
  actionable error rather than silently deploying nothing. `program.Run` re-checks the same
  condition (via `derivedRecords`) and fails fast at deploy if validate was bypassed. The guard
  only fires when the `placementRegion` is itself a defined region, so a typo'd placement region
  reports just the "not a defined region" error, not a second misleading one.

This resolves the former `TODO(ADR-0023)`.

### Known limitations (follow-ups)

- The derived host/service records are env-scoped and region-less (`<svc>.svc.<env>`), so they
  cannot collide with the slug-bearing regional records in the shared zone. A **literal** vanity
  or apex FQDN (e.g. `account.<base>`) declared identically on a global service *and* a service
  in the placement region would collide, and the per-scope dedup does not catch it. (This is an
  extension of the pre-existing cross-region case where two regions share a zone.)
- Neither the global nor the regional `dns:` guard checks that the DNS authority's *provider*
  credentials are present in the realizing providers block — a separate, pre-existing gap.
- Regional services have the same latent DNS requirement (a region with `tls-termination`/health
  but no `dns:`) — left unchanged here.

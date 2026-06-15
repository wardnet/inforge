---
sidebar_position: 2
---

# Runbook: add a region to the mesh

A new region joins the mesh by **minting that region's intermediate** from the cold root and then
redeploying. Until the intermediate exists, `inforge validate` fails for every regional service (a
regional service deploys to all regions, so each needs an intermediate).

**Posture:** offline (the mint signs with the cold root); the redeploy is the normal CI flow.

## Steps

1. **Add the region** to `resources/<env>/regions.yaml` (and its provider block) as usual.

2. **Mint the region's intermediate (offline).** On a workstation that holds the offline root identity:

   ```bash
   export INFORGE_PKI_ROOT_KEY="AGE-SECRET-KEY-…"   # the offline root identity
   inforge pki intermediate <env> <mesh-name> <region>     # e.g. … prd wardnet-mesh eu-central-1
   ```

   This signs a fresh intermediate for the new scope with the cold root and re-encrypts its key to the
   CI recipient. Repeat for every mesh in the env if you run more than one.

3. **Commit** the updated `resources/<env>/pki.enc.yaml`.

4. **Validate.** `inforge validate <env>` now passes — every regional service has an intermediate for
   the new region (credential-free check).

5. **Deploy.** `inforge deploy <env>` provisions the region's infrastructure and per-service
   workspaces. Leaves for the new region are minted at deploy / `inforge releases deploy` time, and
   each host projects them on boot.

## Verify

- `inforge pki ls <env>` lists the new region under the mesh's `intermediates:`.
- Services in the new region start and present a leaf whose SPIFFE SAN carries the new region scope.

## Notes

- This is **additive** — existing regions are untouched. Their trust bundles do not include the new
  region's intermediate (the regional boundary), so they cannot talk to it, and vice-versa; only
  region→global and intra-region traffic is allowed.
- To *remove* a region, drop it from `regions.yaml` and deploy; the now-orphaned intermediate can be
  left in the store (it signs nothing) or pruned in a follow-up commit.
- Minting a region's intermediate is **refused during a [root overlap](/runbooks/pki-rotate-root)** —
  it would chain only to the new root, invisible to consumers still on the old one. Finalize the root
  rotation first, then add the region.

---
sidebar_position: 5
---

# Runbook: rotate the root

Replace a two-tier mesh's **cold root** using a **dual-root overlap**: stand up a new root alongside
the old one, re-sign every intermediate from the new root (keys preserved), let all root-anchoring
consumers come to trust both roots, then drop the old root. During the overlap **both roots verify**.

**Posture:** offline (you must hold the current root identity to rotate it).

:::danger Cross-repo coordination required
**Mesh-root rotation is not just an inforge command.** Mesh *services* anchor on per-scope
*intermediate* bundles, not the root, so re-signing intermediates with their keys preserved leaves the
mesh undisturbed. The overlap exists for **root-anchoring consumers** — chiefly the **daemon fleet**,
which lives in a different repo (#610). Daemons reach mesh services over public Let's Encrypt certs and
do **not** anchor on the mesh root for that path; wherever a consumer *does* anchor on the mesh root,
its trust store must be updated to `{old, new}` **before** the old root is retired. Plan the rotation
with the daemon/consumer owners — finalizing too early breaks any consumer that still trusts only the
old root.
:::

## Steps

1. **Begin the overlap (offline):**

   ```bash
   export INFORGE_PKI_ROOT_KEY="AGE-SECRET-KEY-…"   # the CURRENT offline root identity
   inforge pki rotate <env> <mesh-name> --root      # e.g. … prd wardnet-mesh
   ```

   This:
   - verifies you hold the current cold root (only its custodian may rotate it),
   - mints a fresh cold root (encrypted to the same offline recipient),
   - retains the old root in `previousRoots`,
   - re-signs **every** intermediate from the new root, **preserving each intermediate key** — so
     every live leaf keeps verifying — and retains the old intermediate certs in
     `previousIntermediates` so a chain to the old root still validates during the window.

2. **Commit** `resources/<env>/pki.enc.yaml`. The store now holds both roots.

3. **Distribute the new root** to every root-anchoring consumer so each trusts `{old, new}`. Mesh
   services need **no** change (they anchor on intermediate keys, which were preserved). Coordinate
   with the daemon/consumer owners; wait until every consumer has the new root.

4. **Optionally renew leaves** (`inforge pki renew <env>`) so new leaves chain through the re-signed
   intermediates to the new root. Not required for the mesh — existing leaves already verify under both
   roots — but it advances consumers toward new-root-only chains.

5. **Finalize the overlap (offline)** once *every* consumer trusts the new root:

   ```bash
   export INFORGE_PKI_ROOT_KEY="AGE-SECRET-KEY-…"   # the offline root identity
   inforge pki rotate <env> <mesh-name> --root --finalize
   ```

   Finalize is **custody-gated** like the begin step — it proves you hold the (new) cold root before
   retiring the old one, so CI or a stale checkout can't drop the old root prematurely. It drops the
   retained old root and old intermediate certs. Commit the result.

## Verify

- After step 1: `inforge pki ls <env>` is unchanged in shape; the store's `previousRoots` /
  `previousIntermediates` are populated. The same live leaf verifies under **both** roots (old chain:
  leaf → old-intermediate → old root; new chain: leaf → new-intermediate → new root).
- After step 5: only the new root remains; `previousRoots` is empty.

## Notes

- Re-running `--root` while an overlap is active is refused — finalize first.
- **Intermediate rotation/recovery is blocked during an overlap.** Rolling an intermediate key
  mid-overlap would orphan the old-key leaves the overlap protects, so
  [rotate an intermediate](/runbooks/pki-rotate-intermediate) and
  [recover a compromised intermediate](/runbooks/pki-recover-intermediate) refuse to run until you
  `--finalize`. The rotation itself **preserves intermediate keys**, so the mesh needs no intermediate
  change.
- The old root key is retained (cold) until finalize, so the rotation can be reasoned about — but it
  signs nothing new once the overlap begins.

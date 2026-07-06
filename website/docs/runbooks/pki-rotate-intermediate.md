---
sidebar_position: 4
---

# Runbook: rotate an intermediate

A **planned** key roll of one scope's intermediate — re-mint it from the cold root with a fresh key.
Use this for routine hygiene or when an intermediate approaches its validity window. For a *suspected
compromise*, use [recover a compromised intermediate](/runbooks/pki-recover-intermediate) instead — it
is the same crypto but a no-overlap, act-now posture.

**Posture:** offline for the re-mint (signs with the cold root); CI for the follow-up renew.

## Why this is low-risk

The mesh's **regional boundary** keeps a region's intermediate out of every other region's trust
bundle, so rotating one scope's intermediate is **invisible to every other region**. Within the scope,
the fresh intermediate key means existing leaves must be re-minted — which the routine renew already
does.

## Steps

1. **Re-mint the intermediate (offline):**

   ```bash
   export INFORGE_PKI_ROOT_KEY="AGE-SECRET-KEY-…"   # the offline root identity
   inforge pki rotate <env> <mesh-name> --intermediate <scope>     # e.g. … prd wardnet-mesh us-east-1
   ```

   A fresh intermediate key replaces the old one, signed by the cold root and re-encrypted to the CI
   recipient. The scope must already have an intermediate (this is a rotation, not a first mint — use
   `inforge pki intermediate` for a brand-new scope).

2. **Commit** `resources/<env>/pki.enc.yaml`.

3. **Re-mint leaves** from the new intermediate, within the leaf TTL (sooner is better so peers
   converge on a consistent intermediate):

   ```bash
   export INFORGE_SECRETS_KEY="AGE-SECRET-KEY-…"   # the CI master identity
   inforge pki renew <env>
   ```

4. **Hosts converge immediately.** `inforge pki renew` already pushed the new leaf + bundle to every
   affected host and reload-or-restarted it in step 3 — there is nothing further to run.

## Verify

- `inforge pki ls <env>` still lists the scope's intermediate; the cert content has changed.
- A renewed leaf chains to the new intermediate, which chains to the (unchanged) root.

## Notes

- During the brief window between step 1 and step 4, peers in the scope may hold a mix of old- and
  new-intermediate leaves. Both verify as long as the verifier's bundle contains a cert with the
  matching public key — so renew promptly and converge hosts to a single intermediate. If you need a
  zero-mixed-window guarantee, treat it like a [root overlap](/runbooks/pki-rotate-root) and stage the
  bundle, but for a planned roll the short window is normally acceptable.
- The **root is untouched** — verifiers that anchor on the root are unaffected.
- This command is **refused during a [root overlap](/runbooks/pki-rotate-root)** (`previousRoots`
  present) — rolling the intermediate key would orphan the old-key leaves the overlap protects.
  Finalize the root rotation first.

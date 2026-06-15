---
sidebar_position: 6
---

# Runbook: recover a compromised intermediate

An intermediate key is believed leaked. Re-mint that scope's intermediate from the cold root with a
**fresh key** and force every host in the scope to re-project **immediately** — the old key is burned,
so there is no overlap and no grace period.

**Posture:** offline for the re-mint; CI for the urgent renew. Treat as an incident.

:::danger Act now — no overlap
Every leaf the compromised intermediate signed is now untrusted the moment you replace it. Unlike a
[planned rotation](/runbooks/pki-rotate-intermediate), do **not** leave a mixed window: renew and
re-project on every host at once.
:::

## Steps

1. **Re-mint the intermediate (offline):**

   ```bash
   export INFORGE_PKI_ROOT_KEY="AGE-SECRET-KEY-…"   # the offline root identity
   inforge pki recover-intermediate <env> <mesh-name> <scope>     # e.g. … prd wardnet-mesh us-east-1
   ```

   A fresh intermediate key replaces the compromised one, signed by the cold root and re-encrypted to
   the CI recipient.

2. **Commit** `resources/<env>/pki.enc.yaml` and merge promptly.

3. **Re-mint leaves immediately (CI):**

   ```bash
   export INFORGE_SECRETS_KEY="AGE-SECRET-KEY-…"   # the CI master identity
   inforge pki renew <env>
   ```

4. **Force re-projection on every host in the scope** — do not wait for the daily timer:

   ```bash
   systemctl start wardnet-<svc>-renew.service   # on each host running an affected service
   ```

5. **Confirm** no service still presents a leaf signed by the old key (check the leaf's issuer /
   served chain on each host).

## Scope of the blast radius

Because of the **regional boundary**, a compromised *regional* intermediate only affects that region —
other regions never trusted it. A compromised *global* intermediate affects every service that trusts
global (i.e. all of them), so treat a global-scope compromise as the widest incident.

## Notes

- This command is the same crypto as `inforge pki rotate --intermediate`; it exists separately to make
  the incident posture explicit (no overlap, immediate renew + forced re-projection) and auditable.
- It is **refused during a [root overlap](/runbooks/pki-rotate-root)** — finalize the root rotation
  first. (A compromise during a root overlap is a coordinate-with-the-root-custodian situation, not a
  routine recovery.)
- The **root is not compromised** by an intermediate leak — do not rotate the root for this. If the
  *root* key is believed leaked, follow [rotate the root](/runbooks/pki-rotate-root) and shorten the
  overlap to the minimum your consumers can tolerate.

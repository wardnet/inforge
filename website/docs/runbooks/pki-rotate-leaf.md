---
sidebar_position: 3
---

# Runbook: rotate a leaf

Mesh leaves are **short-TTL** (90 days) and carry no CRL/OCSP — **expiry is the revocation
mechanism**. So "rotating a leaf" is just minting a fresh one and letting hosts re-project it; there is
no in-place leaf command to run.

**Posture:** CI / infra repo (leaf minting uses the CI identity).

## Routine rotation (all services)

Schedule this well inside the 90-day window (e.g. weekly):

```bash
export INFORGE_SECRETS_KEY="AGE-SECRET-KEY-…"   # the CI master identity
inforge pki renew <env>
```

`inforge pki renew` mints one fresh leaf per (service, scope) from the committed intermediate and
writes the leaf + key + per-scope trust bundle to the secrets provider. It **never runs Pulumi**, so
it is safe to cron even with un-shipped infra changes. Each host re-projects the new leaf on its
`wardnet-<svc>-renew.timer` (daily) and reloads or restarts the unit only if the leaf changed.

`inforge pki rotate <env> <mesh-name> --leaf` prints this same guidance — it performs no mutation.

## Roll a single service's leaf immediately

1. Run `inforge pki renew <env>` (writes a fresh leaf for every service to the provider).
2. On the host, force the projection instead of waiting for the daily timer:

   ```bash
   systemctl start wardnet-<svc>-renew.service
   ```

   The bootstrapper re-fetches and atomically projects the new leaf, then `reload`s the unit if it
   declares a `reload:` command, else `restart`s it.

## Notes

- A **newly released** service gets its first leaf from `inforge releases deploy` (it mints the
  released service's leaf before the restart), so its first start never crash-loops on a missing cert.
- If you need to invalidate a leaf *before* its TTL (e.g. a host key leak), rotating the leaf is not
  enough — the signing **intermediate** must be re-minted so the old leaf no longer chains. See
  [recover a compromised intermediate](/runbooks/pki-recover-intermediate).

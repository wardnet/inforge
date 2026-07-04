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
writes the material to the secrets provider: each mesh host's per-host aggregate (leaves + trust
bundle, for the mesh proxy) plus the per-service `/<svc>/mtls` copy for `mtls_files: true` services.
It **never runs Pulumi**, so it is safe to cron even with un-shipped infra changes. Each host
converges on its own daily timer: the mesh proxy re-projects via `wardnet-mesh-renew.timer` and
reloads its nginx only if material changed; an `mtls_files:` service additionally re-projects its own
copy via `wardnet-<svc>-renew.timer` and reloads or restarts the unit.

`inforge pki rotate <env> <mesh-name> --leaf` prints this same guidance — it performs no mutation.

## Roll a service's leaf immediately

1. Run `inforge pki renew <env>` (writes fresh material for every mesh host and `mtls_files:` service
   to the provider).
2. On the service's host, force the mesh proxy's pull instead of waiting for the daily timer:

   ```bash
   systemctl start wardnet-mesh-renew.service
   ```

   The agent re-fetches and atomically projects every co-located service's leaf + the trust bundle,
   then reloads the mesh nginx (no downtime).

3. **Only for an `mtls_files: true` service** (it holds its own raw-plane copy), also force the
   per-service projection:

   ```bash
   systemctl start wardnet-<svc>-renew.service
   ```

   The agent re-fetches and atomically projects the new leaf, then `reload`s the unit if it
   declares a `reload:` command, else `restart`s it. This unit exists only on hosts of
   `mtls_files:` services.

## Notes

- A **newly released** `mtls_files:` service gets its first leaf from `inforge releases deploy` (it
  mints the released service's leaf before the restart), so its first start never crash-loops on a
  missing cert. Every other service holds no cert material — its mesh leaf is delivered to the mesh
  proxy at deploy (`inforge deploy`'s mesh baseline) and on renew, independent of releases.
- If you need to invalidate a leaf *before* its TTL (e.g. a host key leak), rotating the leaf is not
  enough — the signing **intermediate** must be re-minted so the old leaf no longer chains. See
  [recover a compromised intermediate](/runbooks/pki-recover-intermediate).

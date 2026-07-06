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
SSH-pushes the material directly to each host: each mesh host's per-host aggregate `leaf.age` (leaves
+ trust bundle, for the mesh proxy) plus the per-service `/<svc>/mtls` copy for `mtls_files: true`
services — then unconditionally reload-or-restarts the consumer. It **never runs Pulumi**, so it is
safe to cron even with un-shipped infra changes; it does need SSH reachability to every target host.

`inforge pki rotate <env> <mesh-name> --leaf` prints this same guidance — it performs no mutation.

## Roll a service's leaf immediately

Run `inforge pki renew <env>`. Because renewal now pushes each host's fresh `leaf.age` over SSH and
unconditionally reload-or-restarts the consumer in the same step, there is nothing further to trigger
on the host — the mesh proxy (and, for an `mtls_files: true` service, its own raw-plane copy) picks up
the new leaf immediately.

## Notes

- A **newly released** `mtls_files:` service gets its first leaf from `inforge releases deploy` (it
  mints the released service's leaf before the restart), so its first start never crash-loops on a
  missing cert. Every other service holds no cert material — its mesh leaf is delivered to the mesh
  proxy at deploy (`inforge deploy`'s mesh baseline) and on renew, independent of releases.
- If you need to invalidate a leaf *before* its TTL (e.g. a host key leak), rotating the leaf is not
  enough — the signing **intermediate** must be re-minted so the old leaf no longer chains. See
  [recover a compromised intermediate](/runbooks/pki-recover-intermediate).

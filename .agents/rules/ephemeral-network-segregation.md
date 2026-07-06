# An ephemeral environment never shares a Hetzner Network with any other environment

An ephemeral environment (ADR-0028) is a clone of a source env's resource definition deployed under a
distinct slug identity. It runs in the **same Hetzner project / Neon account / Cloudflare zone**
as every other env, isolated only by the slug baked into every name and the
`ephemeral=true` label. The one isolation that must hold unconditionally is **network segregation**: an
ephemeral host must never be able to route to a real env's private IPs, even though the clone inherits
the source's Network CIDR verbatim.

This is **structural, not enforced by new code**. Two existing invariants combine to guarantee it:

1. **Every env provisions its own Hetzner Network**, named `naming.Resource(env, slug, "net", …)`
   (`providers/hetzner/network.go`, `ensureContainer`). The `env` segment is the slug identity, so an
   ephemeral env's Network is a *different cloud object* from the source's — two unconnected Networks
   that happen to carry the same CIDR. Unpeered Hetzner Networks do not exchange traffic, so an
   identical inherited CIDR is harmless: there is no route between them.
2. **There is no Network peering anywhere.** No `NetworkRoute`, no peering resource, no cross-Network
   route exists in `providers/` or `program/`. Private firewall rules are within-Network only — a
   cross-host backend opens its target ports solely to its own `NetworkSpec.CIDR`
   (`PrivateSourceCIDR`, see `cross-host-route-requires-same-network`).

## Applies to

`program/program.go` (the per-env `BuildRegistry` → `createInfra` Network creation) and
`providers/hetzner/network.go`. **Never introduce Network peering, a cross-Network route, or a shared
Network keyed on anything other than `(env, slug)`** — any of these would silently bridge an ephemeral
env into a real env that shares its inherited CIDR. If a future feature needs cross-env connectivity,
it must not key the Network on a value that two envs can share.

## Why

The decoupling that makes ephemeral envs cheap — cloning the source's definition, CIDR and all — is
exactly what would make a routing bridge catastrophic: an ephemeral preview could reach production's
private database. The segregation is free *because* naming is env-scoped and nothing peers; codifying
it as an invariant keeps it free. The `ephemeral`/`expires_at` Hetzner labels are for orphan auditing
only — they are **not** a segregation mechanism and must never be relied on as one.

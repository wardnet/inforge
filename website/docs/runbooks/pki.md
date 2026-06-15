---
sidebar_position: 1
---

# PKI operator runbooks

Step-by-step procedures for the lifecycle of the [service-mesh PKI](/cli/pki) — adding a region,
and rotating or recovering each tier of the trust tree. They implement the lifecycle of
[ADR-0024](https://github.com/wardnet/inforge/blob/main/docs/adr/0024-inforge-pki-service-mesh-and-daemon.md).

## Before you start: two identities, two postures

Every PKI operation uses exactly one of two age identities — keep them separate:

| Identity | Env var | Holds | Used by |
|---|---|---|---|
| **Offline root** | `INFORGE_PKI_ROOT_KEY` | the cold root keys (printed once by `inforge pki init`) | `intermediate`, `rotate --intermediate`, `rotate --root`, `recover-intermediate` |
| **CI master** | `INFORGE_SECRETS_KEY` | intermediate keys (for minting leaves) | `pki renew`, `deploy`, `releases deploy` |

The cold root identity must **never** reach CI. Operations that sign with the root run **offline**
on an operator workstation that holds `INFORGE_PKI_ROOT_KEY`; operations that mint **leaves** run in
CI (or from the infra repo) with `INFORGE_SECRETS_KEY`. The runbooks below say which posture each step
needs.

## How trust actually anchors (read this once)

The mesh's trust model drives every rotation decision:

- **Mesh services anchor on per-scope *intermediate* bundles, not the root.** A leaf verifies against
  the intermediate that signed it (delivered in the service's [trust bundle](/cli/pki#scopes-and-the-regional-boundary)).
  The mesh root is **not** in any mesh verifier's trust path.
- **The regional boundary is structural.** A region only ever receives `{its region, global}`
  intermediates, so one region's intermediate is invisible to every other region. Rotating or
  recovering a regional intermediate cannot affect another region.
- **Root-anchoring consumers are the exception.** Anything that anchors on the mesh **root** (e.g. the
  daemon fleet, cross-repo) needs a [dual-root overlap](/runbooks/pki-rotate-root) when the root rotates.

## The runbooks

| Runbook | When |
|---|---|
| [Add a region](/runbooks/pki-add-region) | a new region joins the mesh |
| [Rotate a leaf](/runbooks/pki-rotate-leaf) | scheduled leaf renewal, or a single service's cert |
| [Rotate an intermediate](/runbooks/pki-rotate-intermediate) | planned key roll of one scope's intermediate |
| [Rotate the root](/runbooks/pki-rotate-root) | the cold root is being replaced |
| [Recover a compromised intermediate](/runbooks/pki-recover-intermediate) | an intermediate key is believed leaked |

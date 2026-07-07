---
status: accepted
date: 2026-07-04
issue: "#168"
---

# Path-level exposure control and gateway-world health addressing

> Refines ADR-0032's gateway authoring (`routes:` is replaced by a derived
> routing table) and ADR-0027's health tier (probe paths become declared,
> allowlist-only; gateway-routed services gain health addressing).

## Context

The gateway authored coarse path-prefix routes (`{path: /tenants/, service}`):
everything under a claimed prefix traversed to the service, and the health tier
proxied `location /` on the public health port — any path on either surface
reached the backend. Probing for undeclared endpoints (debug handlers, admin
routes, `/debug/pprof`) was possible from the internet (within a prefix) and
from any allowed mesh peer (everywhere). Separately, a pure gateway/mesh
service (no `ingress:` FK) had **no** health server and **no** DNS record at
all — `resolveIngressServices` skipped it and service A records derived only
from routes — so gateway-world services were unprobeable.

## Decision

**The service declares its endpoint surface; the gateway declares which
services are public. Everything else is derived, and undeclared paths are
dropped at the first proxy that sees them.**

- **Authoring.** `mesh.public_paths` + `mesh.internal_paths` on the service —
  absolute path globs (`*` = one segment, one-or-more chars; trailing `/**` =
  the node and any tail; charset `[A-Za-z0-9._-]`; compiled by
  `internal/pathglob`). The gateway spec drops `routes:` for `services:
  [names]`; its nginx routing table is derived: one regex location per (listed
  service, public glob), target named in `X-Mesh-Target` as before.
- **Two-plane enforcement, one source.** The gateway serves only `public_paths`
  (unmatched → JSON 404 at the TLS edge, zero backend traffic). The callee's
  mesh proxy admits `public ∪ internal` from peers (unmatched → JSON 404) — an
  undeclared handler is unreachable even by an allowed, authenticated caller.
  `internal_paths` keeps peer-only endpoints internet-invisible on a
  gateway-listed service.
- **Closed by default.** A mesh callee must declare ≥1 path across both lists;
  a gateway-listed service must declare ≥1 public path; `health_probes_port`
  requires ≥1 `health_probe_paths`. All hard `inforge validate` errors — an
  optional allowlist is documentation, not an allowlist.
- **Overlap is the load-bearing invariant.** With a derived table nothing else
  prevents two services claiming one path: validation rejects pairwise-
  overlapping public globs across a gateway's services, and public/internal
  overlaps within one service (`pathglob.Overlaps`, product-automaton segment
  unification). Paths are absolute so the same globs enforce identically at
  both planes (the gateway forwards the path byte-for-byte).
- **Health.** Probe paths are declared per service (`health_probe_paths`,
  exact-match locations + 404 catch-all; no fixed `/healthz` convention). The
  port-81 Host-demuxed tier extends to gateway-listed services without an
  ingress: health server on the **gateway's** host (`GatewaySpec.
  health_probes_port`, default 81), ServiceFQDN A record at the gateway host,
  cross-host backends opened privately — all from one resolver
  (`resolveGatewayHealthServices`). A service with both an ingress and a
  gateway listing keeps its health at the **ingress** (one canonical health
  address, or the A record would derive at two hosts). The gateway's own
  liveness is `health_probe_paths` on the gateway spec, rendered on the **443**
  server as `return 200 "ok"` — the probe traverses DNS+cert+TLS, the real
  daemon path; validation rejects a liveness path a public glob claims.

## Consequences

- Existing `gateway` manifests using `routes:` fail schema validation; resource
  repos migrate to `services:` + per-service `public_paths` in lockstep. Every
  mesh callee and every health-declaring service needs its paths declared.
- The service's endpoint surface now exists in the manifest as well as the
  service router; drift is fail-closed (a new endpoint 404s at the edge until
  declared and deployed — shipping a new public endpoint deliberately touches
  the declared surface).
- One trap documented loudly: on a gateway-listed service, a path in
  `public_paths` is internet-reachable — peer-only endpoints belong in
  `internal_paths`.
- The anti-301 exact-match guard in the gateway server is gone (no prefix
  proxy locations remain); mesh-level path filtering happens before the
  service, so callers see 404s for undeclared paths instead of app errors.

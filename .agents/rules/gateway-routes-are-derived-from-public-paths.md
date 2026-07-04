# Gateway routes are derived from mesh.public_paths and must be pairwise non-overlapping

The north-south gateway's routing table is **derived, never authored** (ADR-0034): the
gateway spec lists `services:`, and `program.toGatewayNginxRoutes` produces one route per
(listed service, `mesh.public_paths` glob). The nginx render compiles each glob to an
anchored regex location (`internal/pathglob`). nginx evaluates regex locations **in
emitted order for every request**, so the ONLY thing preventing one service's traffic
from being routed to another is that the patterns are pairwise non-overlapping across the
gateway's services — `validate.checkGateway` enforces this with `pathglob.Overlaps`.

Two invariants follow:

1. **Never author or synthesize a gateway route outside the derivation.** A hand-added
   location (or a second derivation path) can silently shadow a service's glob depending
   on emission order — preview and `nginx -t` both stay green.
2. **Never weaken or bypass the overlap check.** Overlap within ONE service's own list is
   harmless (same target); overlap across services (and between one service's public and
   internal lists — ambiguous edge visibility) must stay a hard validation error.

The same globs are enforced at BOTH planes from one source: the gateway serves
`public_paths` only; the callee mesh proxy admits `public ∪ internal`
(`program/mesh.go` threads them into `meshnginx.LocalService.Paths`). Undeclared paths
are a JSON 404 at whichever proxy sees them first. Closed by default: a mesh callee with
no paths, a gateway-listed service with no public paths, and a health port with no
`health_probe_paths` are all validation errors — do not add passthrough fallbacks.

## Applies to

`program/program.go` (`toGatewayNginxRoutes`, `gatewaysByHost`), `program/mesh.go`
(`meshInputsByHost` Paths threading), `internal/nginx/config.go` (`gatewayServer`),
`internal/meshnginx/ingress.go` (`ingressServer`), `internal/validate/validate.go`
(`checkGateway` overlap pass, `checkMesh` path checks), `internal/pathglob` (the single
glob syntax/compile/overlap source — never re-implement glob→regex elsewhere).

## Why

With a derived table there is no authored path claim to arbitrate collisions; the overlap
check IS the arbitration. Losing it (or adding routes outside the derivation) turns a
manifest edit in one service into silent traffic misrouting for another — the failure is
invisible until runtime because every rendered config is structurally valid.

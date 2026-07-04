# Mesh host grouping must come from internal/meshplan

The derivation of which mesh members sit on which canonical compute host exists
once, in `internal/meshplan`: `ServicesByHost` for `pki:` services (the POSITIONAL
egress-port universe) and `GatewayMemberByHost` for the north-south gateway's
synthetic member (fixed `meshpaths.GatewayEgressPort` — NEVER inserted into the
positional sort, which would shift every co-located service's injected
INFORGE_MESH_URL). The deploy program derives the per-host proxy inputs from them
(`program/mesh.go`), and the renewal path writes each host's provider material
(`/<hostKey>/<member>/leaf.*` + `bundle.crt`) over the same pair
(`cmd/inforge/pki.go`). Re-deriving the grouping inline in
either place lets the two drift: the proxy would present leaves the renew never
refreshes, or renew would write material no proxy reads — both silent (preview and
`nginx -t` stay green), surfacing only as expired-leaf handshake failures weeks
later. The same failure shape as the egress-port rule
(`mesh-egress-port-assignment-is-single-sourced`), which this rule extends to the
host grain.

## Applies to

`program/mesh.go`, `cmd/inforge/pki.go` (renewMeshCertsAs), `internal/meshplan`,
and any future consumer that needs "the services on a mesh host" (validation,
reporting, gateway realization).

## Example

```go
// WRONG — inline re-derivation, can drift from the proxy realization
byHost := map[string][]types.ServiceSpec{}
for _, svc := range res.Service {
    if svc.Pki != "" { byHost[canonical[svc.Host]] = append(...) }
}

// RIGHT — the shared, sorted derivation both sides consume
byHost := meshplan.ServicesByHost(res, naming.CanonicalComputeKeys(res.Compute))
```

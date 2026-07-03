# A mesh service's egress port must be derived identically for the proxy listener and the injected URL

Each mesh (`pki:`) service is assigned a deterministic loopback **egress port** — the port its local
mesh proxy binds to accept that service's outbound calls, and the port its `INFORGE_MESH_URL` points at.
Two independent code paths compute this port, and they MUST produce the same number for the same service,
or the service dials `INFORGE_MESH_URL` at a port no listener answers on (every outbound mesh call fails):

- **`program.meshInputsByHost`** (`program/mesh.go`) — builds the proxy's `Egress` listeners:
  `meshpaths.EgressPort(index)` over the host's `pki:` services in **sorted-name** order.
- **`program.meshEgressPortsByService`** (`program/mesh.go`) — builds the map the descriptor render path
  (`renderDescriptor` → `INFORGE_MESH_URL`) reads.

Both MUST group by the same canonical host, filter on the same membership (`svc.Pki != ""`), and order by
the same key (service name). The assignment is `meshpaths.EgressPort(i) = meshpaths.EgressBase + i` — the
single source of the port arithmetic. Never inline `EgressBase + n` or re-order the services in one path
only.

## Applies to

`program/mesh.go` (`meshInputsByHost`, `meshEgressPortsByService`), `internal/meshpaths` (`EgressPort` /
`EgressBase` — the port arithmetic), and `program.renderDescriptor` (the consumer of the port map). If a
future change alters the ordering or grouping of a host's mesh services, it must change **both** derivations
together, and the port arithmetic must stay in `meshpaths`.

## Why

The egress port is the v1 caller-identity mechanism (ADR-0032): the mesh maps `127.0.0.1:<egress port>` to
the calling service. A drift between the listener and the injected URL is silent — preview and `nginx -t`
pass (both are structurally valid) — and only surfaces at runtime as a connection refused or, worse, a call
attributed to the wrong caller. Deriving the port once, identically, removes the failure mode by construction.

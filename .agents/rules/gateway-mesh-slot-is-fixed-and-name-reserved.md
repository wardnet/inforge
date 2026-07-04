# The gateway's mesh slot is fixed and its member name reserved

The north-south gateway joins the mesh as the synthetic member
`meshpaths.GatewayMember` ("gateway") with the FIXED egress port
`meshpaths.GatewayEgressPort` — never a slot in the positional
`meshpaths.EgressPort(index)` scheme. Inserting it into the sorted service list
would shift every co-located service's index and break the already-injected
`INFORGE_MESH_URL`s silently (preview and `nginx -t` both stay green; calls fail
at runtime as connection-refused or misattributed callers). For the same
identity reason a real service may not be named "gateway": its leaf would carry
`CN=<scope>/gateway` and could forge daemon-originated traffic at every callee
that demuxes on `X-Service-Identity` — `validate.checkService` rejects the name.

## Applies to

`internal/meshpaths` (GatewayMember/GatewayEgressPort/InReservedEgressRange),
`internal/meshplan.GatewayMemberByHost`, `program/mesh.go` (the egress append),
`internal/nginx` (gatewayServer's proxy_pass port), `internal/validate`
(reserved name), and any future consumer of the gateway's mesh identity.

## Example

```go
// WRONG — gateway inserted into the positional universe; service ports shift
svcs = append(svcs, gatewayMember)
sort.Slice(svcs, ...)
for i, svc := range svcs { port := meshpaths.EgressPort(i) }

// RIGHT — services keep their positional slots; the gateway gets the fixed one
for i, svc := range meshplan.ServicesByHost(res, canonical)[host] { ... EgressPort(i) ... }
mh.egress = append(mh.egress, meshnginx.EgressCaller{
    Name: gw.Name, EgressPort: meshpaths.GatewayEgressPort, ...})
```

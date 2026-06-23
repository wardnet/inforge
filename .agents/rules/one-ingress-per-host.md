# A compute host hosts at most one ingress

The nginx config, firewall plan, and public health port are all derived **per host** (not per
ingress resource). Two ingress resources sharing a `host:` FK would silently merge or override each
other — e.g. only one `health_probes_port` would survive, route sets would interleave
non-deterministically — so validation rejects the collision up front.

`checkIngress` in `internal/validate/validate.go` tracks ingress-to-host mappings in
`ctx.ingressNamesByHost` (keyed by canonical compute specKey) and calls `otherUsers(...)` to reject
any second ingress that resolves to the same host.

## Applies to

`internal/validate/validate.go` (`checkIngress`, the `ingressNamesByHost` map). When adding a
new resource type that is derived per compute host (like ingress), apply the same uniqueness check
using the `ingressNamesByHost` pattern — do not assume multiple resources per host are safe even
if the schema allows it.

## Why

nginx realization, the firewall plan, and the health-port binding are all per-host operations in
`program/program.go` (`realizeIngress`, `firewallPlanByHost`). They accept at most one ingress's
worth of routes, apps, and health endpoints per host. Allowing two ingresses on one host would
produce a rendered config that belongs to neither — a silent correctness failure that would be hard
to detect until `nginx -t` at deploy time or worse.

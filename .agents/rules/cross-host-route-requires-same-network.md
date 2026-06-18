# A cross-host service route requires the ingress host and service host to share a network

A service is fronted by a standalone ingress (its `ingress:` FK) whose nginx runs on the ingress's
compute host. When that host differs from the service's own host (cross-host routing), the ingress
nginx reaches the backend over the **private network** using the backend's `PrivateIP` —
`proxy_pass http://<private-ip>:<target>`. For that to work, both hosts must sit on the **same Hetzner
Network** (subnets within one Network are mutually routable; cross-Network private traffic is not).

`checkService` in `internal/validate/validate.go` enforces this: for any service with `routes:` whose
ingress host ≠ service host, `ctx.computeNetwork[ingressHost]` must equal
`ctx.computeNetwork[serviceHost]`, else validation fails with a "share a network" error. The same-host
(co-located) case is exempt — nginx reaches the backend over loopback (`127.0.0.1`).

The firewall mirrors this (`program.firewallPlanByHost`): a cross-host backend opens its route `target`
ports **only** to its network CIDR (`types.FirewallPorts.PrivateSourceCIDR`, sourced from
`NetworkSpec.CIDR`), never to the internet. Co-located targets get no firewall rule (loopback is not
filtered). Only the ingress host exposes the public `listen` ports.

## Applies to

`internal/validate/validate.go` (`checkService`, the `computeNetwork` map) and
`program/program.go` (`firewallPlanByHost`, `ingressRoutesByHost`, `realizeIngress`). When adding a new
backend-reachability path, scope the private firewall source to the network CIDR — never widen it to
the internet, and never assume same-subnet (validation only guarantees same-Network).

## Why

Picking the subnet CIDR (rather than the Network CIDR) as the private firewall source would break a
valid deployment where the ingress and backend sit in different subnets of one Network — which
validation explicitly permits. The Network CIDR is the tightest static scope that is always correct.

# A service can declare ports inforge opens on the host's private network only

Until now a service could open a port only three ways, and every one of them is **public**:

- an **ingress route** (`tls-termination` / `forward`) — nginx fronts a backend port, the public
  listen port opens to `0.0.0.0/0` (ADR-0026/0027);
- a raw **`compute.firewall.inbound`** rule — opens to `0.0.0.0/0` (and `::/0`) by construction;
- the **health tier** — the ingress's public health port opens to `0.0.0.0/0` (ADR-0027).

Nothing let a service declare a port it binds that should be reachable **only on the host's
private-network CIDR** — peer / service-to-service traffic that must never touch the internet.

The motivating case: a `tunneller` node binds an inter-node mesh-mTLS listener (`:9444`) that sibling
nodes on the **same regional private network** must reach, but the internet must not. The only
existing option, `firewall.inbound`, opens it to `0.0.0.0/0` — wrong. A workaround (front it with a
`forward` route) drags in nginx, an ingress, and a public listen port for a connection that is purely
node-to-node.

We decided to add a service-level **`exposed_ports`** field: ports inforge opens on the host's
**private network only**, never the public internet, with **no ingress** and **no nginx** involved.

## The field

```yaml
exposed_ports:
  - { proto: tcp, port: 9444 }    # inter-node mesh mTLS — private network only
  - { proto: udp, port: 51820 }   # peer link
```

Each entry is `{proto: tcp|udp, port: 1..65535}`. The shape mirrors `compute.firewall.inbound` but is
**narrower**: the port is a plain integer (no ranges) and proto is `tcp`/`udp` only (no `icmp`), so it
is comparable and used directly as a map key (`types.ExposedPort`). A service **may** declare
`exposed_ports` with no ingress and no routes — a *private-only* service is valid (unlike
`health_probes_port`, which requires an ingress to surface it).

`exposed_ports` is the **private sibling** of `compute.firewall.inbound`: same "raw port, no proxy"
intent, opposite exposure. A truly public raw port stays `compute.firewall.inbound`.

## Realization (firewall, private CIDR only)

The port plan is derived per host (`program.firewallPlanByHost`), purely from static spec data. Each
service's `exposed_ports` are collected onto the service's **own** host (read from every service
directly, not via the ingress derivation, so a private-only service is included) and carried as
`FirewallPorts.PrivateExposed` (proto-aware, deduped, sorted). The Hetzner provider
(`ensureFirewall`) opens each as an inbound rule scoped to `PrivateSourceCIDR` — the host's
network CIDR — reusing exactly the path that scopes a cross-host route target to the private network.
They are **never** added to the public source set. The concept is modelled provider-agnostically; only
Hetzner exists today (a provider whose private network needs no rule may no-op).

## Validation (credential-free, proto-aware collisions)

An exposed port is a backend bind on the service's host, registered in the host's target space
alongside route targets and `health_probes_port`. The collision model is **proto-aware** — `tcp/N`
and `udp/N` are distinct OS binds and may coexist:

- A **tcp** exposed port shares the (implicitly-TCP) backend target space: it must differ from the
  service's own route targets and its own `health_probes_port`; must not equal a public listen port
  nginx holds on that host; must not be another service's backend port on that host; and, when the
  host runs an ingress, must stay out of the reserved loopback range
  `[LoopbackBase, LoopbackBase+MaxMixedPorts)` (an ssl_preread terminator binds there).
- A **udp** exposed port collides only with another service's udp exposed port on the same host.
- A `(proto, port)` declared twice on one service is rejected.

There is **no** ingress requirement and **no** cross-host same-network rule — an exposed port is local
to its own host; it is the *operator's* contract that siblings sit on the same private network.

## Considered options

- **Reuse `compute.firewall.inbound` with a private-source flag.** Rejected: `firewall.inbound` is a
  compute-level, public-by-definition primitive; overloading it with a per-rule source scope blurs the
  public/private boundary the two fields exist to keep crisp. A service-level field also co-locates the
  declaration with the workload that binds the port.
- **Front the port with a `forward` route.** Rejected: drags in an ingress, nginx, and a public listen
  port for a connection that is node-to-node and must not be public.
- **TCP-only (drop udp).** Rejected: peer links (wireguard, QUIC) are udp; the firewall realization
  carries proto end-to-end so udp opens a real rule rather than silently no-op'ing.
- **Proto-agnostic collisions (port number only).** Rejected: now that udp is realized, `tcp/9444`
  and `udp/9444` are genuinely distinct binds; collapsing them would forbid a legal co-bind.

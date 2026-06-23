# nginx ssl_preread lets a forward coexist with tls-termination on one port, and a health tier

ADR-0015 split a service host's ingress into two contexts: `tls-termination` routes in `http{}`
(nginx terminates ACME TLS, demuxing shared ports by `server_name`/SNI) and `forward` routes in
`stream{}` (raw L4 + the PROXY protocol, single-service-exclusive). It deliberately ruled out SNI
inspection — *"inforge's nginx needs no `ssl_preread` and no catch-all route"* — punting SNI
passthrough to the bridge daemon. ADR-0026 then made `ingress` a shared proxy tier fronting services
and apps, with apps always served as `listen 443 ssl` in `http{}`.

That left a hard limitation: a `tls-termination` server and a `forward` server **cannot share a
listen port**. Both would bind `*:443` (the http one as `ssl`, the stream one raw), which the kernel
forbids on one host. So a compute that terminates TLS for some SNIs *and* passes one SNI through
untouched is impossible — wardnet's first topology needed **two hosts** purely to separate "terminate
here" from "pass through there." Separately, nothing surfaced a service **health endpoint** through
the ingress for an external prober.

We decided to **bring `ngx_stream_ssl_preread` into inforge's nginx** (reversing ADR-0015's
no-preread stance) so a forward can coexist with tls-termination on one port, and to **add an
ingress-level health tier**.

## SNI-preread coexistence (collision-triggered, per listen port)

Per `(ingress host, listen)` there may be N `tls-termination` routes (and, on 443, apps) **plus at
most one `forward`**. A port is **mixed** when it carries both a TLS consumer (a tls-termination
route or an app) and the one forward. Only a mixed port changes shape; every other port renders
exactly as before, so hosts without a collision get a byte-identical config.

On a mixed port the public socket moves into `stream{}`: a `map $ssl_preread_server_name` sends every
known SNI to an internal `127.0.0.1` terminator and the unknown SNI to the forward backend (the
`default`). The `http{}` tls-termination/app servers for that port move from `listen <p> ssl` to
`listen 127.0.0.1:<loopback> ssl proxy_protocol`; the `stream` server reads the SNI and
`proxy_pass`es to the mapped upstream with `proxy_protocol on`, so both the terminators and the
forward backend learn the real client address. `http{}` recovers it via `set_real_ip_from 127.0.0.1;
real_ip_header proxy_protocol;`. Loopback ports are assigned deterministically from a reserved base
(`nginx.LoopbackBase` = 11443), one per mixed public port.

**Why one forward per port:** the forward owns its own TLS, so inforge does not know its SNI — it is
the map `default`, and a `map` has exactly one default. tls-termination routes still share a port
freely (SNI demux); only the passthrough is single-per-port. This is the only rule the architecture
forces; a forward on its own dedicated port stays a plain `stream` server, any number per host.

## Health tier (ingress-level public port, plain HTTP, strict Host)

A service may declare `health_probes_port` — the **backend** port it serves health checks on. The
**ingress** declares a single public `health_probes_port` (default **81**), opened to `0.0.0.0/0`
only when at least one referencing service declares its own. nginx surfaces health as plain-HTTP
`http{}` servers on that port, demuxed **strictly** by `server_name` (the service's canonical
`naming.ServiceFQDN`, the request `Host`) and reverse-proxied to `backend:<health_probes_port>`. A
missing/wrong Host returns 404 — there is no `default_server`, so the mapping is unambiguous across
regions. Health is plain HTTP (no ACME cert dragged into probes) and shares the `http{}` block; an
ingress with only health endpoints renders a minimal `http{}` (no ACME issuer, no `:80` server).

The same field name `health_probes_port` is used on both the ingress (public listener) and the
service (backend port) for symmetry.

## Validation

- A `forward` is single-service-exclusive **per port** against other forwards only; it may coexist
  with tls-termination on the same port. (`:80` is still forbidden to a forward — ACME owns it.)
- The ingress's public health port may not equal a route listen on its host, nor `80`.
- A co-located service's `health_probes_port` must differ from the ingress's public health port and
  from the service's own route targets, and must stay out of the reserved loopback range
  `[LoopbackBase, LoopbackBase+MaxMixedPorts)` (a co-located backend binds `127.0.0.1:<port>`, which
  would clash with a terminator). The same reserved-range rule applies to a co-located route target.

## Firewall

A health-declaring service opens the ingress's public health port on the ingress host
(`0.0.0.0/0`), and — cross-host — the backend health port privately to the network CIDR (exactly like
a cross-host route target).

## Considered options

- **Keep two hosts.** Rejected: a whole compute per "passthrough vs terminate" split is the cost
  `ssl_preread` exists to remove.
- **Always route 443 through `ssl_preread`.** Rejected: an extra loopback hop and a large diff to
  every existing rendered config for the common no-forward case. Collision-triggered keeps non-mixed
  hosts byte-identical.
- **Let a forward declare an SNI (multiple forwards per port).** Rejected: the forward owns its TLS,
  so its SNI is not inforge's to know; it is the catch-all default, and there is one default.
- **TLS-terminate the health port / one health port per service.** Rejected: probes are plain HTTP
  and a single Host-demuxed port is simpler for an external load balancer than a port per service.

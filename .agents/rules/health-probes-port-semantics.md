# health_probes_port: same field name on both service and ingress, plain HTTP, strict Host demux

`health_probes_port` appears on two distinct specs with distinct roles:

- **`ServiceSpec.HealthProbesPort`** — the **backend** port the service binds and serves health
  checks on (optional; omitting means no health endpoint for that service).
- **`IngressSpec.HealthProbesPort`** — the **public listener** port nginx exposes on the ingress
  host. Defaults to `81` (`types.DefaultHealthProbesPort`; normalized by the loader via
  `NormalizeIngress`; always resolvable via `IngressSpec.EffectiveHealthProbesPort()`). Opened only
  when at least one referencing service declares its own `health_probes_port`.

nginx renders a plain-HTTP `http{}` server on the public health port for each `IngressHealth` entry,
matched **strictly** by `server_name` (the service's canonical `naming.ServiceFQDN`, used as the
request `Host`). There is **no `default_server`** — a wrong or absent Host gets a 404.
`IngressHealth.Backend` is resolved like a route's target: `127.0.0.1` for a co-located service or
the backend's private IP for a cross-host service.

## Validation invariants

- A co-located service's backend `health_probes_port` must differ from the ingress's public health
  port (both would bind on the same host, on all interfaces in the public case).
- The service's backend `health_probes_port` must not overlap with its own route targets, with any
  other service's route targets on the same backend host, or with a public route listen port on the
  same backend host.
- The co-located service's backend `health_probes_port` must stay out of the reserved loopback range
  `[nginx.LoopbackBase, nginx.LoopbackBase+nginx.MaxMixedPorts)` (see rule
  `.agents/rules/reserve-loopback-range-for-preread-terminators.md`).
- The ingress's public health port must not be `80` (ACME HTTP-01 owns `:80`), must not match any
  route listen port on its host, and must also stay out of the reserved loopback range.

## Firewall

The ingress host opens its public health port to `0.0.0.0/0` when any referencing service declares
a backend `health_probes_port`. A cross-host backend opens its `health_probes_port` privately to the
network CIDR (`FirewallPorts.PrivateSourceCIDR`) — exactly as cross-host route targets are handled.
Co-located health ports need no firewall rule (loopback is not filtered).

## Applies to

`internal/types/types.go` (`IngressSpec.HealthProbesPort`, `ServiceSpec.HealthProbesPort`,
`IngressHealth`, `DefaultHealthProbesPort`, `EffectiveHealthProbesPort`);
`internal/loader/loader.go` (`NormalizeIngress` — normalizes to 81);
`internal/validate/validate.go` (`checkIngress`, `checkService`, `ingressHealthPort` map);
`program/program.go` (`ingressHealthByHost`, `ingressHealthPortByHost`, `firewallPlanByHost`,
`realizeIngress`); `internal/nginx/config.go` (`Render`).

## Why

The same field name on both specs is intentional symmetry: one name, two roles (backend port vs.
public port). This can be confusing; the distinction is always "service = backend, ingress = public."
Plain HTTP avoids dragging the ACME cert into probes, and Host-based demux on a single shared port
is simpler for an external load balancer than one port per service.

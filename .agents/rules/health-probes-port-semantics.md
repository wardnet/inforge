# health_probes_port: same field name on three specs, plain HTTP, strict Host demux, declared paths only

`health_probes_port` appears on three specs with two distinct roles:

- **`ServiceSpec.HealthProbesPort`** — the **backend** port the service binds and serves health
  checks on (optional; omitting means no health endpoint for that service). Setting it REQUIRES
  ≥1 `ServiceSpec.HealthProbePaths` entry (exact paths; the listener is allowlist-only, ADR-0034).
- **`IngressSpec.HealthProbesPort`** — the **public listener** port nginx exposes on the ingress
  host. Defaults to `81` (`types.DefaultHealthProbesPort`; normalized by the loader via
  `NormalizeIngress`; always resolvable via `IngressSpec.EffectiveHealthProbesPort()`). Opened only
  when at least one referencing service declares its own `health_probes_port`.
- **`GatewaySpec.HealthProbesPort`** — the ingress twin on the north-south gateway (ADR-0034): the
  public listener its host exposes for its **listed** services' health. Same default-81 scheme
  (`NormalizeGateway` / `GatewaySpec.EffectiveHealthProbesPort()`). Only an **ingress-less**
  gateway-listed service joins the gateway tier — a service with both keeps its health at the
  ingress (D12: one canonical health address, or the ServiceFQDN A record would derive at two
  hosts). `GatewaySpec.HealthProbePaths` is a different thing entirely: the gateway's OWN liveness
  paths, answered `200 "ok"` by nginx on the **443** server (edge liveness over the daemon TLS path).

nginx renders a plain-HTTP `http{}` server on the public health port for each `IngressHealth` entry,
matched **strictly** by `server_name` (the service's canonical `naming.ServiceFQDN`, used as the
request `Host`). There is **no `default_server`** — a wrong or absent Host gets a 404. Within a
matched server only the service's declared `health_probe_paths` are proxied (`location =` per path);
anything else 404s. `IngressHealth.Backend` is resolved like a route's target: `127.0.0.1` for a
co-located service or the backend's private IP for a cross-host service (the gateway tier reuses
the same substitution via `resolveGatewayHealthServices` — the single resolver its nginx entries,
firewall rules, and DNS records all consume).

## Validation invariants

- `health_probes_port` without `health_probe_paths` (and the converse) is an error; paths are exact
  absolute paths in the shared `pathglob` charset (`pathglob.CheckExact` — `[A-Za-z0-9._-]` segments,
  no globs/`..`; a `?`/`#`/`{` would render a location nginx can never match), unique. The renderers
  fail LOUD on a pathless health entry / pathless mesh callee (never a full-open fallback) —
  allowlist-only holds at the enforcement layer even on an unvalidated deploy.
- A service exposing health must name an ingress OR be listed in the scope gateway's `services:`.
- A co-located service's backend `health_probes_port` must differ from the surfacing tier's public
  health port (ingress or gateway — both would bind on the same host). On the gateway host it must
  also avoid `443`/`80` — the gateway's nginx holds them unconditionally (daemon TLS + ACME) and they
  are NOT in `portUsersByHost` (that map carries only route listen ports).
- The reserved ssl_preread loopback range is checked wherever the BACKEND host runs an edge nginx
  (`hostRunsEdgeNginx`: an ingress or the scope gateway) — not just when co-located with the
  surfacing tier; the terminators bind where the edge nginx runs.
- The service's backend `health_probes_port` must not overlap with its own route targets, with any
  other service's route targets on the same backend host, or with a public route listen port on the
  same backend host. (Registered in `targetUsersByHost` UNGATED on the ingress FK — an ingress-less
  gateway-listed service binds it all the same.)
- The co-located backend port must stay out of the reserved loopback range
  `[nginx.LoopbackBase, nginx.LoopbackBase+nginx.MaxMixedPorts)` (see rule
  `.agents/rules/reserve-loopback-range-for-preread-terminators.md`).
- The ingress's public health port must not be `80` (ACME HTTP-01 owns `:80`), must not match any
  route listen port on its host, and must stay out of the reserved loopback range. The gateway's
  additionally must not be `443` (daemon TLS lives there).
- A gateway and an ingress sharing one host must declare **equal** effective health ports — the
  render takes ONE health port per host (D13).

## Firewall + DNS

The surfacing host (ingress or gateway) opens its public health port to `0.0.0.0/0` when any
referencing/listed service declares a backend `health_probes_port`. A cross-host backend opens its
`health_probes_port` privately to the network CIDR (`FirewallPorts.PrivateSourceCIDR`) — exactly as
cross-host route targets are handled. Co-located health ports need no firewall rule (loopback is
not filtered). A health-declaring service gets its ServiceFQDN A record derived even with no routes:
at the ingress host (ingress tier) or the gateway host (gateway tier) — `derivedRecords` consumes
the same resolvers as nginx and the firewall so the three cannot drift.

## Applies to

`internal/types/types.go` (`IngressSpec`/`GatewaySpec`/`ServiceSpec` health fields, `IngressHealth`,
`DefaultHealthProbesPort`, both `EffectiveHealthProbesPort`);
`internal/loader/loader.go` (`NormalizeIngress`, `NormalizeGateway`);
`internal/validate/validate.go` (`checkIngress`, `checkGateway`, `checkService`, `checkExactPaths`,
`ingressHealthPort`/`gatewayServiceTargets`/`gatewayHostKey`/`gatewayHealthPort` ctx state);
`program/program.go` (`ingressHealthByHost`, `resolveGatewayHealthServices`, `gatewayHealthByHost`,
`ingressHealthPortByHost`, `firewallPlanByHost`, `derivedRecords`, `realizeIngress`);
`internal/nginx/config.go` (`Render`, `healthServer`, `gatewayServer`).

## Why

The same field name on the surfacing specs is intentional symmetry: one name, two roles (backend
port vs. public port); "service = backend, ingress/gateway = public". Plain HTTP avoids dragging
the ACME cert into probes, and Host-based demux on a single shared port is simpler for an external
load balancer than one port per service. Declared probe paths keep the health listener from being a
side door onto the backend port (ADR-0034); the gateway's own liveness rides 443 instead so cert
expiry and TLS breakage are observable.

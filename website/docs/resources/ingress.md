---
sidebar_position: 7
---

# Ingress

An **Ingress** is the standalone, shared proxy tier (nginx) that fronts apps and per-service web/SNI
routes under a domain. It is a sibling of [Network](./network) — a thing other resources reference —
not a workload: it references a [Compute](./compute) `host` by name **in the same scope** (exactly
like `service.host`) and reuses that host's provisioning, firewall, cloud-init, and SSH. It carries
**no provider** of its own — it inherits its host's.

The ingress is one of two **north-south** (external-facing) tiers. Its sibling is the
[Gateway](./gateway) — the public edge external **daemons** HTTPS into, which routes each path to a
service through the east-west mesh. Both are public nginx server blocks and can share one host (they
merge into a single nginx there); the ingress fronts apps/web, the gateway fronts the daemon API.

An ingress lives in a folder under `regional/ingress/<name>/` (or `global/ingress/<name>/`):

```
regional/ingress/edge/
  manifest.yaml       # required — the ingress spec
```

## Schema

`manifest.yaml`:

```yaml
name: edge                 # required — ingress name (unique within the scope)
container: bridge          # required — grouping label
host: bridge               # required — FK -> compute name (same scope); the vm nginx runs on
health_probes_port: 81     # optional — public port for service health checks (default 81)
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Ingress name. Unique within the scope. |
| `container` | string | Yes | Grouping label (tags, like other resources). |
| `host` | string | Yes | **Name** of the Compute resource (same scope) this ingress runs on. Must be a single-instance `vm`. A `global/` prefix is rejected — a global ingress is declared in the global slice itself. **At most one ingress per host** — the derived nginx config and firewall are per-host. |
| `health_probes_port` | int | No | Public port nginx exposes service [health checks](#health-probes) on. Defaults to `81`. Opened to the internet only when a referencing service declares its own `health_probes_port`. Must not be `80` or a route `listen` port on this host. |

## What it serves

The nginx/routing config is **not** declared on the ingress — it is **derived at deploy** from the
services that reference it. A [service](./service) names an ingress via its `ingress:` foreign key and
contributes its `routes:`; the ingress nginx terminates ACME TLS (Let's Encrypt, HTTP-01) or
L4-forwards each route, proxying to the backend over loopback (co-located) or the private network
(cross-host). See [Service → Ingress and routes](./service#ingress-and-routes).

A service's public FQDNs (the auto-derived `<svc>.svc` name and any `vanity`) resolve to the **ingress
host's** public IP, not the backend's. A cross-host backend's `target` ports are opened only to the
private network; only the ingress host exposes the public `listen` ports.

A `forward` route may **share a `listen` port** with `tls-termination` routes on the same ingress: nginx
inspects the TLS SNI without terminating (`ssl_preread`) and routes each known SNI to its terminator and
the single unknown SNI to the forward backend. See [Service → Types](./service#types).

## Health probes

When a [service](./service#health-probes) declares a `health_probes_port`, the ingress exposes a single
public health port (the ingress's `health_probes_port`, default `81`) and reverse-proxies each probe to the
right backend, demuxed **by request `Host`** (the service's `<svc>.svc` FQDN). The endpoint is **plain
HTTP**, and the port is opened to the internet only when at least one referencing service declares its own
`health_probes_port` — an ingress with no health-exposing services never opens it.

```yaml
# ingress
health_probes_port: 81     # public; defaults to 81 when omitted
```

A probe is `GET http://<ingress-host>:81/healthz` with `Host: <svc>.svc.<env>.<slug>.<base>`; a wrong or
missing `Host` returns `404` (each backend is matched strictly, with no catch-all). The port must not be
`80` (ACME owns it), a route `listen` port on the host, or fall in nginx's internal `ssl_preread` loopback
range.

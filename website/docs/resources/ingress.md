---
sidebar_position: 7
---

# Ingress

An **Ingress** is the standalone, shared proxy tier (nginx) that fronts services (and, in a later
slice, apps) under a domain. It is a sibling of [Network](./network) — a thing other resources
reference — not a workload: it references a [Compute](./compute) `host` by name **in the same scope**
(exactly like `service.host`) and reuses that host's provisioning, firewall, cloud-init, and SSH. It
carries **no provider** of its own — it inherits its host's.

An ingress lives in a folder under `regional/ingress/<name>/` (or `global/ingress/<name>/`):

```
regional/ingress/edge/
  manifest.yaml       # required — the ingress spec
```

## Schema

`manifest.yaml`:

```yaml
name: edge            # required — ingress name (unique within the scope)
container: bridge     # required — grouping label
host: bridge          # required — FK -> compute name (same scope); the vm nginx runs on
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Ingress name. Unique within the scope. |
| `container` | string | Yes | Grouping label (tags, like other resources). |
| `host` | string | Yes | **Name** of the Compute resource (same scope) this ingress runs on. Must be a single-instance `vm`. A `global/` prefix is rejected — a global ingress is declared in the global slice itself. |

## What it serves

The nginx/routing config is **not** declared on the ingress — it is **derived at deploy** from the
services that reference it. A [service](./service) names an ingress via its `ingress:` foreign key and
contributes its `routes:`; the ingress nginx terminates ACME TLS (Let's Encrypt, HTTP-01) or
L4-forwards each route, proxying to the backend over loopback (co-located) or the private network
(cross-host). See [Service → Ingress and routes](./service#ingress-and-routes).

A service's public FQDNs (the auto-derived `<svc>.svc` name and any `vanity`) resolve to the **ingress
host's** public IP, not the backend's. A cross-host backend's `target` ports are opened only to the
private network; only the ingress host exposes the public `listen` ports.

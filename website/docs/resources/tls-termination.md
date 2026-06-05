---
sidebar_position: 7
---

# TLS termination

A **TLS termination** resource declares a host-level terminator: a capability the compute provider
realizes on a host to terminate inbound TLS and reverse-proxy to the services running there. On
Hetzner this is realized by [Caddy](https://caddyserver.com/) (ACME / Let's Encrypt); another
provider could realize the same resource with a managed load balancer + ACM.

Per-service [ingress](./service#ingress) feeds this terminator: for each service on the host that
declares `ingress`, the terminator writes one vhost that terminates TLS for the service's hostname and
reverse-proxies to its local port.

## Schema

```yaml
name: edge               # required
container: bridge        # required
provider: hetzner        # required — the provider that realizes the terminator
compute: bridge-01       # required — specKey of the host VM the terminator runs on
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Resource name. |
| `container` | string | Yes | Grouping label. |
| `provider` | string | Yes | The provider that realizes the terminator (e.g. `hetzner` → Caddy). |
| `compute` | string | Yes | specKey of the Compute VM the terminator runs on. Must resolve to a `kind: vm` host. |

## Relationship to ingress

A terminator and a service's ingress are two halves of one feature:

- The **terminator** (this resource) is installed once per host and owns the host's TLS / cert
  lifecycle.
- A service's **`ingress`** (a field on the [Service](./service#ingress)) contributes one vhost to the
  terminator on its host.

A service that declares `ingress` must have a `tls-termination` resource on its host — validation
fails otherwise. A host may have a terminator with no services pointing at it yet (it is then idle
until a service declares ingress).

## Example

```yaml title="resources/prd/us-east-1/tls-termination/edge.yaml"
name: edge
container: bridge
provider: hetzner
compute: bridge-01
```

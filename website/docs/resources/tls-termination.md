---
sidebar_position: 7
---

# TLS termination

A **TLS termination** resource declares a host-level terminator: a capability the compute provider
realizes on a host to terminate inbound TLS and reverse-proxy to the services running there. On
Hetzner this is realized by [Caddy](https://caddyserver.com/) (ACME / Let's Encrypt); another
provider could realize the same resource with a managed load balancer + ACM.

Per-service [ingress](./service#ingress) feeds this terminator: each service on the host that declares
`ingress` contributes one **SNI route**. A route either **terminates** TLS (ACME cert + reverse-proxy
to a local port) or **passes through** the raw TLS to a backend that owns its own certificate. A
single per-host **catch-all** route forwards every unmatched SNI to a dispatcher.

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
| `compute` | string | Yes | specKey of the Compute VM the terminator runs on. Must resolve to a `kind: vm` host that declares a [`deploy_user`](./compute) (see below). |

## Realization

On Hetzner the terminator is realized over SSH at deploy time as the host's `deploy_user`. The Caddy
configuration is just Hetzner's translation of the provider-agnostic route set; another provider could
realize the same routes with a managed load balancer. There are two realization paths:

- **Terminate-only host** (no passthrough/catch-all route): inforge installs the stock Caddy and writes
  a base Caddyfile that imports one `conf.d/<service>.caddy` vhost per service, then reloads. Adding a
  service adds a vhost file; removing one deletes it.
- **Host with any passthrough/catch-all route**: inforge installs a Caddy build that includes the
  [layer4](https://github.com/mholt/caddy-l4) module (downloaded from Caddy's build service) and writes
  a single native-JSON config. A `layer4` listener wrapper on `:443` inspects the TLS ClientHello and
  raw-proxies passthrough/catch-all SNIs to their local ports (optionally with the PROXY protocol);
  terminate SNIs fall through to Caddy's normal TLS termination + reverse-proxy on the same listener,
  so terminate behavior (including ACME and the real client IP) is unchanged. The catch-all matches
  every SNI not claimed by a terminate route.

Both paths are idempotent and re-runnable. Because realization happens over SSH as the deploy user,
the terminator's host **must** declare a
[`deploy_user`](./compute) — validation fails otherwise, so the gap is caught before deploy rather
than at `pulumi up`.

## Relationship to ingress

A terminator and a service's ingress are two halves of one feature:

- The **terminator** (this resource) is installed once per host and owns the host's TLS / cert
  lifecycle.
- A service's **`ingress`** (a field on the [Service](./service#ingress)) contributes one SNI route
  (terminate, passthrough, or the catch-all) to the terminator on its host.

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

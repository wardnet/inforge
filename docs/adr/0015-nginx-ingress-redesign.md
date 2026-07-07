---
status: accepted
date: 2026-06-09
issue: "#77"
---

# Host ingress is nginx with typed per-listener routes (forward / tls-termination)

> _Note: the "no host-level resource; realization is driven by ingress" decision was superseded by the standalone ingress tier of [ADR-0026](0026-ingress-tier-and-origin-app-serving.md) (and extended by [ADR-0027](0027-sni-preread-coexistence-and-health-probes.md))._

Until now the host ingress proxy was **Caddy**, declared by a host-level `tls-termination` **resource**
(its own `tls-termination/` YAML type), and a service's `ingress` entries were modelled around a single
implicit `:443` listener: each entry carried a `port` (the local upstream), a `tls` mode (`terminate` |
`passthrough`), an optional `catchall`, and an optional `proxy_protocol`. Caddy terminated ACME TLS for
the named routes (path A: a base `Caddyfile` importing one `conf.d/<svc>.caddy` vhost per service) and,
whenever any passthrough/catch-all route appeared, switched the whole host to a hand-built **layer4**
JSON config (path B: a custom Caddy binary downloaded from caddyserver.com with `github.com/mholt/caddy-l4`,
plus a systemd override).

Three forces broke that model:

1. **The listener is not always `:443`, and a port is not always TLS.** A service needs to bind an
   arbitrary public port, and to express a **raw L4 forward** that preserves the client IP for a backend
   buffered behind the proxy on a hot path (the bridge's daemon). That is `proxy_protocol` on a plain TCP
   forward — a first-class need, not a TLS variant.
2. **Caddy makes the L4 forward a weak trade.** L4 forward with `proxy_protocol` is core in nginx's
   `stream` module; in Caddy it requires the third-party `caddy-l4` custom build and a systemd override
   dance — the wrong trade for a plain reverse proxy.
3. **The `tls-termination` resource was redundant.** It existed only to *trigger* the proxy install on a
   host. With typed routes, a service already declares everything the proxy needs; a second resource to
   say "and run the proxy here" is bookkeeping the model can derive.

We decided to **replace Caddy with nginx**, make ingress entries **typed per-listener routes**, and
**remove the `tls-termination` resource** — nginx is realized wherever a service has ingress.

## Typed routes; nginx always fronts

Each `ingress` entry declares a `type`, a public `listen` port, and a loopback `target` port (all
required; `listen` must differ from `target`):

- **`tls-termination`** — nginx (`http` context) terminates ACME TLS for the service's SNIs (the
  auto-derived `<svc>.svc` FQDN plus any `vanity` entries, demuxed by `server_name`) and reverse-proxies
  cleartext to `localhost:<target>`. **Several services may share one `listen` port** — nginx demuxes by
  SNI — so the per-host sharing rule is per `(host, listen)`.
- **`forward`** — nginx (`stream` context) forwards the raw L4 connection on `listen` to
  `127.0.0.1:<target>` with `proxy_protocol on` (always — the buffered-backend client-IP case is the
  reason the type exists). The backend owns its own TLS. A `forward` port is **single-service-exclusive**
  (nginx `stream` cannot demux it).

A host with **any** ingress runs nginx as its **sole public entry point**: services bind
`127.0.0.1:<target>` and nginx fronts them. There is no direct-bind path — `ingress` always means
"nginx-fronted." A truly raw public port (no proxy, no TLS, no remap) is a `compute.firewall.inbound`
rule, not ingress. `listen` must differ from `target` because nginx binds `*:<listen>` (all interfaces,
loopback included), so the service cannot also bind `127.0.0.1:<listen>`.

The old `port` field is renamed to `target`; `tls`, `catchall`, and `proxy_protocol` are removed.
`vanity` remains, valid only on `tls-termination` (a forward has no SNI). The bridge's sophisticated SNI
passthrough / catch-all is the **bridge daemon's** job, not inforge's proxy, so inforge's nginx needs no
`ssl_preread` and no catch-all route.

## No host-level resource: realization is driven by ingress

The `tls-termination` resource type is removed (no `tls-termination/` directory, schema, loader, or
validation). `program.realizeIngress` iterates the hosts that have ingress routes (from `routesByHost`)
and asks each host's **compute provider** to realize nginx — services carry no provider, so the proxy
runs with the host's. An ingress host must declare a `deploy_user` (already required for any service, so
inforge can SSH to provision it). The provider interface is invoked per host (`Realize(hostKey, host,
deployUser, routes, …)`), not per resource.

## ACME via the native nginx-acme module (HTTP-01 only)

nginx terminates TLS with the official **`nginx-module-acme`** (F5 OSS), installed from the **nginx.org
mainline apt repo** (the module ships there from nginx ≥ 1.29.1). The rendered `http` block carries a
`resolver` (a public resolver, since stock images run no local DNS on `:53`), an `acme_issuer` (Let's
Encrypt production directory) with a `state_path`, an `acme_shared_zone`, and per-service servers
referencing `ssl_certificate $acme_certificate`. A `:80` server answers the **HTTP-01** challenge (the
only challenge inforge uses) and redirects everything else to HTTPS; the module intercepts
`/.well-known/acme-challenge/` before location matching, so the catch-all `return 301` does not swallow
challenges. certbot is the documented fallback only.

## Firewall is derived, not hand-maintained

A host's inbound rules are derived: SSH `22` (always) + the **union of every service's ingress `listen`
ports** on that host + `:80` **iff** the host has at least one `tls-termination` entry (ACME HTTP-01).
Explicitly declared `firewall.inbound` rules are unioned on top (for raw ports not fronted by nginx). The
program computes the per-host port set in an early pass (before compute creation) and threads it into the
compute provider, so the firewall stays a pure consumer.

## Validation

`listen` and `target` are required on every entry, and `listen` != `target`. A `listen` shared by ≥2
services must be `tls-termination` (since `forward` is single-service-exclusive, and the only
non-terminating type). `vanity` is rejected on a `forward`. A `forward` on `:80` coexisting with any
`tls-termination` on the same host is an error (ACME owns `:80`). No "host needs a tls-termination
resource" rule remains — the resource is gone.

## Rendering

The Hetzner provider renders the **whole `nginx.conf`** (main + `events` + `http` + `stream`) via the
typed AST builder `github.com/nginxinc/nginx-go-crossplane` (pure Go — `CGO_ENABLED=0` stays intact),
not string templating. `load_module` (main context) and the `stream` block cannot live in a `conf.d`
drop-in included inside `http{}`, so owning the whole file is also what makes the output deterministic.
Servers are sorted by listen port then service; `http{}` (with the `:80` ACME/redirect server) is emitted
only when the host terminates TLS, `stream{}` only when it has a forward route. Realization writes the
file, runs `nginx -t`, and reloads — so a bad render fails the deploy loudly rather than reloading into a
broken state. wardnet's `resources/prd` has no service ingress yet, so there is no existing rendered
config to diff against — the regression guarantee lives in inforge's own golden tests (`internal/nginx`).

## Considered options

- **Keep Caddy, add `caddy-l4` for the forward.** Rejected: a custom-build + systemd-override dependency
  to obtain in a third-party module what nginx has in core `stream`, for a plain TCP forward.
- **Keep the implicit `:443` listener, add a port field.** Rejected: it cannot express a service on an
  arbitrary public port, nor two services sharing a TLS port, nor a non-TLS forward.
- **Keep the `tls-termination` resource as the proxy trigger.** Rejected: it forced a plain `forward`
  host (the self-fronting bridge) to declare a resource purely to get nginx installed — bookkeeping the
  model derives from ingress presence.
- **Allow `ingress` to bind a port directly (no proxy).** Rejected: it split "ingress" into proxied and
  unproxied paths and let a service expose itself bypassing the single audited entry point. Raw ports
  belong on `compute.firewall.inbound`; `ingress` is uniformly nginx-fronted.
- **nginx with typed per-listener routes, resource-less, native ACME (chosen).** Core `stream` forward
  with `proxy_protocol`, SNI demux on shared TLS ports via `server_name`, ACME via the native module, a
  single audited public entry point, all in one deterministically rendered config.

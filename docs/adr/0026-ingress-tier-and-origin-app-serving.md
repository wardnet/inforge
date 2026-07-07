---
status: accepted
date: 2026-06-17
issue: "#126"
---

# Front-ends are origin-served by a standalone ingress tier, not a CDN edge

Slice 1 (schema-only) modelled a front-end (static SPA) as an `app` resource attached to a `cdn`
resource, whose provider came from a per-scope **cdn authority** in `regions.yaml` — the intent being to
serve apps from the Cloudflare Workers Static-Assets edge, with the bundle delivered to the edge over a
REST path at release time. Two things broke that model before any realization landed:

1. **Free Cloudflare Universal SSL only covers one subdomain level (`*.<base>`).** A deep app host like
   `my.use1.wardnet.network` is two labels below the base domain, so the edge serves it with a certificate
   error. Edge serving of regionally-scoped app hosts is not viable on the free tier, and a paid
   wildcard-of-wildcards (ACM / advanced certificate) is a cost and account-coupling we rejected.
2. **We already run nginx.** ADR-0015 put an nginx ingress proxy on service hosts (origin-terminated
   Let's Encrypt via the native `nginx-module-acme`, HTTP-01). Origin termination has **no subdomain-depth
   limit** and nginx serves a static SPA bundle natively (`root` + `try_files`), which a CDN-edge worker
   path does not buy us anything over.

We decided to **serve apps from our own nginx at the origin** (Let's Encrypt, HTTP-01, no depth limit) and
to **promote ingress to a first-class, shared proxy tier** that fronts **both apps and services**.

## Ingress becomes a standalone resource referencing a compute host

Previously (ADR-0015) there was deliberately **no** host-level ingress resource: nginx realization was
driven implicitly by whichever services had `ingress` routes on a host. That is now reversed. `ingress` is
a **standalone declarative resource** (`regional|global/ingress/<name>/manifest.yaml`) with `name`,
`container`, and a `host:` foreign key to a compute name **in the same scope** — exactly like
`service.host`. It reuses the compute machinery (provisioning, firewall, cloud-init, SSH) and carries no
provider of its own: it inherits its host's. The nginx/routing config it serves is **not** declared on the
resource; it is **derived at deploy** from the apps (and, from slice B, the services) that reference this
ingress. This **supersedes the "realization is driven by ingress, no host-level resource" decision of
ADR-0015**: ingress hosts are now named resources, and the proxy tier is shared rather than per-service-host.

An `app` now references an `ingress` by name (`ingress:` FK, same-scope, `global/` rejected — see the
`app-ingress-fk-is-same-scope-only` rule), replacing the retired `cdn:` FK.

## App FQDN is the clean dotted form (no env segment)

An app's public FQDN is `<subdomain>.<base>` (global) or `<subdomain>.<slug>.<base>` (regional) —
`naming.AppFQDN`. It is deliberately **flatter than `ServiceFQDN`/`RecordFQDN`**, which carry an `<env>` and
a type segment: each environment uses its own `base_domain`, so the env is already encoded in the base and
an app is a public-facing site, not an internal derived record. The app's DNS A record is a **grey-cloud**
(unproxied) record pointing at the ingress host's public IP, created through the existing `dns:` authority
and `CreateRecord` — there is no separate cdn authority anymore.

## The releases store is canonical for app delivery

App bundles are delivered at release time by **filesystem delivery over SSH to the ingress host** (atomic
symlink swap, slice D), with the artifact pulled by content hash from the **R2 releases store** (ADR-0016).
The Cloudflare-Workers REST delivery path is dropped. The releases store is the single canonical source of
release artifacts for both services and apps.

## Retired in this pivot

- The `cdn` resource type (`CdnSpec`, `schemas/cdn.json`, `regional|global/cdn/`).
- The per-scope **cdn authority** in `regions.yaml` (`regions[].cdn`, `global.cdn`, `regions.CdnAuthority`)
  and its availability checks (`cdnConsumerPaths`, the cdn branches in `checkProviderAvailability` /
  `checkGlobalProviderAvailability`). An ingress inherits its compute host's provider, which is already
  covered by the compute provider-availability pass, so no dedicated authority/availability machinery is
  needed.

## Scope of this slice (A)

This ADR is realized incrementally. **Slice A is schema-only and behavior-free**: it adds the `ingress`
resource type, switches `app.cdn` → `app.ingress`, removes the `cdn` type and cdn authority, adds
`naming.AppFQDN`, and the same-scope FK + name/subdomain collision validation. `ServiceSpec` and all
deploy-time realization are untouched. Later slices add ingress realization + service migration (B), app
static serving + DNS + descriptor (C), and app release delivery + CLI (D).

> Naming note: the unqualified `IngressSpec` type name is, for slice A, still the inline per-service
> routing-entry struct embedded in `ServiceSpec`. The new resource temporarily carries the
> `IngressResourceSpec` name (mirroring `PKIResourceSpec`); slice B renames the route struct to `RouteSpec`
> and this resource to `IngressSpec`.

---
sidebar_position: 9
---

# App

An **App** is a static front-end (a single-page application or any set of static assets) served by an
[ingress](./ingress)'s nginx directly from disk. It is a sibling of [service](./service) — a workload —
but has **no backend process**: the ingress terminates ACME TLS for the app's single public FQDN and
serves files from an on-host document root that the release path swaps atomically.

Apps are **origin-served by our own nginx** (Let's Encrypt, HTTP-01) rather than a CDN edge, so an app's
DNS record is grey-cloud (`Proxied=false`) — the ACME challenge must reach the origin.

An app lives in a folder under `regional/app/<name>/` (or `global/app/<name>/`):

```
regional/app/dashboard/
  manifest.yaml       # required — the app spec
```

```yaml title="regional/app/dashboard/manifest.yaml"
name: dashboard
container: web
ingress: edge          # FK -> ingress resource (same scope) that serves this app
subdomain: app         # -> app.<slug>.<base> (regional) / app.<base> (global)
spa: true              # any non-file path serves index.html (deep-link fallback)
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | App resource name (unique per scope). |
| `container` | string | Yes | Logical container/grouping, like every resource. |
| `ingress` | string | Yes | **Name** of the [ingress](./ingress) resource (same scope) that serves this app. The app inherits the ingress's provider (its compute host's) and declares none of its own. A `global/` reference is rejected — an app served from a global ingress is declared in the global slice itself. |
| `subdomain` | string | Yes | Public subdomain the app is served on. The FQDN is the flat form `<subdomain>.<slug>.<base>` (regional) / `<subdomain>.<base>` (global); an ephemeral env inserts its slug. Must be unique among apps in the scope, must not collide with a [gateway](./gateway) subdomain, and must not be the reserved `ingress` label (the scope's [ingress host record](./ingress#the-ingress-dns-name)). |
| `spa` | bool | No | When `true`, any request path that does not map to a file serves `index.html` (single-page-app deep-link fallback via nginx `try_files`); when `false`, a missing path is `404`. |

## How an app is served and released

At deploy the ingress nginx gains one `server { listen 443 ssl; … root <dir>/current; }` block per app,
sharing the ingress's ACME issuer and, when a `forward` route also uses `:443`, its `ssl_preread` demux.
`provisionApps` seeds a placeholder bundle and points the `current` symlink at it, so the server block
and cert provision before the first real release.

Releases are delivered by the CLI, not by `inforge deploy`:

```bash
# package a local SPA build, push it to the release store, and swap it in
inforge release app <env> <name> --bundle ./dist

# release a SHA already in the store
inforge release app <env> <name> --sha <sha>

# re-point `current` at a SHA already on the host (no re-fetch)
inforge release app <env> <name> --rollback --sha <sha>
```

Each release extracts the bundle into a per-SHA directory and atomically re-points the `current` symlink
nginx serves, then reloads nginx and garbage-collects old bundles — so a release (or `--rollback`) never
leaves the served root in a half-written state.

## See also

- [Ingress](./ingress) — the proxy tier that serves the app (its `ingress:` FK)
- [Gateway](./gateway) — the other north-south tier (the daemon API edge)
- [`inforge release`](/cli/releases) — the release store and delivery

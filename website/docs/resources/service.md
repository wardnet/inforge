---
sidebar_position: 6
---

# Service

A **Service** resource defines an application hosted on a Compute VM. inforge provisions
the host-side scaffolding (folder + systemd unit); the service repo deploys code separately
via `inforge releases` from its own CI workflow.

A service's definition lives in a folder under `regional/service/<name>/`:

```
regional/service/api/
  manifest.yaml        # required — routing, identity, ingress, host
  environment.yaml     # optional — runtime env-var contract (source DSL strings)
```

## Schema

`manifest.yaml`:

```yaml
name: api                # required
container: bridge        # required
host: bridge             # required — name of the Compute resource that hosts this service
type: raw                # required — delivery type
user: wardnet            # required — no-login system user the service runs as
pki: wardnet-mesh        # required — name of the two-tier (mesh) PKI this service is a member of
mtls_files: true          # optional — ALSO project this service's own leaf + trust bundle (raw mTLS plane only)
reload: /bin/kill -HUP $MAINPID  # optional — ExecReload command to apply a renewed mesh leaf without a restart
ingress: edge             # FK -> ingress resource (same scope) whose nginx fronts the routes below
routes:                   # optional — typed inbound routes realized on the ingress's nginx
  - type: tls-termination #   required — tls-termination | forward
    listen: 443           #   required — public port the ingress accepts traffic on
    target: 8080          #   required — backend port nginx reverse-proxies to
    vanity:               #   optional — extra public FQDNs beyond the auto-derived <svc>.svc name
      - api.{BASE_DOMAIN}
  - type: forward         #   a second route — raw L4 forward (PROXY protocol)
    listen: 853           #   required — public port
    target: 5353          #   required — backend port to forward to
health_probes_port: 8081  # optional — backend port the ingress/gateway surfaces as a health check
health_probe_paths:       # required (>=1) when health_probes_port is set — exact paths the health
  - /healthz              #   listener proxies; anything else 404s
exposed_ports:            # optional — ports opened on the host's PRIVATE network only (no ingress, no nginx)
  - { proto: tcp, port: 9444 }   #   inter-node mesh mTLS — reachable only on the host's network CIDR
  - { proto: udp, port: 51820 }  #   a peer link
mesh:                     # optional — east-west mesh exposure (see "East-west service mesh")
  port: 8080              #   loopback port this service binds to receive mesh traffic (plain HTTP)
  allowed_services: [tunneller, gateway]  # callee-side allow list: who may call this service
  public_paths:           #   path globs exposed at the internet edge (via a gateway listing this service)
    - /api/v*/tenants/**  #   ...and admitted from mesh peers
  internal_paths:         #   path globs for mesh peers ONLY — never served by a gateway
    - /admin/**
```

`environment.yaml` (optional sidecar — env-var name → source DSL string):

```yaml
SERVER_IP: ref:compute/bridge.publicIp          # an output from another resource
CF_TOKEN: env:CLOUDFLARE_API_TOKEN              # a deploy-environment variable
API_KEY: vault:API_KEY                          # a value from the git-encrypted store
LOG_LEVEL: info                                 # a literal (non-secret config) value
```

Database credentials are **not** declared here — they flow through a [grant](#grants), never a `ref:`.

## Fields

**`manifest.yaml` fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Service name. Also becomes the folder name (`/srv/wardnet/<name>`) and unit name (`wardnet-<name>.service`). |
| `container` | string | Yes | Grouping label (cloud tags, URN namespace). It has **no** bearing on secrets — those are keyed by the service. |
| `host` | string | Yes | **Name** of the Compute resource that hosts this service (e.g. `bridge`). The host must have `instance_count: 1`; a multi-instance host is a validation error. |
| `type` | string | Yes | Delivery type. Currently only `raw` (SSH-push) is supported. `container` is reserved. |
| `user` | string | Yes | No-login system user the service runs as. inforge emits `User=<name>` in the systemd unit and creates the account via SSH on first deploy; the agent drops privilege to it before exec. |
| `pki` | string | Yes | Name of the **two-tier (mesh) PKI** in `pki.enc.yaml` this service is a leaf member of. `inforge validate` checks it names an existing two-tier PKI with an intermediate for every scope the service deploys under (a global service → `global`; a regional service → every region). See [`inforge pki`](/cli/pki). |
| `mtls_files` | bool | No | **Opt-in** (default `false`): also project this service's **own** leaf + trust bundle into its tmpfs and inject the `MTLS_*_PATH` env vars — for a service running a **raw mTLS plane outside the mesh** (e.g. a node↔node forward listener on an [exposed port](#exposed-ports)). By default the per-host **mesh proxy** is the sole custodian of a service's mesh leaf and the service holds no cert material at all. See [Leaf custody](#east-west-service-mesh). |
| `reload` | string | No | `ExecReload=` command the service uses to apply a renewed leaf **without a restart** (e.g. `/bin/kill -HUP $MAINPID`, `nginx -s reload`). Only meaningful with `mtls_files: true` (only then does the service hold its own cert material); when set, `inforge pki renew`'s push reloads the unit, else restarts (a brief interruption). Must be a **single line** (it becomes one `ExecReload=` directive; a newline would inject extra unit directives). The leaf/key/bundle paths are in the `MTLS_LEAF_CERT_PATH` / `MTLS_LEAF_KEY_PATH` / `MTLS_TRUST_BUNDLE_PATH` env vars — these names are **reserved**: a service's own `environment:` may not use them. |
| `ingress` | string | When `routes` is set | **Name** of the [ingress](#ingress-and-routes) resource (same scope) whose nginx fronts this service's `routes`. The ingress host and this service's host must share a network when they differ (cross-host routing). |
| `routes` | array | No | Typed inbound routes (`tls-termination` / `forward`) realized on the referenced ingress's nginx. Each route binds a public `listen` port and a backend `target` port. See [Ingress and routes](#ingress-and-routes) below. |
| `health_probes_port` | int | No | Backend port the service serves health checks on, surfaced on a public [health port](./ingress#health-probes) (default `81`) demuxed by the service's FQDN. The health server renders on the service's `ingress:` host — or, for a service **without** an ingress that is listed in the scope [gateway](./gateway)'s `services:`, on the **gateway's** host. Requires ≥1 `health_probe_paths`. See [Health probes](#health-probes) below. |
| `health_probe_paths` | array | When `health_probes_port` is set | Exact request paths (e.g. `/healthz`) the health listener proxies to `health_probes_port` — **only** these paths; anything else is `404` (allowlist-only). Required (≥1) whenever `health_probes_port` is set. Same segment charset as path globs (`[A-Za-z0-9._-]`, no globs) — characters like `?` or `{` can never appear in a matched nginx path and are rejected. |
| `exposed_ports` | array | No | Ports the service binds that inforge opens on the host's **private network only** (never the public internet) — for peer / service-to-service traffic. Each entry is `{proto: tcp\|udp, port: 1..65535}`. Needs **no** ingress and uses **no** nginx (distinct from `routes`); it is the private sibling of [`compute.firewall.inbound`](./compute#firewall) (which is public). See [Exposed ports](#exposed-ports) below. |
| `mesh` | object | No | East-west mesh exposure: `port` (the loopback port this service binds to receive mesh traffic), `allowed_services` (the callee-side allow list of who may call it), and the declared endpoint surface `public_paths` / `internal_paths` (a mesh callee must declare ≥1 path across the two lists — see [Path-level exposure](#path-level-exposure)). Omit the whole block for a service that only makes outbound mesh calls. See [East-west service mesh](#east-west-service-mesh) below. |

**`environment.yaml` sidecar:**

An optional flat map of `ENV_VAR_NAME: <source>` entries declaring the runtime environment variables
inforge injects when the service starts. If absent the service has no environment variables. See
[Environment](#environment) below.

:::note No `provider` field
A service has **no `provider`** — it is host-managed, not realized by a cloud provider. Its secrets
need no provider configuration either: inforge resolves them at deploy time and age-encrypts them
directly to the service's host, over the same SSH connection used to provision it.
:::

## Folder layout

```
regional/service/<name>/
  manifest.yaml       # service identity, host, type, user, ingress
  environment.yaml    # optional — env-var name → source DSL
```

## Delivery types

### `raw`

The service code is delivered as a gzip payload pushed via SSH. inforge creates:

- `/srv/wardnet/<name>/` — service working directory
- `wardnet-<name>.service` — systemd unit (inforge-managed)

The service must provide a `run` executable at the top level of its payload.

### `container` (reserved)

Pull-based container deployment. Not yet implemented.

## Provisioned on-host files

For a service named `api` hosted on `bridge`, `inforge deploy` provisions:

| Path | Description |
|------|-------------|
| `/srv/wardnet/api/` | Service folder (root-owned, world-readable; the service user gets r-x) |
| `/etc/systemd/system/wardnet-api.service` | systemd unit (managed by inforge) |

Provisioning runs over SSH as the host's [`deploy_user`](./compute) — so a service's host **must**
declare one (validation fails otherwise). The unit is written, `daemon-reload`ed, and **enabled**, but
**not started**: its `ExecStart=<folder>/run` does not exist until code is released. After the first
deploy the unit exists but fails to start until [code lands](#releasing-code) — expected.

## Releasing code

`inforge deploy` provisions the scaffolding above; `inforge releases` then delivers the payload
and starts the service. Release resolves the deploy target
(host DNS, folder, unit, SSH user) live from the Pulumi stack — no descriptor file is committed —
then SSHes in as the host's deploy user, extracts the payload into the folder, and
`systemctl restart`s the unit (the first restart is the service's first real start). See the
[service release starter](/github-actions/overview#service-release-optional) for the full setup.

## Service user

Every service must declare a `user`. `inforge deploy`:

1. Emits `User=<name>` in the inforge-managed systemd unit so the service process runs as that account.
2. Creates the account with `useradd --system --shell /usr/sbin/nologin <name>` when provisioning the
   unit (idempotent).

The user is a no-login system account, **distinct from the host's `deploy_user`** (the account inforge
connects as over SSH). It is the account `inforge-agent` drops privilege to before exec, so it is
required for every service — with or without secrets.

## Environment

A service's runtime environment variables are declared in the `environment.yaml` sidecar — a flat
map of `ENV_VAR_NAME: <source>` entries. Each source is a small DSL string that says *where the
value comes from*, not the value itself:

```yaml title="regional/service/api/environment.yaml"
SERVER_IP: ref:compute/bridge.publicIp          # an output from another resource
CF_TOKEN: env:CLOUDFLARE_API_TOKEN              # a deploy-environment variable
API_KEY: vault:API_KEY                          # a value from the git-encrypted store
LOG_LEVEL: info                                 # a literal (non-secret config) value
```

The source kinds are:

| Source | Form | Where the value comes from |
|--------|------|----------------------------|
| **ref** | `ref:<database\|compute>/<name>.<output>` | A runtime output of another resource (e.g. a compute private IP). A **database credential is never a `ref:`** — it flows through a [grant](#grants). |
| **env** | `env:<VAR>` | A variable in the **deploy process environment** — e.g. a CI secret mapped to an env var in your workflow. Unset/empty fails the deploy loudly. |
| **vault** | `vault:<KEY>` | A value held **age-encrypted in git** in the env's committed store, keyed by `(service, KEY)`. Managed with the [`inforge secret`](/cli/secret) CLI. |
| **literal** | any other string | A verbatim inline value. **Plaintext in git — non-secret config only.** |

Environment variables are **service-scoped**: they belong to the service that declares them, and two
services sharing a `container` share none of them. At deploy, inforge resolves every entry (regardless of source kind), age-encrypts the
resulting map directly to the host's own SSH key as `secrets.age`, and `inforge-agent` decrypts it
locally and injects each value as an env var at start. The [`vault:`](/cli/secret) and full delivery
mechanics are covered in [Secrets](./secrets).

:::warning Literals are not secrets
A literal value (any string without a `ref:`/`env:`/`vault:` prefix) is committed **in plaintext**. Use
it for non-secret per-service config only; use `vault:`, `env:` or `ref:` for anything sensitive.
:::

Env-var names in the reserved `INFORGE_*` namespace are rejected — see
[Runtime environment](#runtime-environment).

## Grants

A **grant** is a service's declared, permissioned access to a **Grantable** resource — a
[database](./database) or a [PKI resource](./pki-resource) — materialized as the env vars the service
composes over the fields that resource publishes. Unlike a `ref:` (which only **reads** an existing
output), a grant *creates or issues* a credential: a scoped per-service DB role, or a minted
certificate. Database credentials reach a service **only** this way — `ref:database/*` is rejected.

Grants are authored as a `grants:` list on the **manifest** (topological, beside `pki:` and `ingress:`
— not in `environment.yaml`):

```yaml title="regional/service/api/manifest.yaml (grants:)"
grants:
  - resource: database/main       # <type>/<name>; a global target uses database/global/<name>
    permission: rw                # ro | rw
    outputs:
      DATABASE_URL: "{URL}"       # env var -> template over the fields the resource publishes
  - resource: pki/daemon-ca
    permission: ro
    outputs:
      CA_CERT_PATH: "{CERT}"      # a file field must stand alone (only the placeholder)
```

| Field | Description |
|-------|-------------|
| `resource` | The granted resource as `<type>/<name>` (`type` ∈ `database` \| `pki`). A global target uses a `global/` name prefix (e.g. `database/global/main`). |
| `permission` | `ro` or `rw`. Each Grantable maps it to its own domain (see below). One grant per target — `rw` subsumes `ro`. |
| `outputs` | Map of **env-var name → template** over `{FIELD}` placeholders the resource publishes for that permission. |

### What each resource publishes

| Resource | `ro` fields | `rw` fields | Field kind |
|----------|-------------|-------------|------------|
| **database** | `{USER} {PASSWORD} {HOST} {PORT} {DBNAME} {URL}` | same | **value** — composed into a string secret |
| **pki** | `{CERT}` (verify) | `{CERT} {KEY}` (issue) | **file** — resolves to a projected PEM's on-host path |

- **Database** — `ro` mints a read-only role (`CONNECT`/`USAGE`/`SELECT`); `rw` adds read/write plus
  `CREATE ON SCHEMA public` (the service owns its own migrations). The role is per-service and scoped;
  prefer `{URL}` (the already-encoded connection URI) for a DSN rather than hand-assembling
  `{USER}:{PASSWORD}@…`.
- **PKI resource** — `ro` publishes the CA certificate (`{CERT}`, to verify); `rw` adds the root
  signing key (`{KEY}`, to issue). See [PKI resource](./pki-resource).

### Field kinds

A **value field** composes a string secret (delivered like an `environment.yaml` secret). A **file
field** resolves to the on-host path of a projected PEM — so a file-field template must contain **only**
the placeholder, nothing around it (e.g. `"{CERT}"`, never `"cert={CERT}"`). The two kinds never mix in
one template.

`inforge validate` checks each grant credential-free: the target resolves to a supported Grantable of
the right shape; the permission is `ro`/`rw`; every `{FIELD}` is one the resource publishes for that
permission; a file field stands alone; and output env-var names avoid the reserved `INFORGE_*` /
`MTLS_*_PATH` namespaces and don't collide with `environment.yaml` keys or each other across the
service's grants.

## Ingress and routes

A service is exposed through a standalone **[ingress](./ingress)** resource — the shared proxy tier
(nginx) named by the `ingress:` foreign key — which fronts the service's `routes:`. nginx runs on the
**ingress's host**, not the service's: it terminates/forwards on the public `listen` port and proxies to
the backend's `target` port over **loopback** when the service is co-located with its ingress host, or
over the **private network** (the backend's private IP) when they are different hosts. A cross-host
service and its ingress must share a network (same Hetzner Network — subnets within it are mutually
routable); `inforge validate` enforces this.

Each route is one of two types: **`tls-termination`** (nginx terminates ACME TLS and reverse-proxies to
the backend) or **`forward`** (nginx forwards the raw L4 connection with the PROXY protocol). A service
may carry several routes — e.g. terminate TLS on `443` *and* forward a second protocol on `853`. A
service with any `routes:` must name the `ingress:` that fronts them.

Each route's fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | Yes | `tls-termination` or `forward`. |
| `listen` | int | Yes | Public port (1–65535) the ingress nginx accepts traffic on. **No default.** Must differ from `target` when the service is co-located with its ingress. |
| `target` | int | Yes | Backend port the service listens on; nginx reverse-proxies/forwards to it over loopback (co-located) or the private network (cross-host). |
| `vanity` | array | No | Extra public FQDNs a `tls-termination` route serves, **in addition to** the auto-derived `<svc>.svc` name (see below). Invalid on a `forward` (no SNI). |

### Types

- **`tls-termination`**: nginx owns an ACME certificate for each of the route's FQDNs (the auto-derived
  `<svc>.svc` name plus any `vanity`), terminates TLS on `listen`, and reverse-proxies HTTP to the
  backend `target`. **Several services on one ingress may share a `listen` port** — nginx demuxes by SNI
  (`server_name`).
- **`forward`**: nginx forwards the raw L4 stream on `listen` to the backend `target` with
  `proxy_protocol on` so the backend learns the real client address; the **backend owns its own TLS**.
  A `listen` port admits **at most one `forward`** (it is the single passthrough on that port).

A `forward` **may share a `listen` port with `tls-termination` routes** (e.g. several services terminating
TLS on `443` plus one service forwarding `443`). nginx inspects the TLS SNI without terminating
(`ssl_preread`) and routes each known SNI to its terminator and the **one** unknown SNI to the forward —
which is why only a single forward is allowed per port. ACME owns `:80` on the ingress host for HTTP-01
challenges, so a `forward` on `:80` still cannot coexist with a `tls-termination` on the same ingress. The
backend's `target` ports are opened only to the private network (never the internet); only the ingress host
exposes the public `listen` ports.

:::tip Raw public ports
A route always goes through the ingress nginx. To open a **raw** public port with no proxy (no TLS, no
remap, no PROXY protocol), declare it on the [Compute firewall](./compute#firewall-rules) instead — not
as a route.
:::

### Health probes

A service may expose a health endpoint with `health_probes_port` — the **backend** port it serves
health checks on — plus `health_probe_paths`, the **exact** request paths the health listener
proxies (required, ≥1, whenever the port is set). The fronting host publishes a single
[public health port](./ingress#health-probes) (default `81`) and reverse-proxies each probe to the
right backend, demuxed **by request `Host`** (the service's `<svc>.svc` FQDN). The health endpoint
is **plain HTTP** (no TLS), and the public port is opened to the internet only when at least one
fronted service declares a `health_probes_port`.

```yaml
ingress: edge
health_probes_port: 8081   # the ingress exposes this on its public health port (:81)
health_probe_paths:        # allowlist — ONLY these exact paths are proxied
  - /healthz
```

A probe reaches it as `GET http://<ingress-host>:81/healthz` with `Host: <svc>.svc.<env>.<slug>.<base>`;
a missing or wrong `Host` returns `404` (each backend is matched strictly), and so does any path
outside `health_probe_paths` (the listener is **allowlist-only** — it never exposes the backend's
whole port). `health_probes_port` must differ from the service's own route `target`s and, when
co-located, from the public health port.

**Where the health server renders** depends on what fronts the service:

- a service with an `ingress:` — on the **ingress host**, as before;
- a service with **no** `ingress:` that is listed in the scope [gateway](./gateway#health)'s
  `services:` — on the **gateway's host** (the gateway's own `health_probes_port`, default `81`),
  and the service's `<svc>.svc` DNS A record points at the **gateway host**, so probes address it by
  the same FQDN scheme;
- a service with **both** an ingress and a gateway listing keeps its health at the **ingress** (one
  canonical health address — the A record can only derive at one host).

### Exposed ports

Some ports a service binds are not public endpoints at all — they are reachable **only by peers on the
same private network** (service-to-service / node-to-node traffic). `exposed_ports` declares them:

```yaml
exposed_ports:
  - { proto: tcp, port: 9444 }   # an inter-node mesh-mTLS listener
  - { proto: udp, port: 51820 }  # a peer link
```

inforge opens each port on the host's **private-network CIDR only** — never `0.0.0.0/0`. There is **no
ingress and no nginx**: the port is realized purely as a private inbound firewall rule on the service's
own host. A service may declare `exposed_ports` with **no ingress and no routes** (a *private-only*
service is valid).

This is the **private sibling** of [`compute.firewall.inbound`](./compute#firewall): same "raw port,
no proxy" intent, but private instead of public. Use `firewall.inbound` for a port that must be open to
the internet, `exposed_ports` for one that must stay on the private network, and a `routes` entry when
nginx should front it.

Each entry is `{proto: tcp|udp, port: 1..65535}`. `tcp/N` and `udp/N` are distinct binds and may
coexist. An exposed port must not collide with the service's own route `target`s or `health_probes_port`,
with a public listen port nginx holds on that host, or with another service's backend port on the host.

:::note Reachability is the operator's contract
inforge opens the firewall rule; it does **not** provide service discovery, peer enumeration, or private
DNS. It is up to you to ensure the peers that need the port sit on the same private network and know how
to address each other.
:::

### Hostnames, DNS and certificates

A service's public domain is **derived automatically** from its name:
`<service>.svc.<env>.<slug>.<baseDomain>` (e.g. `api.svc.prd.use1.wardnet.network`). inforge creates
the DNS A-record for it (pointing at the host) on the region's
[DNS authority](../configuration/regions-yaml#dns) — there is no hand-written DNS resource.

To serve additional public names on a `tls-termination` entry, list them under `vanity`:

- a **bare token** (no dot or `{…}`) is env+region-scoped: `api` → `api.<env>.<slug>.<baseDomain>`;
- a value with a **dot or placeholder** is a literal/template FQDN: `key-broker.{BASE_DOMAIN}` →
  `key-broker.<baseDomain>`, or `key-broker.inforge.wardnet.network` used as-is;
- available placeholders: `{BASE_DOMAIN}`, `{ENV}`, `{REGION_SLUG}`.

Every vanity must resolve **under `base_domain`**. Records are created zone-relative in the DNS
authority's zone, so a vanity on another domain (`shop.other.net`) would not be a record on that
domain — it would deploy as `shop.other.net.<baseDomain>`, a wrong hostname whose ACME certificate
could never issue. `inforge validate` rejects it, and `preview`/`deploy` fail before any record is
written.

Each `tls-termination` FQDN (auto + vanity) gets a DNS record and is listed in the service's ACME
certificate (`server_name`). A `forward` entry gets the `<svc>.svc` DNS record but no certificate (the
backend owns TLS).

:::caution Vanity and multiple regions
The auto `<svc>.svc` name embeds the region slug, so it is unique per region. A **region-independent**
vanity (a literal FQDN, or one using only `{BASE_DOMAIN}`) does not — if the service deploys to more
than one region, inforge creates the same DNS name in each region pointing at a different host IP.
Scope such a vanity per region with `{REGION_SLUG}` (or a bare token, which is auto-scoped) unless you
intend the round-robin.
:::

## East-west service mesh

Services call **each other** over a derived **east-west mesh** (never the public ingress). Any service
that declares `pki:` is a mesh member; inforge materializes a per-host **mesh proxy** (a second, private
nginx, separate from the north-south ingress) on every host running a mesh member, and generates its
routing table from the set of mesh services and their hosts. There is **no mesh resource to author** —
the only authoring surface is the optional per-service `mesh:` block.

```yaml
mesh:
  port: 8080                              # loopback port this service binds to RECEIVE mesh traffic (plain HTTP)
  allowed_services: [tunneller, gateway]  # callee-side allow list: who may call this service
  public_paths:                           # exposed at the internet edge (via a gateway) AND to mesh peers
    - /api/v*/tenants/**
  internal_paths:                         # mesh peers ONLY — never served by a gateway
    - /admin/**
```

| Field | Type | Meaning |
|-------|------|---------|
| `mesh.port` | int | The `127.0.0.1:<port>` the service binds to **receive** mesh traffic (plain HTTP). Injected as `INFORGE_MESH_PORT`. Omit the whole block for a service that only makes outbound calls. |
| `mesh.allowed_services` | list | Bare service names permitted to call this service. Enforced at **this** service's local mesh proxy — a disallowed caller is rejected before it reaches the service. Include `gateway` to be reachable by daemon traffic through the [north-south gateway](./gateway). Empty means no service peers may call it. |
| `mesh.public_paths` | list | Absolute path globs exposed **at the internet edge** through a [gateway](./gateway) that lists this service, **and** admitted from mesh peers. See [Path-level exposure](#path-level-exposure). |
| `mesh.internal_paths` | list | Absolute path globs admitted from **mesh peers only** — never served by a gateway, internet-invisible even on a gateway-listed service. |

**Calling another service.** Read `INFORGE_MESH_URL` and name the target in the `X-Mesh-Target` header;
the path is your real path, unchanged:

```
GET $INFORGE_MESH_URL/api/foo      # header: X-Mesh-Target: tunneller
```

The local mesh resolves the target's **current** location — loopback (co-located), a private IP (same
region, other host), or the **global mesh gateway** (cross-scope) — and does the `nginx↔nginx` mTLS hop,
presenting this caller's leaf (`CN=<scope>/<service>`). The callee's mesh verifies the client cert,
enforces `allowed_services`, checks the request path against the callee's
[declared paths](#path-level-exposure) (`public_paths ∪ internal_paths` — an undeclared path is a
JSON `404`, even for an allowed peer), and forwards over loopback with the verified caller in
`X-Service-Identity`.
The service itself does **no TLS** for east-west traffic — plain HTTP in and out. Because the target is
addressed by name (not location), a callee moving to another host regenerates routing only: the caller's
URL is byte-identical.

**Direction.** A regional service may call same-region services and any global service (regional→global);
a global service may call only global services. This falls out of the topology — regional meshes are
private-only, and only the global scope exposes a public mesh gateway.

### Path-level exposure

A mesh callee declares its **entire endpoint surface** as absolute path globs in the `mesh:` block —
two lists with different reach:

| List | Reachable from |
|------|----------------|
| `mesh.public_paths` | The **internet edge**, through a [gateway](./gateway) that lists this service in its `services:` — **and** mesh peers. |
| `mesh.internal_paths` | **Mesh peers only.** Never served by a gateway — internet-invisible even when the service is gateway-listed. |

**Closed by default.** A service with a `mesh:` block must declare **at least one path** across the
two lists — the callee's mesh proxy proxies **only** declared paths and answers a JSON `404` for
anything else, *even for an allowed, authenticated peer*. An undeclared handler is unreachable from
everywhere: shipping a new endpoint deliberately touches the manifest. A gateway-listed service must
additionally declare ≥1 **public** path.

**Glob syntax.** Paths are absolute (`/…`); segment charset is `[A-Za-z0-9._-]`:

- `*` matches **exactly one** path segment of one-or-more characters, and combines with literals:
  `/v*/` matches `/v1/`, `/v2beta/`, …;
- a **trailing** `/**` segment matches the parent node itself **plus any deeper tail**:
  `/api/tenants/**` matches `/api/tenants`, `/api/tenants/42`, `/api/tenants/42/keys`;
- `..` is forbidden, and `**` may only appear as the final segment (no mid-pattern `**`);
- the bare root glob `/**` is rejected — it matches **every** request path, defeating the allowlist;
  declare concrete globs (e.g. `/v1/**`).

**Overlap is a validation error.** Because the gateway's routing table is derived, the globs are the
only thing preventing two services claiming one path: public globs of **different services listed on
one gateway** must not overlap, and a single service's own **public and internal globs** must not
overlap (a path cannot be both). `inforge validate` rejects both.

:::danger `public_paths` means internet-reachable
On a gateway-listed service, **everything in `public_paths` is reachable from the public internet**
— the gateway forwards any matching path, and the daemon's JWT is validated only *after* the request
reaches your handler. Peer-only endpoints — admin surfaces, reconciliation hooks, internal RPC —
belong in `internal_paths`, where no gateway will ever serve them.
:::

**Leaf custody.** The mesh proxy — not the service — holds every co-located service's mesh leaf and the
trust bundle, in its host's persistent `leaf.age`: [`inforge pki renew`](/cli/pki) mints fresh material
and SSH-pushes the updated `leaf.age` directly to each mesh host, then reload-or-restarts the proxy so
it picks it up immediately. At boot, `inforge-agent` decrypts `leaf.age` locally with the host's own
SSH key, so a reboot always restores real certificates from disk with no network round-trip. A service
therefore ships **no TLS code and no cert files** for east-west traffic. The one exception is
`mtls_files: true`: a service running its own raw mTLS listener outside the mesh (e.g. an inter-node
forward plane on an exposed port) additionally gets its own leaf + bundle projected into its tmpfs from
its own `leaf.age` with the `MTLS_*_PATH` env vars — a second, independent leaf; the mesh proxy's copy
is unaffected.

## Example

```yaml title="regional/service/bridge/manifest.yaml"
name: bridge
container: bridge
host: bridge
type: raw
user: bridge
ingress: edge             # the ingress resource whose nginx fronts these routes
routes:
  - type: tls-termination # ACME TLS on :443 for bridge.svc.<env>.<slug>.<base> + the vanity names
    listen: 443
    target: 8080
    vanity:
      - key-broker.{BASE_DOMAIN}
      - key-broker.inforge.wardnet.network
  - type: forward         # raw L4 forward of :853 to a backend (PROXY protocol)
    listen: 853
    target: 5353
```

```yaml title="regional/service/bridge/environment.yaml"
SESSION_KEY: vault:SESSION_KEY
```

```yaml title="regional/service/bridge/manifest.yaml (grants:)"
grants:
  - resource: database/main       # a scoped per-service DB role, materialized as env vars
    permission: rw
    outputs:
      DATABASE_URL: "{URL}"
```

## Runtime environment

Every service is started by `inforge-agent` (the systemd `ExecStart`), which builds the process
environment from a minimal base (`PATH`, `HOME`, `USER`, `LOGNAME`), then injects:

- the service's **environment variables** (from `environment.yaml`; see [Secrets](./secrets)), and
- the **observability/deployment context** as `INFORGE_*` variables — derived, non-secret values
  describing where the service is running and which signals it emits. These are present for **every**
  service, including secret-less ones, and double as OpenTelemetry resource attributes.

| Variable | Example | OTel attribute | Meaning |
|----------|---------|----------------|---------|
| `INFORGE_SERVICE_NAMESPACE` | `bridge` | `service.namespace` | The service name — stable across all of its instances, restarts and regions. |
| `INFORGE_INSTANCE_ID` | `9f3a…` | `service.instance.id` | A unique id **regenerated on every (re)start** — distinguishes replicas and restarts. |
| `INFORGE_HOST_ID` | `wardnet-prd-use1-vm-bridge-01` | `host.id` | The host's full VM resource name — **stable per host** (does not change across restarts). |
| `INFORGE_DEPLOYMENT_REGION` | `us-east-1` | — | Abstract region the service is deployed to. |
| `INFORGE_DEPLOYMENT_REGION_SLUG` | `use1` | `region` | Region slug used in resource names and FQDNs. |
| `INFORGE_DEPLOYMENT_ENV` | `prd` | `deployment.environment.name` | Environment name (the `resources/<env>/` directory). |
| `INFORGE_DEPLOYMENT_BASE_DOMAIN` | `wardnet.network` | — | The environment's base domain. |
| `INFORGE_DEPLOYMENT_FQDN` | `bridge.svc.prd.use1.wardnet.network` | — | The service's auto-derived public FQDN (`<service>.svc.<env>.<slug>.<base>`). |

A **mesh member** (any service declaring `pki:`) additionally gets the east-west mesh endpoint
contract — see [East-west service mesh](#east-west-service-mesh):

| Variable | Example | Meaning |
|----------|---------|---------|
| `INFORGE_MESH_URL` | `http://127.0.0.1:9500` | Base URL for **outbound** mesh calls: `GET $INFORGE_MESH_URL/<path>` with an `X-Mesh-Target: <service>` header. Plain HTTP over loopback. |
| `INFORGE_MESH_SCOPE` | `us-east-1` | The service's mesh scope — a region name, or the literal `global`. The caller identity a peer presents is `<scope>/<service>`. |
| `INFORGE_MESH_PORT` | `8080` | The loopback port the service **binds to receive** mesh traffic (its `mesh.port`). Present only when the service declares a `mesh:` block (an inbound endpoint). |

The `INFORGE_*` names are reserved — do not map a secret to one of them.
